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
	m.overlayLogsTab = logsTabPromotions
	m.overlayStageName = stageName
	m.overlayTitle = "Logs · " + stageName
	m.overlayVP.SetContent("loading…")
	m.overlayVP.GotoTop()
}

// openDiffOverlay computes a freight diff for the selected stage: current
// freight (deployed) vs. the most recent freight matching the same warehouse
// (the candidate the next promotion would pick up).
func (m *Model) openDiffOverlay() {
	m.openDiffOverlayForStage(m.selectedStage())
}

// openDiffOverlayForStage is the stage-scoped variant used by the right-click
// context menu so the diff always targets the clicked stage even if the
// underlying selection shifts before the action fires.
func (m *Model) openDiffOverlayForStage(s *kargo.Stage) {
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

// renderLogs builds the overlay body for the active Logs tab (Promotions or
// Events). The tab strip itself is drawn by overlayView so it stays visible
// when the viewport scrolls.
func (m *Model) renderLogs() {
	keyStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	valStyle := lipgloss.NewStyle().Foreground(normal).Background(bg)

	bodyW := popupInnerWidth(m.width)

	var lines []string
	if m.overlayError != nil {
		lines = append(lines, lipgloss.NewStyle().Foreground(degraded).Background(bg).Render(wrap(m.overlayError.Error(), bodyW)))
	}

	switch m.overlayLogsTab {
	case logsTabEvents:
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
			lines = append(lines, style.Render(wrapIndent(fmt.Sprintf("  %s %s%s, %s  %s",
				etype, e.Reason, count, e.Source, whenString(e.Last)), bodyW, "    ")))
			if e.Message != "" {
				lines = append(lines, keyStyle.Render(wrapIndent("    "+e.Message, bodyW, "      ")))
			}
		}
	default:
		if len(m.overlayPromos) == 0 {
			lines = append(lines, keyStyle.Render("  (none)"))
		}
		for _, p := range m.overlayPromos {
			lines = append(lines, valStyle.Render(wrapIndent(fmt.Sprintf("  %s  %s  %s",
				promoCell(p.Phase), whenString(p.Created), p.Name), bodyW, "    ")))
			if p.Freight != "" {
				label := shortFreight(p.Freight)
				if f := m.freightByName(p.Freight); f != nil {
					label += aliasSuffix(f.Alias)
				}
				lines = append(lines, keyStyle.Render(wrapIndent("    freight: "+label, bodyW, "      ")))
			}
			if p.Message != "" {
				lines = append(lines, keyStyle.Render(wrapIndent("    "+p.Message, bodyW, "      ")))
			}
			for i, s := range p.Steps {
				marker := " "
				if int32(i) == p.CurrentStep && (p.Phase == "Running" || p.Phase == "Pending") {
					marker = "▶"
				}
				alias := s.Alias
				if alias == "" {
					alias = fmt.Sprintf("step-%d", i+1)
				}
				ts := stepWhen(s, p.Created)
				lines = append(lines, valStyle.Render(wrapIndent(fmt.Sprintf("    %s %2d. %s  %s  %s",
					marker, i+1, padRight(promoCell(s.Status), 9), alias, ts), bodyW, "         ")))
				if s.Message != "" {
					lines = append(lines, keyStyle.Render(wrapIndent("       "+s.Message, bodyW, "         ")))
				}
				if s.ErrorCount > 0 {
					lines = append(lines, keyStyle.Render(fmt.Sprintf("       errors: %d", s.ErrorCount)))
				}
			}
		}
	}
	m.overlayVP.SetContent(strings.Join(lines, "\n"))
}

// logsTabLabels is the ordered list of tab labels in the logs overlay.
// Order must match the logsTab iota values.
var logsTabLabels = []string{"Promotions", "Events"}

// logsTabScreenRow is the absolute screen Y of the tab strip inside the
// logs overlay box. The box is anchored at (0,0); its rendered layout is
// border-top(1) + padding-top(1) + header(1) + spacer(1), so the strip
// lands on row 4.
const logsTabScreenRow = 4

// logsTabScreenCol is the absolute screen X where the tab strip starts.
// Box layout: border-left(1) + padding-left(2), so the strip starts at
// col 3.
const logsTabScreenCol = 3

// logsTabHitTest maps a screen click to a tab index. Tab cells are
// rendered with Padding(0, 2), so each tab occupies len(label) + 4 cells
// laid out flush against each other starting at logsTabScreenCol.
func logsTabHitTest(x, y int) (logsTab, bool) {
	if y != logsTabScreenRow {
		return 0, false
	}
	col := logsTabScreenCol
	for i, label := range logsTabLabels {
		w := lipgloss.Width(label) + 4
		if x >= col && x < col+w {
			return logsTab(i), true
		}
		col += w
	}
	return 0, false
}

// renderLogsTabs draws the flat rectangular tab strip shown above the logs
// overlay body. Tabs sit flush against each other; the active tab uses a
// bright background to mark the selection.
func (m Model) renderLogsTabs() string {
	active := lipgloss.NewStyle().
		Foreground(darkFg).
		Background(normal).
		Bold(true).
		Padding(0, 2)
	inactive := lipgloss.NewStyle().
		Foreground(muted).
		Background(bg).
		Padding(0, 2)

	tabs := make([]string, len(logsTabLabels))
	for i, label := range logsTabLabels {
		if logsTab(i) == m.overlayLogsTab {
			tabs[i] = active.Render(label)
		} else {
			tabs[i] = inactive.Render(label)
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

// overlayView renders the active full-screen overlay (Logs or Diff).
func (m Model) overlayView() tea.View {
	titleStyle := lipgloss.NewStyle().Foreground(normal).Bold(true).Background(bg)
	hintStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)

	header := titleStyle.Render(m.overlayTitle)
	hint := hintStyle.Render("esc/enter dismiss · j/k scroll · home/end top/bottom")
	if m.overlay == overlayLogs {
		hint = hintStyle.Render("esc/enter dismiss · tab/[/] switch tab · j/k scroll · home/end top/bottom")
	}
	if m.overlayLoading {
		hint = hintStyle.Render("loading…  esc to cancel")
	}

	w := m.width
	if w <= 0 {
		w = 80
	}
	h := m.height
	if h < 9 {
		h = 9
	}
	// Box chrome: rounded border (2) + Padding(1, 2) → 6 cols, 4 rows.
	// Body chrome: header(1) + spacer(1) + spacer(1) + hint(1) → 4 rows.
	// Logs view adds a tab strip (1) + spacer(1) → 2 more rows.
	innerW := w - 6
	if innerW < 10 {
		innerW = 10
	}
	innerH := h - 4 - 4
	if m.overlay == overlayLogs {
		innerH -= 2
	}
	if innerH < 1 {
		innerH = 1
	}
	m.overlayVP.SetWidth(innerW)
	m.overlayVP.SetHeight(innerH)

	var body string
	if m.overlay == overlayLogs {
		body = lipgloss.JoinVertical(lipgloss.Left, header, "", m.renderLogsTabs(), "", m.overlayVP.View(), "", hint)
	} else {
		body = lipgloss.JoinVertical(lipgloss.Left, header, "", m.overlayVP.View(), "", hint)
	}
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
