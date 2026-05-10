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
	if m.overlay != overlayNone {
		return m.overlayView()
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
		Render("d deploys · c controls · f freights · v details · l logs · D diff · o argo · s sort · y yank · p projects · C contexts · / filter · ? help · q quit")

	bodyArea := body

	if m.detailsOnly {
		// Full-width details panel, suitable for narrow terminals.
		panelHeight := m.height - 4
		if panelHeight < 5 {
			panelHeight = 5
		}
		w := m.width
		if w <= 0 {
			w = 80
		}
		bodyArea = m.renderPanel(w, panelHeight)
	}

	content := lipgloss.JoinVertical(lipgloss.Left, header, bodyArea, filterLine, help)

	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
