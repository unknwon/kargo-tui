package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"unknwon.dev/kargo-tui/internal/kargo"
)

// treeNode is one entry in the rendered tree. Each stage appears exactly
// once, attached to its primary parent (BFS-first, with a flow-correctness
// lift that re-anchors joins as siblings of the fan-out block they merge).
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

	children := make(map[string][]string)
	hasParent := make(map[string]bool)
	for _, s := range stages {
		for _, up := range s.Upstreams {
			if _, ok := byName[up]; !ok {
				continue
			}
			children[up] = append(children[up], s.Name)
			hasParent[s.Name] = true
		}
	}
	for k := range children {
		sort.Strings(children[k])
	}
	var roots []string
	for _, s := range stages {
		if !hasParent[s.Name] {
			roots = append(roots, s.Name)
		}
	}
	sort.Strings(roots)

	// parents[child] = every upstream stage we have data for. Used both
	// for primaryParent selection and the "is this parent really a sibling
	// in disguise?" check below.
	parents := make(map[string][]string)
	parentSet := make(map[string]map[string]bool)
	for _, s := range stages {
		for _, up := range s.Upstreams {
			if _, ok := byName[up]; !ok {
				continue
			}
			parents[s.Name] = append(parents[s.Name], up)
			if parentSet[s.Name] == nil {
				parentSet[s.Name] = make(map[string]bool)
			}
			parentSet[s.Name][up] = true
		}
	}

	// primaryParent[child] = the parent under which this stage owns its
	// real subtree. Resolved breadth-first from roots so the alphabetically
	// first reachable parent wins.
	primaryParent := make(map[string]string)
	visited := make(map[string]bool)
	queue := append([]string(nil), roots...)
	for _, r := range roots {
		visited[r] = true
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range children[cur] {
			if _, ok := primaryParent[c]; !ok {
				primaryParent[c] = cur
			}
			if !visited[c] {
				visited[c] = true
				queue = append(queue, c)
			}
		}
	}

	// Flow-correctness lift: a stage S with multiple upstream parents is a
	// join. In graph view, a join sits in its own column downstream of
	// the fan-out it merges. The tree analogue: find the deepest single
	// tree-node X whose primary subtree contains every one of S's real
	// upstreams. That node is the "block" S merges. Attach S as a
	// sibling of X (i.e. as a child of X's primary parent) so it renders
	// after the whole fan-out instead of inside it.
	ancestorChain := func(name string) []string {
		var chain []string
		for cur := name; cur != ""; cur = primaryParent[cur] {
			chain = append(chain, cur)
		}
		return chain
	}
	for _, s := range stages {
		ps := parents[s.Name]
		if len(ps) < 2 {
			continue
		}
		original := primaryParent[s.Name]
		// Compute the deepest ancestor common to every parent's chain.
		// depthOf is taken from the first parent's chain; identical
		// ancestor positions yield identical depths across parents because
		// primaryParent forms a tree.
		common := make(map[string]int)
		depthOf := make(map[string]int)
		firstChain := ancestorChain(ps[0])
		for i, a := range firstChain {
			common[a] = 1
			depthOf[a] = len(firstChain) - i
		}
		for _, p := range ps[1:] {
			seen := make(map[string]bool)
			for _, a := range ancestorChain(p) {
				if seen[a] {
					continue
				}
				seen[a] = true
				if _, ok := common[a]; ok {
					common[a]++
				}
			}
		}
		lca, lcaDepth := "", -1
		for a, c := range common {
			if c == len(ps) && depthOf[a] > lcaDepth {
				lca = a
				lcaDepth = depthOf[a]
			}
		}
		if lca == "" {
			continue
		}
		// X = deepest node whose subtree contains all of S's parents.
		// When every parent shares X as their primary parent (the typical
		// fan-out join), X is the fan-out source itself. Lift S one step
		// further so it renders alongside X. When parents already share
		// the LCA as one of themselves (e.g. one parent IS the ancestor
		// of the others), keep the BFS pick — that handles the
		// "parent + descendant of that parent" shape elsewhere.
		if parentSet[s.Name][lca] {
			continue
		}
		newPrimary := primaryParent[lca]
		if newPrimary == "" || newPrimary == original {
			continue
		}
		primaryParent[s.Name] = newPrimary
		if !slices.Contains(children[newPrimary], s.Name) {
			children[newPrimary] = append(children[newPrimary], s.Name)
			sort.Strings(children[newPrimary])
		}
	}

	// Prune children[] to only entries that render under each parent (i.e.
	// primaryParent[c] == parent). The filter visibility walk below and
	// the render walk both consume children[] recursively. Non-primary
	// edges left over from Upstreams would otherwise inflate the visible
	// subtree of unrelated ancestors when a downstream join happens to
	// match the filter.
	for name, kids := range children {
		filtered := kids[:0]
		for _, c := range kids {
			if primaryParent[c] == name {
				filtered = append(filtered, c)
			}
		}
		children[name] = filtered
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
			if primaryParent[c] == name {
				primaryKids = append(primaryKids, c)
			}
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
		var toggle string
		switch {
		case n.HasKids && n.Expanded:
			toggle = mutedStyle.Render("[-] ")
		case n.HasKids && !n.Expanded:
			toggle = mutedStyle.Render("[+] ")
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

// setTreeNodeExpansion forces expand=true (`+`) or false (`-`).
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
		Render("+/- expand/collapse · v details · P promote · l logs · / filter · ? help")

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
