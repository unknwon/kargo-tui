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

// argoURLMsg carries the discovered Argo CD UI base URL ("" if none found).
type argoURLMsg string

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

// tickCmd schedules the next refresh tick.
func tickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// discoverArgoURLCmd dispatches Argo CD UI URL discovery via the Kargo
// server's GetConfig RPC.
func discoverArgoURLCmd(c *kargo.Client) tea.Cmd {
	return func() tea.Msg {
		return argoURLMsg(c.DiscoverArgoCDBaseURL(context.Background()))
	}
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
