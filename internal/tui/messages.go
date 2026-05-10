package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"unknwon.dev/kargo-tui/internal/kargo"
)

const refreshInterval = 5 * time.Second

// namespacesLoadedMsg carries the result of listing Kargo project
// namespaces. Used by the picker.
type namespacesLoadedMsg struct {
	namespaces []string
	err        error
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

// loadNamespacesCmd dispatches a list of Kargo project namespaces.
func loadNamespacesCmd() tea.Cmd {
	return func() tea.Msg {
		ns, err := kargo.ListProjects(context.Background())
		return namespacesLoadedMsg{namespaces: ns, err: err}
	}
}

// loadDeploysCmd fetches Stages for the given namespace; the TUI surfaces
// them as the "deploys" view.
func loadDeploysCmd(ns string) tea.Cmd {
	return func() tea.Msg {
		s, err := kargo.ListStages(context.Background(), ns)
		return deploysLoadedMsg{deploys: s, err: err}
	}
}

// loadFreightsCmd fetches Freight for the given namespace.
func loadFreightsCmd(ns string) tea.Cmd {
	return func() tea.Msg {
		f, err := kargo.ListFreight(context.Background(), ns)
		return freightsLoadedMsg{freights: f, err: err}
	}
}

// tickCmd schedules the next refresh tick.
func tickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// discoverArgoURLCmd dispatches Argo CD UI URL discovery.
func discoverArgoURLCmd() tea.Cmd {
	return func() tea.Msg {
		return argoURLMsg(kargo.DiscoverArgoCDBaseURL(context.Background()))
	}
}

// loadLogsCmd fetches Promotions and Events for the given stage in parallel
// (sequentially within the goroutine, conceptually parallel from the UI's
// point of view).
func loadLogsCmd(namespace, stageName string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		promos, pErr := kargo.ListPromotionsForStage(ctx, namespace, stageName)
		events, eErr := kargo.ListEventsForStage(ctx, namespace, stageName)
		err := pErr
		if err == nil {
			err = eErr
		}
		return logsLoadedMsg{stage: stageName, promos: promos, events: events, err: err}
	}
}
