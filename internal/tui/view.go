package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"go.opentelemetry.io/otel/attribute"

	"unknwon.dev/kargo-tui/internal/tracing"
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
	ctx, span := tracing.Start(context.Background(), "View")
	defer span.End()
	// Install the View span's ctx so render helpers like paintFrame nest
	// their spans under it without us having to thread ctx through every
	// view method's signature.
	resetAmbient := tracing.SetAmbient(ctx)
	defer resetAmbient()
	if span.IsRecording() {
		span.SetAttributes(
			attribute.Stringer("view", m.view),
			attribute.Stringer("phase", m.phase),
			attribute.Bool("detailsOnly", m.detailsOnly),
			attribute.Int("width", m.width),
			attribute.Int("height", m.height),
		)
	}
	defer func() {
		if r := recover(); r != nil {
			// Prefer the freshly-recovered trace over m.panicMessage:
			// a panic here means panicView (or the fallback path) just
			// failed, so the most useful trace to surface is the new
			// one, not whatever the previous Update panic recorded.
			v = renderPanicFallback(formatPanic(r), m.width, m.height)
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
		body = m.deploysTable.View()
		if m.deploysError != nil {
			errLine = m.deploysError.Error()
		}
	case viewControlFlow:
		title = "controls"
		count = len(m.deploysTable.Rows())
		body = m.deploysTable.View()
		if m.deploysError != nil {
			errLine = m.deploysError.Error()
		}
	case viewFreights:
		title = "freights"
		count = len(m.freightsTable.Rows())
		body = m.freightsTable.View()
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
		yankColor := healthy
		if m.yankedIsError {
			yankColor = degraded
		}
		filterLine = lipgloss.NewStyle().Foreground(yankColor).Background(bg).Padding(0, 1).Render(m.yankedMessage)
	} else if errLine != "" {
		filterLine = lipgloss.NewStyle().Foreground(degraded).Background(bg).Padding(0, 1).Render(errLine)
	} else {
		hint := "press / to filter"
		if m.view == viewFreights {
			hint += " · counts: deploy/control-flow stages"
		}
		if mode := m.sort[m.view]; mode != sortDefault {
			hint += " · sort: " + mode.String()
		}
		filterLine = lipgloss.NewStyle().Foreground(muted).Background(bg).Padding(0, 1).Render(hint)
	}

	// Bottom hint is intentionally short: up to 5 most-used keys per
	// view plus ? for the full keybindings panel. Everything else lives
	// in the help overlay.
	var helpText string
	switch m.view {
	case viewDeploys:
		helpText = "v details · P promote · l logs · D diff · / filter · ? help"
	case viewControlFlow:
		helpText = "v details · P promote · > downstream · l logs · / filter · ? help"
	case viewFreights:
		helpText = "v details · y yank · s sort · / filter · g graph · ? help"
	}
	help := lipgloss.NewStyle().Foreground(muted).Background(bg).Padding(0, 1).Render(helpText)

	content := lipgloss.JoinVertical(lipgloss.Left, header, body, filterLine, help)
	content = m.composeWithMenu(content)
	content = paintFrame(content, m.width, m.height)

	view := tea.NewView(content)
	view.AltScreen = true
	view.BackgroundColor = bg
	view.MouseMode = m.activeMouseMode()
	if m.filtering {
		if c := m.filter.Cursor(); c != nil {
			c.Y += lipgloss.Height(header) + lipgloss.Height(body)
			view.Cursor = c
		}
	}
	return view
}

// renderAuthBanner renders the persistent "session expired" status line
// that supersedes both the 3-second yank flash and per-view error lines
// while m.authExpired is set. Bright red so it's hard to miss; the inline
// re-login affordance (`R`) is documented in the line.
func (m Model) renderAuthBanner() string {
	msg := "session expired. Press R to re-login (or C to switch context)"
	if m.authExpiredMsg != "" {
		msg = "session expired (" + m.authExpiredMsg + "). Press R to re-login (or C to switch context)"
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

	// Bottom hint is intentionally short: up to 5 most-used keys for
	// this context plus ? for the full keybindings panel.
	var hintText string
	switch m.view {
	case viewFreights:
		hintText = "v/esc back · y yank · ? help"
	case viewControlFlow:
		hintText = "v/esc back · P promote · > downstream · l logs · D diff · ? help"
	default:
		hintText = "v/esc back · P promote · l logs · D diff · o argo · ? help"
	}
	hint := hintStyle.Render(hintText)
	content := lipgloss.JoinVertical(lipgloss.Left, header, body, hint)
	content = paintFrame(content, m.width, m.height)

	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = m.activeMouseMode()
	return v
}
