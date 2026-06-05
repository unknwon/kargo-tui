package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"unknwon.dev/kargo-tui/internal/kargo"
)

const refreshInterval = 5 * time.Second

// projectsLoadedMsg carries the result of listing Kargo projects.
type projectsLoadedMsg struct {
	projects []string
	err      error
}

// deploysLoadedMsg carries the result of listing Stages (rendered as
// "deploys" in the UI).
type deploysLoadedMsg struct {
	deploys []kargo.Stage
	err     error
}

// freightsLoadedMsg carries the result of listing Freight.
type freightsLoadedMsg struct {
	freights []kargo.Freight
	err      error
}

// tickMsg fires on the periodic refresh timer.
type tickMsg time.Time

// warehousesRefreshedMsg carries the result of the F shortcut: a
// fan-out RefreshWarehouse call across every warehouse in the current
// project. Refreshed is the number of warehouses the server accepted
// the request for. err is the first failure encountered, if any (the
// fan-out short-circuits so we surface the first error rather than
// continuing past a likely-systemic failure).
type warehousesRefreshedMsg struct {
	project   string
	refreshed int
	err       error
}

// argoShardsMsg carries the discovered Argo CD shard table (empty when none
// are configured or discovery failed).
type argoShardsMsg kargo.ArgoCDShards

// logsLoadedMsg carries the result of fetching Promotions and Events for a
// given stage.
type logsLoadedMsg struct {
	stage  string
	promos []kargo.PromotionEntry
	events []kargo.EventEntry
	err    error
}

// loadProjectsCmd dispatches a list of Kargo projects.
func loadProjectsCmd(c *kargo.Client) tea.Cmd {
	return func() tea.Msg {
		ps, err := c.ListProjects(context.Background())
		return projectsLoadedMsg{projects: ps, err: err}
	}
}

// loadDeploysCmd fetches Stages for the given project; the TUI surfaces them
// as the "deploys" view.
func loadDeploysCmd(c *kargo.Client, project string) tea.Cmd {
	return func() tea.Msg {
		s, err := c.ListStages(context.Background(), project)
		return deploysLoadedMsg{deploys: s, err: err}
	}
}

// loadFreightsCmd fetches Freight for the given project.
func loadFreightsCmd(c *kargo.Client, project string) tea.Cmd {
	return func() tea.Msg {
		f, err := c.ListFreight(context.Background(), project)
		return freightsLoadedMsg{freights: f, err: err}
	}
}

// refreshWarehousesCmd lists every warehouse in the project and fires
// RefreshResource at each one. Server-side refresh is asynchronous, so
// the new Freight only shows up on a subsequent QueryFreight; the
// existing 5s tick picks it up without any extra coordination here.
func refreshWarehousesCmd(c *kargo.Client, project string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		names, err := c.ListWarehouseNames(ctx, project)
		if err != nil {
			return warehousesRefreshedMsg{project: project, err: err}
		}
		for _, n := range names {
			if err := c.RefreshWarehouse(ctx, project, n); err != nil {
				return warehousesRefreshedMsg{project: project, err: err}
			}
		}
		return warehousesRefreshedMsg{project: project, refreshed: len(names)}
	}
}

// tickCmd schedules the next refresh tick.
func tickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// discoverArgoShardsCmd dispatches Argo CD shard discovery via the Kargo
// server's GetConfig RPC.
func discoverArgoShardsCmd(c *kargo.Client) tea.Cmd {
	return func() tea.Msg {
		return argoShardsMsg(c.DiscoverArgoCDShards(context.Background()))
	}
}

// contextLoginMsg carries the result of an in-app SSO login triggered from
// the context picker.
type contextLoginMsg struct {
	name string
	err  error
}

// loginStatusMsg lets the SSO goroutine report progress (e.g. the auth URL
// to visit, "waiting for callback") back to the picker view without ending
// the whole login operation. The picker just stores the latest text.
type loginStatusMsg string

// runContextLoginCmd invokes the supplied login callback on a goroutine.
// The callback receives a `status` reporter that posts loginStatusMsg
// updates back into the TUI via the `send` function (set up by main from
// the running tea.Program). The final outcome is returned as a
// contextLoginMsg.
func runContextLoginCmd(
	login func(ctx context.Context, url string, status func(string)) (string, error),
	ctx context.Context,
	url string,
	send func(tea.Msg),
) tea.Cmd {
	return func() tea.Msg {
		report := func(s string) { send(loginStatusMsg(s)) }
		name, err := login(ctx, url, report)
		return contextLoginMsg{name: name, err: err}
	}
}

// stageEventMsg carries one StageEvent from the WatchStages stream into
// the model. Folded into m.deploys via kargo.MergeStageEvent and
// triggers a refreshRows so the table/tree/graph reflect the change
// without waiting for the next 5s tick.
type stageEventMsg kargo.StageEvent

// stageWatchEndedMsg signals that the WatchStages goroutine exited
// (clean close or error). Carries the error (nil on clean close); the
// model uses this to log a warning and let the tick-based refresh
// continue carrying the load.
type stageWatchEndedMsg struct{ err error }

// promoteResultMsg carries the outcome of a PromoteToStage call. Posted by
// promoteCmd; consumed by the promote overlay's submit step.
type promoteResultMsg struct {
	stage         string
	freight       string
	promotionName string
	phase         string
	err           error
}

// promoteDownstreamResultMsg carries the outcome of a PromoteDownstream
// call (one source stage → every downstream that requested its freight).
type promoteDownstreamResultMsg struct {
	source     string
	freight    string
	promotions int // number of Promotion CRs the server created
	err        error
}

// startStageWatchGoroutine opens a WatchStages stream against the given
// project and pipes events back into the TUI via the supplied send.
// The returned CancelFunc stops the watch (called on project switch /
// context switch / program exit). Returns a no-op cancel and starts
// nothing when any of c, project, or send is missing — without all
// three the watch has nothing to call, no project to scope to, or no
// way to deliver events.
func startStageWatchGoroutine(
	c *kargo.Client,
	project string,
	send func(tea.Msg),
) context.CancelFunc {
	if send == nil || c == nil || project == "" {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		events, errCh := c.WatchStages(ctx, project)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					// Stream ended. WatchStages closes errCh before
					// closing events, so by the time we get here the
					// error (if any) has already been buffered. A clean
					// close yields the zero value with more=false.
					var endErr error
					if e, more := <-errCh; more {
						endErr = e
					}
					send(stageWatchEndedMsg{err: endErr})
					return
				}
				send(stageEventMsg(ev))
			case <-ctx.Done():
				return
			}
		}
	}()
	return cancel
}

// loadLogsCmd fetches Promotions and Events for the given stage.
func loadLogsCmd(c *kargo.Client, project, stageName string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		promos, pErr := c.ListPromotionsForStage(ctx, project, stageName)
		events, eErr := c.ListEventsForStage(ctx, project, stageName)
		err := pErr
		if err == nil {
			err = eErr
		}
		return logsLoadedMsg{stage: stageName, promos: promos, events: events, err: err}
	}
}
