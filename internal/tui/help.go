package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// helpBindings returns the static binding table rendered in the help
// overlay. Lives outside helpView so prepareHelpViewport can format
// it from Update.
func helpBindings() []struct{ section, key, desc string } {
	return []struct{ section, key, desc string }{
		{"Views", "v", "details"},
		{"", "d", "deploys"},
		{"", "c", "controls"},
		{"", "f", "freights"},
		{"", "t", "tree (DAG of stages, expand/collapse with +/-)"},
		{"", "g", "graph (layered DAG with spatial cursor)"},
		{"", "p", "switch project"},
		{"", "?", "show this help overlay"},
		{"Actions", "l", "logs (promotions + events) for selected stage"},
		{"", "D", "diff current vs. candidate freight for selected stage"},
		{"", "P", "promote freight to selected stage"},
		{"", ">", "promote selected stage's freight to every downstream"},
		{"", "o", "open Argo CD application in browser"},
		{"", "s", "cycle sort: name / age / health / last-promo"},
		{"", "y", "yank (copy) selected resource name to clipboard"},
		{"Navigation", "↑/k", "move cursor up"},
		{"", "↓/j", "move cursor down"},
		{"", "wheel", "mouse wheel scrolls cursor / overlay / details"},
		{"", "shift+wheel", "scroll columns (table) / move cursor left/right (graph)"},
		{"", "←", "scroll columns left (table) / collapse node (tree)"},
		{"", "→", "scroll columns right (table) / expand node (tree)"},
		{"", "+/-", "expand / collapse tree node"},
		{"", "enter", "apply filter / toggle tree node"},
		{"", "pgup/pgdn", "page details panel (full-screen mode) / tree"},
		{"", "home/end", "jump to top / bottom"},
		{"Filtering", "/", "start filtering (per list) / name search (graph)"},
		{"", "n / N", "next / previous match (graph search)"},
		{"", "esc", "dismiss details/overlay, then clear filter"},
		{"Other", "r", "refresh now"},
		{"", "M", "toggle mouse capture (off enables terminal text selection)"},
		{"", "q / ctrl+c", "quit"},
		{"Contexts", "C", "switch Kargo context (then press + to log in to a new URL)"},
		{"", "R", "re-login to current context (only when session expired)"},
	}
}

// prepareHelpViewport sizes the help viewport and loads its content
// into m.helpVP. Called from Update when the help overlay opens or the
// terminal resizes — the mutation persists in the reducer so j/k scroll
// keys can move past their no-op state.
func (m *Model) prepareHelpViewport() {
	keyStyle := lipgloss.NewStyle().Foreground(selected).Bold(true).Background(bg)
	descStyle := lipgloss.NewStyle().Foreground(normal).Background(bg)
	sectStyle := lipgloss.NewStyle().Foreground(progressing).Bold(true).Background(bg)

	keyW := 12
	var lines []string
	for _, b := range helpBindings() {
		if b.section != "" {
			lines = append(lines, "")
			lines = append(lines, sectStyle.Render(b.section))
		}
		lines = append(lines,
			"  "+keyStyle.Render(padRight(b.key, keyW))+"  "+descStyle.Render(b.desc))
	}

	w := m.width
	if w <= 0 {
		w = 80
	}
	h := m.height
	if h < 9 {
		h = 9
	}
	// Match the box chrome in helpView: border(2) + Padding(1, 2) → 6 cols,
	// 4 rows; body chrome: header(1) + spacer(1) + spacer(1) + hint(1) → 4 rows.
	innerW := w - 6
	if innerW < 10 {
		innerW = 10
	}
	innerH := h - 4 - 4
	if innerH < 1 {
		innerH = 1
	}
	m.helpVP.SetWidth(innerW)
	m.helpVP.SetHeight(innerH)
	m.helpVP.SetContent(strings.Join(lines, "\n"))
}

// helpView renders the keybindings overlay. The viewport content is
// loaded by prepareHelpViewport from Update; here we just compose the
// frame around m.helpVP.View().
func (m Model) helpView() tea.View {
	titleStyle := lipgloss.NewStyle().Foreground(normal).Bold(true).Background(bg)
	hintStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)

	header := titleStyle.Render("Keybindings")
	hint := hintStyle.Render("j/k scroll · home/end top/bottom · esc/? dismiss")
	body := lipgloss.JoinVertical(lipgloss.Left, header, "", m.helpVP.View(), "", hint)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(muted).
		Background(bg).
		Padding(1, 2).
		Render(body)
	box = paintFrame(box, m.width, m.height)

	v := tea.NewView(box)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = m.activeMouseMode()
	return v
}
