package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"unknwon.dev/kargo-tui/internal/kargo"
)

// openLogsOverlay seeds the overlay state for an in-flight Logs request. The
// actual fetch is dispatched separately as a tea.Cmd so the UI can render a
// spinner.
func (m *Model) openLogsOverlay(stageName string) {
	m.overlay = overlayLogs
	m.overlayLoading = true
	m.overlayError = nil
	m.overlayPromos = nil
	m.overlayEvents = nil
	m.overlayTitle = "Logs · " + stageName
	m.overlayVP.SetContent("loading…")
	m.overlayVP.GotoTop()
}

// openDiffOverlay computes a freight diff for the selected stage: current
// freight (deployed) vs. the most recent freight matching the same warehouse
// (the candidate the next promotion would pick up).
func (m *Model) openDiffOverlay() {
	s := m.selectedStage()
	if s == nil {
		return
	}
	if len(s.CurrentFreight) == 0 {
		m.overlay = overlayDiff
		m.overlayTitle = "Diff · " + s.Name
		m.overlayError = fmt.Errorf("stage has no current freight")
		m.renderDiffOverlay()
		return
	}
	from := m.freightByName(s.CurrentFreight[0])
	to := latestFreightForWarehouse(m.freights, warehouseFromFreight(from))
	m.overlay = overlayDiff
	m.overlayTitle = "Diff · " + s.Name
	m.overlayDiffFrom = from
	m.overlayDiffTo = to
	m.overlayError = nil
	m.renderDiffOverlay()
}

func warehouseFromFreight(f *kargo.Freight) string {
	if f == nil {
		return ""
	}
	return f.Warehouse
}

func latestFreightForWarehouse(freights []kargo.Freight, warehouse string) *kargo.Freight {
	var best *kargo.Freight
	for i := range freights {
		f := &freights[i]
		if warehouse != "" && f.Warehouse != warehouse {
			continue
		}
		if best == nil || f.Created.After(best.Created) {
			best = f
		}
	}
	return best
}

// renderDiffOverlay materializes the diff body into the overlay viewport.
func (m *Model) renderDiffOverlay() {
	keyStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	addStyle := lipgloss.NewStyle().Foreground(healthy).Background(bg)
	delStyle := lipgloss.NewStyle().Foreground(degraded).Background(bg)
	sameStyle := lipgloss.NewStyle().Foreground(normal).Background(bg)
	titleStyle := lipgloss.NewStyle().Foreground(normal).Bold(true).Background(bg)

	var lines []string
	if m.overlayError != nil {
		lines = append(lines, delStyle.Render(m.overlayError.Error()))
		m.overlayVP.SetContent(strings.Join(lines, "\n"))
		return
	}

	from := m.overlayDiffFrom
	to := m.overlayDiffTo
	fromLabel := "—"
	toLabel := "—"
	if from != nil {
		fromLabel = shortFreight(from.Name) + aliasSuffix(from.Alias)
	}
	if to != nil {
		toLabel = shortFreight(to.Name) + aliasSuffix(to.Alias)
	}
	lines = append(lines, titleStyle.Render("Freight"))
	lines = append(lines, delStyle.Render("  - current   "+fromLabel))
	lines = append(lines, addStyle.Render("  + candidate "+toLabel))
	lines = append(lines, "")

	if from == nil || to == nil || from.Name == to.Name {
		lines = append(lines, keyStyle.Render("no pending changes"))
		m.overlayVP.SetContent(strings.Join(lines, "\n"))
		return
	}

	renderSection := func(title string, entries []diffEntry, fmtVal func(string) string) {
		lines = append(lines, titleStyle.Render(title))
		if len(entries) == 0 {
			lines = append(lines, sameStyle.Render("  (unchanged)"))
			return
		}
		for _, d := range entries {
			lines = append(lines, keyStyle.Render("  "+d.repo))
			switch d.kind {
			case '+':
				lines = append(lines, addStyle.Render("  + "+fmtVal(d.id)))
			case '-':
				lines = append(lines, delStyle.Render("  - "+fmtVal(d.id)))
			case '~':
				lines = append(lines, delStyle.Render("  - "+fmtVal(d.id)))
				lines = append(lines, addStyle.Render("  + "+fmtVal(d.note)))
			}
		}
	}

	renderSection("Commits", diffCommits(from.Commits, to.Commits), shortSHA)
	lines = append(lines, "")
	renderSection("Images", diffImages(from.Images, to.Images), identity)
	lines = append(lines, "")
	renderSection("Charts", diffCharts(from.Charts, to.Charts), identity)

	m.overlayVP.SetContent(strings.Join(lines, "\n"))
	m.overlayVP.GotoTop()
}

func identity(s string) string { return s }

type diffEntry struct {
	kind byte // '+', '-', '~'
	repo string
	id   string
	note string
}

func diffCommits(a, b []kargo.FreightCommit) []diffEntry {
	aMap := make(map[string]kargo.FreightCommit)
	bMap := make(map[string]kargo.FreightCommit)
	for _, c := range a {
		aMap[c.RepoURL] = c
	}
	for _, c := range b {
		bMap[c.RepoURL] = c
	}
	var out []diffEntry
	for repo, ac := range aMap {
		bc, ok := bMap[repo]
		if !ok {
			out = append(out, diffEntry{kind: '-', repo: repo, id: ac.ID})
			continue
		}
		if ac.ID != bc.ID {
			out = append(out, diffEntry{kind: '~', repo: repo, id: ac.ID, note: bc.ID})
		}
	}
	for repo, bc := range bMap {
		if _, ok := aMap[repo]; !ok {
			out = append(out, diffEntry{kind: '+', repo: repo, id: bc.ID})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].repo < out[j].repo })
	return out
}

func diffImages(a, b []kargo.FreightImage) []diffEntry {
	aMap := make(map[string]kargo.FreightImage)
	bMap := make(map[string]kargo.FreightImage)
	for _, i := range a {
		aMap[i.RepoURL] = i
	}
	for _, i := range b {
		bMap[i.RepoURL] = i
	}
	var out []diffEntry
	for repo, ai := range aMap {
		bi, ok := bMap[repo]
		if !ok {
			out = append(out, diffEntry{kind: '-', repo: repo, id: imageID(ai)})
			continue
		}
		if imageID(ai) != imageID(bi) {
			out = append(out, diffEntry{kind: '~', repo: repo, id: imageID(ai), note: imageID(bi)})
		}
	}
	for repo, bi := range bMap {
		if _, ok := aMap[repo]; !ok {
			out = append(out, diffEntry{kind: '+', repo: repo, id: imageID(bi)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].repo < out[j].repo })
	return out
}

func imageID(i kargo.FreightImage) string {
	if i.Tag != "" {
		return i.Tag
	}
	return i.Digest
}

func diffCharts(a, b []kargo.FreightChart) []diffEntry {
	aMap := make(map[string]kargo.FreightChart)
	bMap := make(map[string]kargo.FreightChart)
	for _, c := range a {
		aMap[c.RepoURL+"/"+c.Name] = c
	}
	for _, c := range b {
		bMap[c.RepoURL+"/"+c.Name] = c
	}
	var out []diffEntry
	for k, ac := range aMap {
		bc, ok := bMap[k]
		if !ok {
			out = append(out, diffEntry{kind: '-', repo: k, id: ac.Version})
			continue
		}
		if ac.Version != bc.Version {
			out = append(out, diffEntry{kind: '~', repo: k, id: ac.Version, note: bc.Version})
		}
	}
	for k, bc := range bMap {
		if _, ok := aMap[k]; !ok {
			out = append(out, diffEntry{kind: '+', repo: k, id: bc.Version})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].repo < out[j].repo })
	return out
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func shortFreight(name string) string {
	if isFreightName(name) {
		return name[:8]
	}
	return name
}

func aliasSuffix(alias string) string {
	if alias == "" {
		return ""
	}
	return " (" + alias + ")"
}

// renderLogs builds the overlay body for a Logs view from the loaded
// Promotions and Events lists.
func (m *Model) renderLogs() {
	keyStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	titleStyle := lipgloss.NewStyle().Foreground(normal).Bold(true).Background(bg)
	valStyle := lipgloss.NewStyle().Foreground(normal).Background(bg)

	var lines []string
	if m.overlayError != nil {
		lines = append(lines, lipgloss.NewStyle().Foreground(degraded).Background(bg).Render(m.overlayError.Error()))
	}
	lines = append(lines, titleStyle.Render("Promotions"))
	if len(m.overlayPromos) == 0 {
		lines = append(lines, keyStyle.Render("  (none)"))
	}
	for _, p := range m.overlayPromos {
		lines = append(lines, valStyle.Render(fmt.Sprintf("  %s %s %s",
			ageString(p.Created), promoCell(p.Phase), p.Name)))
		if p.Freight != "" {
			lines = append(lines, keyStyle.Render("    freight: "+shortFreight(p.Freight)))
		}
		if p.Message != "" {
			lines = append(lines, keyStyle.Render("    "+p.Message))
		}
	}
	lines = append(lines, "")
	lines = append(lines, titleStyle.Render("Events"))
	if len(m.overlayEvents) == 0 {
		lines = append(lines, keyStyle.Render("  (none)"))
	}
	for _, e := range m.overlayEvents {
		etype := e.Type
		style := valStyle
		if etype == "Warning" {
			style = lipgloss.NewStyle().Foreground(degraded).Background(bg)
		}
		count := ""
		if e.Count > 1 {
			count = fmt.Sprintf(" ×%d", e.Count)
		}
		lines = append(lines, style.Render(fmt.Sprintf("  %s %s %s%s — %s",
			ageString(e.Last), etype, e.Reason, count, e.Source)))
		if e.Message != "" {
			lines = append(lines, keyStyle.Render("    "+e.Message))
		}
	}
	m.overlayVP.SetContent(strings.Join(lines, "\n"))
}

// overlayView renders the active full-screen overlay (Logs or Diff).
func (m Model) overlayView() tea.View {
	titleStyle := lipgloss.NewStyle().Foreground(normal).Bold(true).Background(bg)
	hintStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)

	header := titleStyle.Render(m.overlayTitle)
	hint := hintStyle.Render("esc/enter dismiss · j/k scroll · g/G top/bottom")
	if m.overlayLoading {
		hint = hintStyle.Render("loading…  esc to cancel")
	}

	w := m.width
	if w <= 0 {
		w = 80
	}
	h := m.height - 4
	if h < 5 {
		h = 5
	}
	m.overlayVP.SetWidth(w - 4)
	m.overlayVP.SetHeight(h - 2)

	body := lipgloss.JoinVertical(lipgloss.Left, header, "", m.overlayVP.View(), "", hint)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(muted).
		Background(bg).
		Padding(1, 2).
		Render(body)

	v := tea.NewView(box)
	v.AltScreen = true
	v.BackgroundColor = bg
	return v
}
