package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// View renders the current frame. It dispatches between the project
// picker, the help overlay, the logs/diff overlay, and the main
// table+details layout depending on Model state.
func (m Model) View() tea.View {
	if m.phase == phasePickingProject {
		return m.pickerView()
	}
	if m.phase == phasePickingContext {
		return m.contextPickerView()
	}
	if m.showHelp {
		return m.helpView()
	}
	if m.overlay == overlayPromote {
		return m.promoteOverlayView()
	}
	if m.overlay != overlayNone {
		return m.overlayView()
	}
	// detailsOnly takes precedence over the structural views — `v` should
	// flip the whole frame to the details panel regardless of whether the
	// user was in tree, graph, or a list view.
	if m.detailsOnly {
		return m.detailsOnlyView()
	}
	if m.view == viewTree {
		return m.treeView()
	}
	if m.view == viewGraph {
		return m.graphView()
	}
	var (
		title   string
		count   int
		body    string
		errLine string
	)
	switch m.view {
	case viewDeploys:
		title = "deploys"
		count = len(m.deploysTable.Rows())
		body = lipgloss.NewStyle().Background(bg).Render(m.deploysTable.View())
		if m.deploysError != nil {
			errLine = m.deploysError.Error()
		}
	case viewControlFlow:
		title = "controls"
		count = len(m.deploysTable.Rows())
		body = lipgloss.NewStyle().Background(bg).Render(m.deploysTable.View())
		if m.deploysError != nil {
			errLine = m.deploysError.Error()
		}
	case viewFreights:
		title = "freights"
		count = len(m.freightsTable.Rows())
		body = lipgloss.NewStyle().Background(bg).Render(m.freightsTable.View())
		if m.freightsError != nil {
			errLine = m.freightsError.Error()
		}
	}

	headerText := fmt.Sprintf("kargo-tui · %s · %s · project=%s · %d items",
		title, m.contextName, m.project, count)
	if m.loading {
		headerText += " · refreshing…"
	}
	header := lipgloss.NewStyle().
		Foreground(normal).Background(bg).Bold(true).
		Padding(0, 1).
		Render(headerText)

	var filterLine string
	if m.filtering || m.filter.Value() != "" {
		filterLine = lipgloss.NewStyle().Background(bg).Render(m.filter.View())
	} else if m.yankedMessage != "" && time.Since(m.yankedAt) < 3*time.Second {
		filterLine = lipgloss.NewStyle().Foreground(healthy).Background(bg).Padding(0, 1).Render(m.yankedMessage)
	} else if errLine != "" {
		filterLine = lipgloss.NewStyle().Foreground(degraded).Background(bg).Padding(0, 1).Render(errLine)
	} else {
		hint := "press / to filter"
		if mode := m.sort[m.view]; mode != sortDefault {
			hint += " · sort: " + mode.String()
		}
		filterLine = lipgloss.NewStyle().Foreground(muted).Background(bg).Padding(0, 1).Render(hint)
	}

	help := lipgloss.NewStyle().Foreground(muted).Background(bg).Padding(0, 1).
		Render("d deploys · c controls · f freights · t tree · g graph · v details · l logs · D diff · P promote · > downstream · o argo · s sort · y yank · p projects · C contexts · / filter · ? help · q quit")

	content := lipgloss.JoinVertical(lipgloss.Left, header, body, filterLine, help)

	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// detailsOnlyView fills the whole frame with the side details panel —
// the layout `v` toggles into. Works from any structural view since the
// panel's content is selection-driven (selectedStage / selectedFreight)
// and those helpers are view-aware.
func (m Model) detailsOnlyView() tea.View {
	titleStyle := lipgloss.NewStyle().Foreground(normal).Bold(true).Background(bg).Padding(0, 1)
	hintStyle := lipgloss.NewStyle().Foreground(muted).Background(bg).Padding(0, 1)

	var label string
	switch m.view {
	case viewDeploys:
		label = "deploys"
	case viewControlFlow:
		label = "controls"
	case viewFreights:
		label = "freights"
	case viewTree:
		label = "tree"
	case viewGraph:
		label = "graph"
	}
	header := titleStyle.Render(fmt.Sprintf("kargo-tui · %s · %s · project=%s · details",
		label, m.contextName, m.project))

	panelHeight := m.height - 3
	if panelHeight < 5 {
		panelHeight = 5
	}
	w := m.width
	if w <= 0 {
		w = 80
	}
	body := m.renderPanel(w, panelHeight)

	hint := hintStyle.Render("v back · j/k pgup/pgdn g/G scroll · l logs · D diff · P promote · esc back · q quit")
	content := lipgloss.JoinVertical(lipgloss.Left, header, body, hint)

	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
