package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"unknwon.dev/kargo-tui/internal/kargo"
)

// treeNode is one entry in the rendered tree. Layout rules:
//
//  1. Every control-flow stage renders as a top-level root. The chain of
//     control-flow stages is intentionally flattened — Kargo projects
//     often pipe control-flow stages serially (canary -> tier1 ->
//     tier1-01 -> ...), and nesting them deepens the indentation past
//     anything useful.
//  2. Non-control-flow stages attach as siblings under their nearest
//     control-flow ancestor, walking up Upstreams BFS-first with an
//     alphabetical tiebreak. This groups every tenant that descends from
//     a given control-flow stage together regardless of how they're
//     chained through intermediate non-CF stages.
//  3. Non-control-flow stages with no control-flow ancestor become roots
//     themselves so the tree still renders them.
type treeNode struct {
	Stage    *kargo.Stage
	Prefix   string // pre-built indent + branch glyphs (├─ / └─ / spaces)
	HasKids  bool
	Expanded bool
	IsMatch  bool // true when filter is active and this row's name matches
}

// rebuildTree produces the flat ordered list of visible nodes for the
// current stage set, honouring m.treeExpanded and the active filter
// query (m.filter.Value()). When the filter is non-empty, a node is
// rendered only if it matches OR any of its descendants matches; the
// matching rows themselves get IsMatch=true so the renderer can
// highlight them.
func (m *Model) rebuildTree() {
	stages := m.deploys
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	byName := make(map[string]*kargo.Stage, len(stages))
	for i := range stages {
		s := &stages[i]
		byName[s.Name] = s
	}

	parents := make(map[string][]string)
	for _, s := range stages {
		for _, up := range s.Upstreams {
			if _, ok := byName[up]; !ok {
				continue
			}
			parents[s.Name] = append(parents[s.Name], up)
		}
		sort.Strings(parents[s.Name])
	}

	// layer[name] = longest path from any in-data root. Used purely to
	// order the rendered control-flow roots (top to bottom) so the tree
	// reads in promotion direction — canary above tier1, tier1 above
	// tier1-01, etc. Tiebreaks are alphabetical.
	layer := make(map[string]int, len(stages))
	visiting := make(map[string]bool)
	var depth func(name string) int
	depth = func(name string) int {
		if d, ok := layer[name]; ok {
			return d
		}
		if visiting[name] {
			return 0
		}
		visiting[name] = true
		maxD := 0
		for _, p := range parents[name] {
			if d := depth(p) + 1; d > maxD {
				maxD = d
			}
		}
		visiting[name] = false
		layer[name] = maxD
		return maxD
	}
	for _, s := range stages {
		depth(s.Name)
	}

	// nearestCF walks upstreams BFS-first with alphabetical tiebreaks and
	// returns the first control-flow ancestor it finds (or "" when none
	// exists). Memoised so a fan-in shape doesn't redo the walk for every
	// downstream tenant.
	nearestCF := make(map[string]string, len(stages))
	nearestCFFor := func(start string) string {
		if v, ok := nearestCF[start]; ok {
			return v
		}
		seen := map[string]bool{start: true}
		queue := append([]string(nil), parents[start]...)
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if seen[cur] {
				continue
			}
			seen[cur] = true
			if s := byName[cur]; s != nil && s.IsControlFlow {
				nearestCF[start] = cur
				return cur
			}
			queue = append(queue, parents[cur]...)
		}
		nearestCF[start] = ""
		return ""
	}

	// primaryParent[child] = the control-flow stage child sits under.
	// Control-flow stages themselves are roots (primaryParent == "") so
	// the long CF chain renders flat instead of nesting. Non-CF stages
	// with no CF ancestor are also roots; they'd otherwise vanish.
	primaryParent := make(map[string]string)
	for _, s := range stages {
		if s.IsControlFlow {
			continue
		}
		if cf := nearestCFFor(s.Name); cf != "" {
			primaryParent[s.Name] = cf
		}
	}

	// roots = every control-flow stage plus every non-CF stage with no
	// CF ancestor. Ordered by layer (promotion direction), then name.
	var roots []string
	for _, s := range stages {
		if s.IsControlFlow || primaryParent[s.Name] == "" {
			roots = append(roots, s.Name)
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		li, lj := layer[roots[i]], layer[roots[j]]
		if li != lj {
			return li < lj
		}
		return roots[i] < roots[j]
	})

	// children[parent] = stages attached under parent in the rendered tree,
	// alphabetically ordered. Only primary edges populate this map, so the
	// recursive walk below produces a strict tree (every stage appears once).
	children := make(map[string][]string)
	for name, p := range primaryParent {
		children[p] = append(children[p], name)
	}
	for k := range children {
		sort.Strings(children[k])
	}

	if m.treeExpanded == nil {
		m.treeExpanded = make(map[string]bool)
	}
	// Default-expand any node we haven't seen before so first paint shows
	// the whole tree without forcing the user to fan it out manually.
	for name := range byName {
		if _, ok := m.treeExpanded[name]; !ok {
			m.treeExpanded[name] = true
		}
	}

	// Filter pass: compute which nodes match and which subtrees contain a
	// match. matches[name] = the node's name contains the query;
	// visible[name] = matches OR has a matching descendant.
	matches := make(map[string]bool, len(byName))
	visible := make(map[string]bool, len(byName))
	if q != "" {
		for name := range byName {
			if strings.Contains(strings.ToLower(name), q) {
				matches[name] = true
			}
		}
		// Memoised reverse walk: a node is visible if it matches itself
		// or any descendant via primaryParent does.
		var hasVisibleDesc func(name string) bool
		visited := make(map[string]bool)
		hasVisibleDesc = func(name string) bool {
			if v, ok := visible[name]; ok {
				return v
			}
			if visited[name] {
				return false
			}
			visited[name] = true
			v := matches[name]
			for _, c := range children[name] {
				if hasVisibleDesc(c) {
					v = true
				}
			}
			visible[name] = v
			return v
		}
		for name := range byName {
			hasVisibleDesc(name)
		}
	}

	// isVisible reports whether a node should appear given the active
	// filter (true for everything when no filter is set).
	isVisible := func(name string) bool {
		if q == "" {
			return true
		}
		return visible[name]
	}

	var out []treeNode
	var walk func(name string, prefix string, isLast, isRoot bool)
	walk = func(name string, prefix string, isLast, isRoot bool) {
		s := byName[name]
		if s == nil || !isVisible(name) {
			return
		}
		var branch, childPrefix string
		switch {
		case isRoot:
			branch = ""
			childPrefix = ""
		case isLast:
			branch = prefix + "└─ "
			childPrefix = prefix + "   "
		default:
			branch = prefix + "├─ "
			childPrefix = prefix + "│  "
		}

		// Only primary children render here. Non-primary upstreams used to
		// be shown as `↗ name` stubs so the cross-edge stayed
		// discoverable, but in projects with wide fan-in/fan-out (e.g. a
		// shared control-flow stage downstream of every tenant) the same
		// stub repeated under every sibling drowned out the actual flow.
		// The graph view is the authoritative full-DAG visualisation; the
		// tree shows the primary spine only.
		var primaryKids []string
		for _, c := range children[name] {
			if !isVisible(c) {
				continue
			}
			primaryKids = append(primaryKids, c)
		}
		hasKids := len(primaryKids) > 0
		expanded := m.treeExpanded[name]
		// While filtering, force-expand: collapsing would defeat the
		// point of "show me where this name lives in the tree."
		if q != "" {
			expanded = true
		}

		out = append(out, treeNode{
			Stage:    s,
			Prefix:   branch,
			HasKids:  hasKids,
			Expanded: expanded,
			IsMatch:  matches[name],
		})
		if !expanded {
			return
		}
		for i, c := range primaryKids {
			walk(c, childPrefix, i == len(primaryKids)-1, false)
		}
	}
	for i, r := range roots {
		if !isVisible(r) {
			continue
		}
		walk(r, "", i == len(roots)-1, true)
	}
	m.treeNodes = out
	if m.treeCursor >= len(out) {
		m.treeCursor = len(out) - 1
	}
	if m.treeCursor < 0 {
		m.treeCursor = 0
	}
}

// treeBodyHeight returns the row budget the tree renderer uses for the
// visible window. Mirrors the math in treeView so the scroll recompute in
// Update agrees with the renderer.
func (m Model) treeBodyHeight() int {
	bodyH := m.height - 4
	if bodyH < 5 {
		bodyH = 5
	}
	return bodyH
}

// renderTreeBody returns the visible window of the tree as a single string.
func (m Model) renderTreeBody(width, height int) string {
	mutedStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	dimStyle := mutedStyle.Italic(true)
	cursorStyle := lipgloss.NewStyle().Background(selected).Foreground(darkFg).Bold(true)
	rowStyle := lipgloss.NewStyle().Background(bg)

	if len(m.treeNodes) == 0 {
		return mutedStyle.Render("no stages — try the deploys view (d)")
	}

	start := clampListScroll(m.treeScroll, m.treeCursor, height, len(m.treeNodes))
	end := start + height
	if height <= 0 || end > len(m.treeNodes) {
		end = len(m.treeNodes)
	}

	var lines []string
	for i := start; i < end; i++ {
		n := m.treeNodes[i]
		// On the cursor row, render the [+]/[-] toggle without a baked-in
		// background so the outer cursorStyle selection color fills through
		// it. Root rows lead with this toggle (no tree-branch prefix), so a
		// bg-painted toggle was the one segment that left the highlight
		// looking absent on roots.
		toggleStyle := mutedStyle
		if i == m.treeCursor {
			toggleStyle = lipgloss.NewStyle().Foreground(darkFg)
		}
		var toggle string
		switch {
		case n.HasKids && n.Expanded:
			toggle = toggleStyle.Render("[-] ")
		case n.HasKids && !n.Expanded:
			toggle = toggleStyle.Render("[+] ")
		default:
			toggle = "    "
		}
		name := stageNameCell(n.Stage.Name, n.Stage.Health)
		if n.IsMatch {
			// Underline matches so users can scan to "where" the filter
			// hit even when ancestor rows are also rendered for context.
			name = lipgloss.NewStyle().Background(bg).Underline(true).Render(n.Stage.Name)
			switch n.Stage.Health {
			case "Healthy":
				name = lipgloss.NewStyle().Foreground(healthy).Background(bg).Bold(true).Underline(true).Render(n.Stage.Name)
			case "Unhealthy":
				name = lipgloss.NewStyle().Foreground(degraded).Background(bg).Bold(true).Underline(true).Render(n.Stage.Name)
			case "Progressing":
				name = lipgloss.NewStyle().Foreground(progressing).Background(bg).Bold(true).Underline(true).Render(n.Stage.Name)
			}
		}
		if n.Stage.IsControlFlow {
			name += " " + dimStyle.Render("(control)")
		}
		health := healthCell(n.Stage.Health)
		var freight string
		switch {
		case len(n.Stage.CurrentFreight) > 0:
			freight = shortFreight(n.Stage.CurrentFreight[0])
			if a := m.aliasOf(n.Stage.CurrentFreight[0]); a != "" {
				freight += mutedStyle.Render(" " + a)
			}
		case isFreightName(n.Stage.FreightSummary):
			freight = shortFreight(n.Stage.FreightSummary)
		default:
			freight = mutedStyle.Render("—")
		}
		var age string
		switch {
		case !n.Stage.LastPromoAt.IsZero():
			age = mutedStyle.Render(ageString(n.Stage.LastPromoAt) + " ago")
		case !n.Stage.Created.IsZero():
			age = mutedStyle.Render(ageString(n.Stage.Created) + " ago")
		default:
			age = mutedStyle.Render("—")
		}
		body := fmt.Sprintf("%s%s%s  %s  %s  %s",
			n.Prefix, toggle, name, health, freight, age)
		body = clipToWidth(body, width)
		if i == m.treeCursor {
			lines = append(lines, cursorStyle.Render(padToWidth(body, width)))
		} else {
			lines = append(lines, rowStyle.Render(body))
		}
	}
	if end < len(m.treeNodes) || start > 0 {
		lines = append(lines, mutedStyle.Render(fmt.Sprintf("— showing %d–%d of %d —", start+1, end, len(m.treeNodes))))
	}
	return strings.Join(lines, "\n")
}

// clipToWidth truncates a styled string at width visible columns.
func clipToWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// padToWidth right-pads a styled string with spaces so the visible width
// matches width — needed so the cursor highlight spans the whole row.
func padToWidth(s string, width int) string {
	w := ansi.StringWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// selectedTreeStage returns the stage under the tree cursor, or nil.
func (m Model) selectedTreeStage() *kargo.Stage {
	if m.treeCursor < 0 || m.treeCursor >= len(m.treeNodes) {
		return nil
	}
	return m.treeNodes[m.treeCursor].Stage
}

func (m *Model) moveTreeCursor(delta int) {
	if len(m.treeNodes) == 0 {
		return
	}
	m.treeCursor += delta
	if m.treeCursor < 0 {
		m.treeCursor = 0
	}
	if m.treeCursor >= len(m.treeNodes) {
		m.treeCursor = len(m.treeNodes) - 1
	}
}

// toggleTreeNode flips the expand/collapse state on the cursor row.
// No-op for leaves.
func (m *Model) toggleTreeNode() {
	if m.treeCursor < 0 || m.treeCursor >= len(m.treeNodes) {
		return
	}
	n := m.treeNodes[m.treeCursor]
	if !n.HasKids {
		return
	}
	m.treeExpanded[n.Stage.Name] = !m.treeExpanded[n.Stage.Name]
	m.rebuildTree()
}

// setTreeNodeExpansion forces the cursor node to expand or collapse. The
// right arrow uses it to expand. Collapsing goes through
// collapseTreeOneLevel instead so left always steps out by one level.
func (m *Model) setTreeNodeExpansion(expand bool) {
	if m.treeCursor < 0 || m.treeCursor >= len(m.treeNodes) {
		return
	}
	n := m.treeNodes[m.treeCursor]
	if !n.HasKids {
		return
	}
	m.treeExpanded[n.Stage.Name] = expand
	m.rebuildTree()
}

// treeNodeDepth returns the indentation depth of a rendered row. Roots have an
// empty prefix (depth 0). Each nested level adds one 3-cell indent unit
// ("├─ ", "└─ ", "│  ", or "   ") to the prefix.
func treeNodeDepth(n treeNode) int {
	return len([]rune(n.Prefix)) / 3
}

// collapseTreeOneLevel implements the left-arrow behavior: collapse the
// current node when it is an expanded parent, otherwise move the cursor up to
// the parent node and collapse that. This mirrors how file-tree views treat
// left: it always steps "out" by one level, whether by folding the current
// subtree or by climbing to the enclosing one.
func (m *Model) collapseTreeOneLevel() {
	if m.treeCursor < 0 || m.treeCursor >= len(m.treeNodes) {
		return
	}
	cur := m.treeNodes[m.treeCursor]
	if cur.HasKids && cur.Expanded {
		m.treeExpanded[cur.Stage.Name] = false
		name := cur.Stage.Name
		m.rebuildTree()
		m.focusTreeNode(name)
		return
	}
	// Already collapsed or a leaf: climb to the nearest shallower row above
	// the cursor (the parent) and collapse it.
	depth := treeNodeDepth(cur)
	for i := m.treeCursor - 1; i >= 0; i-- {
		if treeNodeDepth(m.treeNodes[i]) >= depth {
			continue
		}
		parent := m.treeNodes[i]
		m.treeExpanded[parent.Stage.Name] = false
		name := parent.Stage.Name
		m.rebuildTree()
		m.focusTreeNode(name)
		return
	}
}

// focusTreeNode moves the cursor onto the row whose stage matches name, or
// leaves it clamped in range when the node is no longer visible.
func (m *Model) focusTreeNode(name string) {
	for i, n := range m.treeNodes {
		if n.Stage.Name == name {
			m.treeCursor = i
			return
		}
	}
	if m.treeCursor >= len(m.treeNodes) {
		m.treeCursor = len(m.treeNodes) - 1
	}
	if m.treeCursor < 0 {
		m.treeCursor = 0
	}
}

// treeView renders the full tree-view frame.
func (m Model) treeView() tea.View {
	headerText := fmt.Sprintf("kargo-tui · tree · %s · project=%s · %d stages",
		m.contextName, m.project, len(m.deploys))
	if m.loading {
		headerText += " · refreshing…"
	}
	header := lipgloss.NewStyle().
		Foreground(normal).Background(bg).Bold(true).
		Padding(0, 1).
		Render(headerText)

	bodyH := m.treeBodyHeight()
	body := lipgloss.NewStyle().Background(bg).Padding(0, 1).Render(
		m.renderTreeBody(m.width-2, bodyH))

	hint := lipgloss.NewStyle().Foreground(muted).Background(bg).Padding(0, 1).
		Render("←/→ collapse/expand · v details · P promote · l logs · / filter · ? help")

	var statusLine string
	switch {
	case m.filtering || m.filter.Value() != "":
		statusLine = lipgloss.NewStyle().Background(bg).Render(m.filter.View())
	case m.authExpired:
		statusLine = m.renderAuthBanner()
	case m.deploysError != nil:
		statusLine = lipgloss.NewStyle().Foreground(degraded).Background(bg).Padding(0, 1).Render(m.deploysError.Error())
	case m.yankedMessage != "":
		statusLine = lipgloss.NewStyle().Foreground(healthy).Background(bg).Padding(0, 1).Render(m.yankedMessage)
	}
	var content string
	if statusLine != "" {
		content = lipgloss.JoinVertical(lipgloss.Left, header, body, statusLine, hint)
	} else {
		content = lipgloss.JoinVertical(lipgloss.Left, header, body, hint)
	}
	content = paintFrame(content, m.width, m.height)

	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = m.activeMouseMode()
	return v
}
