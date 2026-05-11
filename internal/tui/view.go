package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// View renders the current frame. It dispatches, in priority order,
// between the panic popup, project picker, context picker, help overlay,
// promote overlay, logs/diff overlay, full-screen details panel, tree
// view, graph view, and the default table+details layout.
//
// View wraps the real renderer in a panic recovery shim. A panic during
// rendering would otherwise tear the program down with the alt-screen
// still active; instead we fall back to a plain-text panic frame so the
// trace stays visible and copyable.
func (m Model) View() (v tea.View) {
	defer func() {
		if r := recover(); r != nil {
			msg := m.panicMessage
			if msg == "" {
				msg = formatPanic(r)
			}
			v = renderPanicFallback(msg, m.width, m.height)
		}
	}()
	if m.panicMessage != "" {
		return m.panicView()
	}
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
	} else if m.authExpired {
		filterLine = m.renderAuthBanner()
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

	// Each list view gets a tailored hint line — controls don't have
	// per-stage promotions in the same sense as deploys, freights have
	// no logs / diff / argo actions at all, so reusing one generic
	// line either lies about what works here or buries the action
	// keys the user actually has. Common keys (s sort, / filter, p
	// projects, C contexts, ? help, q quit) appear in all three.
	var helpText string
	switch m.view {
	case viewDeploys:
		helpText = "↑/↓ select · ←/→ scroll cols · v details · l logs · D diff · P promote · > downstream · o argo · y yank · s sort · / filter · t tree · g graph · c controls · f freights · p projects · C contexts · ? help · q quit"
	case viewControlFlow:
		helpText = "↑/↓ select · ←/→ scroll cols · v details · l logs · D diff · P promote · > downstream · y yank · s sort · / filter · t tree · g graph · d deploys · f freights · p projects · C contexts · ? help · q quit"
	case viewFreights:
		helpText = "↑/↓ select · ←/→ scroll cols · v details · y yank · s sort · / filter · t tree · g graph · d deploys · c controls · p projects · C contexts · ? help · q quit"
	}
	help := lipgloss.NewStyle().Foreground(muted).Background(bg).Padding(0, 1).Render(helpText)

	content := lipgloss.JoinVertical(lipgloss.Left, header, body, filterLine, help)

	view := tea.NewView(content)
	view.AltScreen = true
	view.BackgroundColor = bg
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

// renderAuthBanner renders the persistent "session expired" status line
// that supersedes both the 3-second yank flash and per-view error lines
// while m.authExpired is set. Bright red so it's hard to miss; the inline
// re-login affordance (`R`) is documented in the line.
func (m Model) renderAuthBanner() string {
	msg := "session expired — press R to re-login (or C to switch context)"
	if m.authExpiredMsg != "" {
		msg = "session expired (" + m.authExpiredMsg + ") — press R to re-login (or C to switch context)"
	}
	return lipgloss.NewStyle().
		Foreground(normal).Background(degraded).Bold(true).
		Padding(0, 1).
		Render(msg)
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

	// detailsOnly is contextual to whichever structural view is behind it
	// (deploys / controls / freights / tree / graph). Freights don't
	// expose logs / diff / promote, so we trim those from the hint when
	// the user toggled v from the freights list.
	var hintText string
	if m.view == viewFreights {
		hintText = "v back · j/k pgup/pgdn home/end scroll · y yank · esc back · q quit"
	} else {
		hintText = "v back · j/k pgup/pgdn home/end scroll · l logs · D diff · P promote · > downstream · o argo · y yank · esc back · q quit"
	}
	hint := hintStyle.Render(hintText)
	content := lipgloss.JoinVertical(lipgloss.Left, header, body, hint)

	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
