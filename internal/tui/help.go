package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"unknwon.dev/kargo-tui/internal/config"
)

// Build information surfaced in the bottom of the help overlay. Populated
// from main via SetBuildInfo so the tui package doesn't need to import
// anything from cmd/.
var (
	buildVersion = "dev"
	buildCommit  = "unknown"
	buildDate    = "unknown"
)

// SetBuildInfo records the build metadata shown in the help overlay's
// footer. Call from main before launching the program.
func SetBuildInfo(version, commit, date string) {
	if version != "" {
		buildVersion = version
	}
	if commit != "" {
		buildCommit = commit
	}
	if date != "" {
		buildDate = date
	}
}

// shortCommit trims a full git SHA to its first 7 characters. Non-SHA
// fallbacks like "unknown" are returned unchanged.
func shortCommit(s string) string {
	if len(s) >= 7 && isHex(s) {
		return s[:7]
	}
	return s
}

func isHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func helpConfigPath() string {
	p, err := config.Path()
	if err != nil {
		return "unavailable"
	}
	return p
}

// helpBindings returns the binding table rendered in the help overlay.
// Lives outside helpView so prepareHelpViewport can format it from Update.
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
		{"", "config file", helpConfigPath()},
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
	// Match the box chrome in helpView: border(2) + Padding(1, 2) -> 6 cols,
	// 4 rows; body chrome: header(1) + spacer(1) + spacer(1) + hint(1) +
	// build(1) -> 5 rows.
	innerW := w - 6
	if innerW < 10 {
		innerW = 10
	}
	innerH := h - 4 - 5
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
	build := hintStyle.Render("kargo-tui " + buildVersion + " · " + shortCommit(buildCommit) + " · built " + buildDate)
	body := lipgloss.JoinVertical(lipgloss.Left, header, "", m.helpVP.View(), "", hint, build)

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
