package tui

import (
	"fmt"
	"runtime/debug"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// formatPanic builds the body of the panic popup: a one-line summary of
// the recovered value followed by the stack trace captured at recover
// time. Kept self-contained so it can also be used from the View-time
// fallback path where the model viewport isn't usable.
func formatPanic(r any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "panic: %v\n\n", r)
	b.Write(debug.Stack())
	return b.String()
}

// preparePanicViewport sizes the panic viewport and loads its content
// into m.panicVP. Called from the Update recover handler so the popup
// appears with a usable scroll position on the next View.
func (m *Model) preparePanicViewport() {
	w := m.width
	if w <= 0 {
		w = 80
	}
	h := m.height - 4
	if h < 5 {
		h = 5
	}
	m.panicVP.SetWidth(w - 4)
	m.panicVP.SetHeight(h - 4)
	m.panicVP.SetContent(m.panicMessage)
	m.panicVP.GotoTop()
}

// panicView renders the recovered-panic popup. The trace lives in a
// scrollable viewport so even a deep stack remains accessible, and the
// raw text is preserved verbatim so terminal selection lets the user
// copy the trace into a bug report.
func (m Model) panicView() tea.View {
	titleStyle := lipgloss.NewStyle().Foreground(degraded).Bold(true).Background(bg)
	hintStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)

	header := titleStyle.Render("panic recovered — select to copy")
	hint := hintStyle.Render("j/k scroll · pgup/pgdn page · home/end top/bottom · esc dismiss")
	body := lipgloss.JoinVertical(lipgloss.Left, header, "", m.panicVP.View(), "", hint)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(degraded).
		Background(bg).
		Padding(1, 2).
		Render(body)

	v := tea.NewView(box)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// renderPanicFallback is the last-resort frame used when the regular
// View itself panicked. It deliberately avoids any of the model's
// helpers (which we just proved aren't safe) and emits a plain styled
// block with the trace.
func renderPanicFallback(message string, width, height int) tea.View {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	titleStyle := lipgloss.NewStyle().Foreground(degraded).Bold(true).Background(bg)
	hintStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	bodyStyle := lipgloss.NewStyle().Foreground(normal).Background(bg)

	header := titleStyle.Render("panic recovered (render-time) — select to copy")
	hint := hintStyle.Render("esc dismiss")

	// Truncate to the available height so the popup doesn't push the
	// hint off-screen. We keep the head of the stack — that's where the
	// originating frame lives.
	maxLines := height - 6
	if maxLines < 4 {
		maxLines = 4
	}
	lines := strings.Split(message, "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], "… (trace truncated to fit terminal)")
	}
	body := bodyStyle.Render(strings.Join(lines, "\n"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(degraded).
		Background(bg).
		Padding(1, 2).
		Width(width - 4).
		Render(lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", hint))

	v := tea.NewView(box)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
