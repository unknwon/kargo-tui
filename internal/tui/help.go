package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// helpView renders the keybindings overlay. The body lives inside a
// scrollable viewport so it stays usable on small terminals.
func (m Model) helpView() tea.View {
	titleStyle := lipgloss.NewStyle().Foreground(normal).Bold(true).Background(bg)
	keyStyle := lipgloss.NewStyle().Foreground(selected).Bold(true).Background(bg)
	descStyle := lipgloss.NewStyle().Foreground(normal).Background(bg)
	hintStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	sectStyle := lipgloss.NewStyle().Foreground(progressing).Bold(true).Background(bg)

	bindings := []struct{ section, key, desc string }{
		{"Views", "d", "deploys"},
		{"", "c", "controls"},
		{"", "f", "freights"},
		{"", "t", "tree (DAG of stages, expand/collapse with +/-)"},
		{"", "p", "switch project"},
		{"", "v", "toggle full-screen details"},
		{"", "?", "show this help overlay"},
		{"Actions", "l", "logs (promotions + events) for selected stage"},
		{"", "D", "diff current vs. candidate freight for selected stage"},
		{"", "P", "promote freight to selected stage"},
		{"", "o", "open Argo CD application in browser"},
		{"", "s", "cycle sort: name / age / health / last-promo"},
		{"", "y", "yank (copy) selected resource name to clipboard"},
		{"Navigation", "↑/k", "move cursor up"},
		{"", "↓/j", "move cursor down"},
		{"", "wheel", "mouse wheel scrolls cursor / overlay / details"},
		{"", "←", "scroll columns left (table) / collapse node (tree)"},
		{"", "→", "scroll columns right (table) / expand node (tree)"},
		{"", "+/-", "expand / collapse tree node"},
		{"", "enter", "apply filter / toggle tree node"},
		{"", "pgup/pgdn", "page details panel (full-screen mode) / tree"},
		{"", "g/G", "jump to top / bottom of details panel or tree"},
		{"Filtering", "/", "start filtering (per list)"},
		{"", "esc", "dismiss details/overlay, then clear filter"},
		{"Other", "r", "refresh now"},
		{"", "q / ctrl+c", "quit"},
		{"Contexts", "C", "switch Kargo context (then press + to log in to a new URL)"},
	}

	keyW := 12
	var lines []string
	for _, b := range bindings {
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
	h := m.height - 4
	if h < 5 {
		h = 5
	}
	m.helpVP.SetWidth(w - 4)
	m.helpVP.SetHeight(h - 4)
	m.helpVP.SetContent(strings.Join(lines, "\n"))

	header := titleStyle.Render("Keybindings")
	hint := hintStyle.Render("j/k scroll · g/G top/bottom · esc/? dismiss")
	body := lipgloss.JoinVertical(lipgloss.Left, header, "", m.helpVP.View(), "", hint)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(muted).
		Background(bg).
		Padding(1, 2).
		Render(body)

	v := tea.NewView(box)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
