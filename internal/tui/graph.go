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

// graphLayout is a positioned, edge-routed view of the project's stage DAG
// produced by layoutGraph. Coordinates are cell-grid: x = layer (column),
// y = position within the layer (row). Renderers convert (x, y) into
// terminal cells using nodeW/nodeH/colGap/rowGap from the layout config.
type graphLayout struct {
	nodes []graphNode
	edges []graphEdge
	// byName indexes into nodes for cursor navigation.
	byName  map[string]int
	width   int // total cell width
	height  int // total cell height
	cfg     graphCfg
}

type graphNode struct {
	Stage *kargo.Stage
	Layer int // 0-based column
	Slot  int // 0-based row within layer
	X, Y  int // top-left cell of the node box
}

type graphEdge struct {
	From, To int // indices into nodes
}

type graphCfg struct {
	NodeW    int
	NodeH    int
	ColGap   int
	RowGap   int
	HMargin  int
	VMargin  int
}

func defaultGraphCfg() graphCfg {
	return graphCfg{
		NodeW:   12,
		NodeH:   3,
		ColGap:  6,
		RowGap:  1,
		HMargin: 1,
		VMargin: 0,
	}
}

// layoutGraph runs Sugiyama-style layered layout over the project stages
// and returns a positioned graph ready for rendering. The algorithm:
//
//  1. Layer assignment: each node's layer = longest path from any root.
//  2. Slot ordering: barycenter heuristic (median of parent slots) to
//     minimise edge crossings — one pass is good enough for the small
//     DAGs Kargo projects produce.
//  3. Cell coordinates: (layer * (NodeW+ColGap), slot * (NodeH+RowGap)).
func layoutGraph(stages []kargo.Stage, cfg graphCfg) graphLayout {
	if cfg.NodeW == 0 {
		cfg = defaultGraphCfg()
	}
	byName := make(map[string]*kargo.Stage, len(stages))
	for i := range stages {
		byName[stages[i].Name] = &stages[i]
	}

	// Build child + parent maps restricted to stages we actually have.
	children := make(map[string][]string)
	parents := make(map[string][]string)
	for _, s := range stages {
		for _, up := range s.Upstreams {
			if _, ok := byName[up]; !ok {
				continue
			}
			children[up] = append(children[up], s.Name)
			parents[s.Name] = append(parents[s.Name], up)
		}
	}

	// Layer = longest path from any root. Computed via memoised DFS.
	layer := make(map[string]int, len(stages))
	var depth func(name string) int
	visiting := make(map[string]bool)
	depth = func(name string) int {
		if d, ok := layer[name]; ok {
			return d
		}
		if visiting[name] {
			// Cycle defence — Kargo stages shouldn't form a cycle but
			// guard anyway to keep the renderer crash-free.
			return 0
		}
		visiting[name] = true
		max := 0
		for _, p := range parents[name] {
			if d := depth(p) + 1; d > max {
				max = d
			}
		}
		visiting[name] = false
		layer[name] = max
		return max
	}
	for _, s := range stages {
		depth(s.Name)
	}

	// Group nodes by layer.
	byLayer := make(map[int][]string)
	maxLayer := 0
	for name, l := range layer {
		byLayer[l] = append(byLayer[l], name)
		if l > maxLayer {
			maxLayer = l
		}
	}
	// Initial within-layer order: alphabetical for determinism.
	for l := range byLayer {
		sort.Strings(byLayer[l])
	}

	// Barycenter pass: for layers > 0, reorder by mean parent slot. We
	// need slot indexes from the previous layer to compute barycentres,
	// so build slot indexes layer-by-layer.
	slot := make(map[string]int)
	for i, n := range byLayer[0] {
		slot[n] = i
	}
	for l := 1; l <= maxLayer; l++ {
		layerNodes := byLayer[l]
		// score(node) = mean parent slot, fallback to 0 when no parents
		// fall in a previous layer.
		score := make(map[string]float64, len(layerNodes))
		for _, n := range layerNodes {
			ps := parents[n]
			if len(ps) == 0 {
				score[n] = 0
				continue
			}
			sum, count := 0.0, 0
			for _, p := range ps {
				if s, ok := slot[p]; ok {
					sum += float64(s)
					count++
				}
			}
			if count == 0 {
				score[n] = 0
			} else {
				score[n] = sum / float64(count)
			}
		}
		sort.SliceStable(layerNodes, func(i, j int) bool {
			if score[layerNodes[i]] != score[layerNodes[j]] {
				return score[layerNodes[i]] < score[layerNodes[j]]
			}
			return layerNodes[i] < layerNodes[j]
		})
		byLayer[l] = layerNodes
		for i, n := range layerNodes {
			slot[n] = i
		}
	}

	// Materialise nodes with cell coordinates.
	nodes := make([]graphNode, 0, len(stages))
	idxByName := make(map[string]int, len(stages))
	for l := 0; l <= maxLayer; l++ {
		for s, name := range byLayer[l] {
			st := byName[name]
			x := cfg.HMargin + l*(cfg.NodeW+cfg.ColGap)
			y := cfg.VMargin + s*(cfg.NodeH+cfg.RowGap)
			idxByName[name] = len(nodes)
			nodes = append(nodes, graphNode{
				Stage: st,
				Layer: l,
				Slot:  s,
				X:     x,
				Y:     y,
			})
		}
	}

	// Edges (parent → child).
	edges := make([]graphEdge, 0)
	for parent, kids := range children {
		fi, ok := idxByName[parent]
		if !ok {
			continue
		}
		for _, k := range kids {
			ti, ok := idxByName[k]
			if !ok {
				continue
			}
			edges = append(edges, graphEdge{From: fi, To: ti})
		}
	}

	// Total canvas size.
	width := cfg.HMargin
	if maxLayer >= 0 {
		width += (maxLayer+1)*cfg.NodeW + maxLayer*cfg.ColGap
	}
	maxSlot := 0
	for _, ns := range byLayer {
		if len(ns) > maxSlot {
			maxSlot = len(ns)
		}
	}
	height := cfg.VMargin
	if maxSlot > 0 {
		height += maxSlot*cfg.NodeH + (maxSlot-1)*cfg.RowGap
	}

	return graphLayout{
		nodes:  nodes,
		edges:  edges,
		byName: idxByName,
		width:  width,
		height: height,
		cfg:    cfg,
	}
}

// canvas is a 2D grid of styled runes used to compose the graph picture.
// Each cell carries its own style so the painter can layer node borders,
// edge glyphs and cursor highlights without re-rendering the whole frame.
type canvas struct {
	w, h  int
	cells [][]canvasCell
}

type canvasCell struct {
	r     rune
	style lipgloss.Style
}

func newCanvas(w, h int) *canvas {
	cells := make([][]canvasCell, h)
	for i := range cells {
		cells[i] = make([]canvasCell, w)
		for j := range cells[i] {
			cells[i][j] = canvasCell{r: ' '}
		}
	}
	return &canvas{w: w, h: h, cells: cells}
}

func (c *canvas) set(x, y int, r rune, style lipgloss.Style) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	c.cells[y][x] = canvasCell{r: r, style: style}
}

func (c *canvas) writeAt(x, y int, s string, style lipgloss.Style) {
	for _, r := range s {
		c.set(x, y, r, style)
		x++
	}
}

func (c *canvas) hLine(x1, x2, y int, style lipgloss.Style) {
	if x1 > x2 {
		x1, x2 = x2, x1
	}
	for x := x1; x <= x2; x++ {
		c.set(x, y, '─', style)
	}
}

func (c *canvas) vLine(x, y1, y2 int, style lipgloss.Style) {
	if y1 > y2 {
		y1, y2 = y2, y1
	}
	for y := y1; y <= y2; y++ {
		c.set(x, y, '│', style)
	}
}

// render flattens the canvas into a single string, rendering each cell
// with its own style. Lipgloss styles aren't comparable, so we can't
// coalesce same-style runs cheaply; the canvas is small enough that
// per-cell rendering is fine.
func (c *canvas) render() string {
	return c.renderRect(0, 0, c.w, c.h)
}

// renderRect renders only a sub-rectangle of the canvas. Used to pan a
// large graph layout into a smaller terminal viewport.
func (c *canvas) renderRect(x0, y0, w, h int) string {
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	x1 := x0 + w
	y1 := y0 + h
	if x1 > c.w {
		x1 = c.w
	}
	if y1 > c.h {
		y1 = c.h
	}
	var b strings.Builder
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			cell := c.cells[y][x]
			b.WriteString(cell.style.Render(string(cell.r)))
		}
		if y < y1-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// renderGraph paints g onto a fresh canvas, then crops a viewport
// (viewW × viewH) that keeps the cursor node visible.
func renderGraph(g graphLayout, cursorIdx, viewW, viewH int, m Model) string {
	cv := newCanvas(g.width, g.height)

	edgeStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	cursorEdgeStyle := lipgloss.NewStyle().Foreground(selected).Background(bg).Bold(true)
	defaultBorderStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	cursorBorderStyle := lipgloss.NewStyle().Foreground(selected).Background(bg).Bold(true)
	bgStyle := lipgloss.NewStyle().Background(bg)

	// Edges first so node boxes paint over them at the boundary.
	for _, e := range g.edges {
		from := g.nodes[e.From]
		to := g.nodes[e.To]
		style := edgeStyle
		if e.From == cursorIdx || e.To == cursorIdx {
			style = cursorEdgeStyle
		}
		// Path: out the right side of `from`, left to the gutter column,
		// vertical to to.Y midline, then right into the left side of `to`.
		startX := from.X + g.cfg.NodeW
		startY := from.Y + g.cfg.NodeH/2
		endX := to.X - 1
		endY := to.Y + g.cfg.NodeH/2
		gutter := from.X + g.cfg.NodeW + g.cfg.ColGap/2
		// Horizontal out of source.
		cv.hLine(startX, gutter, startY, style)
		// Vertical in the gutter (skipped if startY == endY).
		if startY != endY {
			cv.vLine(gutter, startY, endY, style)
			// Corner glyphs: top-left bend at the higher end, bottom-left
			// at the lower end. The "bend" is from the perspective of the
			// gutter column.
			if endY > startY {
				cv.set(gutter, startY, '┐', style)
				cv.set(gutter, endY, '└', style)
			} else {
				cv.set(gutter, startY, '┘', style)
				cv.set(gutter, endY, '┌', style)
			}
		}
		// Horizontal into target.
		cv.hLine(gutter, endX, endY, style)
		// Arrowhead at the target side.
		cv.set(endX, endY, '▶', style)
	}

	// Nodes.
	for i, n := range g.nodes {
		border := defaultBorderStyle
		if i == cursorIdx {
			border = cursorBorderStyle
		}
		drawNode(cv, n, g.cfg, border, bgStyle, m)
	}

	// Viewport pan: shift the visible window so the cursor node stays
	// fully on screen with a small margin. When the cursor is unset or
	// the layout fits, render from the origin.
	x0, y0 := 0, 0
	if viewW <= 0 {
		viewW = g.width
	}
	if viewH <= 0 {
		viewH = g.height
	}
	if cursorIdx >= 0 && cursorIdx < len(g.nodes) {
		n := g.nodes[cursorIdx]
		const margin = 2
		// Horizontal: keep the entire node box visible.
		nodeRight := n.X + g.cfg.NodeW
		if n.X-margin < x0 {
			x0 = n.X - margin
		}
		if nodeRight+margin > x0+viewW {
			x0 = nodeRight + margin - viewW
		}
		if x0 < 0 {
			x0 = 0
		}
		// Vertical.
		nodeBottom := n.Y + g.cfg.NodeH
		if n.Y-margin < y0 {
			y0 = n.Y - margin
		}
		if nodeBottom+margin > y0+viewH {
			y0 = nodeBottom + margin - viewH
		}
		if y0 < 0 {
			y0 = 0
		}
	}
	return cv.renderRect(x0, y0, viewW, viewH)
}

// drawNode paints a single node box with name + health + freight short.
// border carries the colour for the box edges; bgStyle clears the
// interior so node text doesn't pick up edge colours.
func drawNode(cv *canvas, n graphNode, cfg graphCfg, border, bgStyle lipgloss.Style, m Model) {
	x, y := n.X, n.Y
	w, h := cfg.NodeW, cfg.NodeH
	// Border corners + sides.
	cv.set(x, y, '┌', border)
	cv.set(x+w-1, y, '┐', border)
	cv.set(x, y+h-1, '└', border)
	cv.set(x+w-1, y+h-1, '┘', border)
	for i := x + 1; i < x+w-1; i++ {
		cv.set(i, y, '─', border)
		cv.set(i, y+h-1, '─', border)
	}
	for i := y + 1; i < y+h-1; i++ {
		cv.set(x, i, '│', border)
		cv.set(x+w-1, i, '│', border)
	}
	// Clear interior.
	for iy := y + 1; iy < y+h-1; iy++ {
		for ix := x + 1; ix < x+w-1; ix++ {
			cv.set(ix, iy, ' ', bgStyle)
		}
	}
	// Line 1: name (truncated).
	innerW := w - 2
	name := n.Stage.Name
	if ansi.StringWidth(name) > innerW {
		name = ansi.Truncate(name, innerW, "…")
	}
	nameStyle := bgStyle.Foreground(normal).Bold(true)
	switch n.Stage.Health {
	case "Healthy":
		nameStyle = nameStyle.Foreground(healthy)
	case "Unhealthy":
		nameStyle = nameStyle.Foreground(degraded)
	case "Progressing":
		nameStyle = nameStyle.Foreground(progressing)
	}
	cv.writeAt(x+1, y+1, padOrTrim(name, innerW), nameStyle)
	// Line 2: health glyph + age.
	if h >= 3 {
		glyph := healthGlyph(n.Stage.Health)
		var age string
		switch {
		case !n.Stage.LastPromoAt.IsZero():
			age = ageString(n.Stage.LastPromoAt)
		case !n.Stage.Created.IsZero():
			age = ageString(n.Stage.Created)
		default:
			age = "—"
		}
		summary := fmt.Sprintf("%s %s", glyph, age)
		summary = padOrTrim(summary, innerW)
		summaryStyle := bgStyle.Foreground(muted)
		cv.writeAt(x+1, y+2, summary, summaryStyle)
	}
}

func healthGlyph(h string) string {
	switch h {
	case "Healthy":
		return "✓"
	case "Unhealthy":
		return "✗"
	case "Progressing":
		return "⟳"
	default:
		return "·"
	}
}

func padOrTrim(s string, w int) string {
	cur := ansi.StringWidth(s)
	if cur == w {
		return s
	}
	if cur > w {
		return ansi.Truncate(s, w, "")
	}
	return s + strings.Repeat(" ", w-cur)
}

// graphView renders the full graph-view frame.
func (m Model) graphView() tea.View {
	headerText := fmt.Sprintf("kargo-tui · graph · %s · project=%s · %d stages",
		m.contextName, m.project, len(m.deploys))
	if m.loading {
		headerText += " · refreshing…"
	}
	header := lipgloss.NewStyle().
		Foreground(normal).Background(bg).Bold(true).
		Padding(0, 1).
		Render(headerText)

	g := m.graphLayout
	cursorIdx := -1
	if m.graphCursor >= 0 && m.graphCursor < len(g.nodes) {
		cursorIdx = m.graphCursor
	}

	// Carve out the body area from the available terminal size. Three
	// trim lines: header, status, hint. One more if there's an error or
	// yank message; reserve room for it conservatively so the layout
	// doesn't jitter when the message appears.
	bodyW := m.width - 2 // account for padding
	if bodyW < 20 {
		bodyW = 20
	}
	bodyH := m.height - 5
	if bodyH < 5 {
		bodyH = 5
	}
	body := lipgloss.NewStyle().Background(bg).Padding(0, 1).
		Render(renderGraph(g, cursorIdx, bodyW, bodyH, m))

	statusLine := graphStatusLine(g, cursorIdx)
	hint := lipgloss.NewStyle().Foreground(muted).Background(bg).Padding(0, 1).
		Render("←/→/↑/↓ move along edges · enter logs · P promote · t tree · d deploys · ? help · q quit")

	var content string
	if m.deploysError != nil {
		errLine := lipgloss.NewStyle().Foreground(degraded).Background(bg).Padding(0, 1).Render(m.deploysError.Error())
		content = lipgloss.JoinVertical(lipgloss.Left, header, body, errLine, statusLine, hint)
	} else if m.yankedMessage != "" {
		yankLine := lipgloss.NewStyle().Foreground(healthy).Background(bg).Padding(0, 1).Render(m.yankedMessage)
		content = lipgloss.JoinVertical(lipgloss.Left, header, body, yankLine, statusLine, hint)
	} else {
		content = lipgloss.JoinVertical(lipgloss.Left, header, body, statusLine, hint)
	}

	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// graphStatusLine renders the "selected: X · N incoming · M outgoing"
// summary with hints about which spatial keys land where.
func graphStatusLine(g graphLayout, cursorIdx int) string {
	muted := lipgloss.NewStyle().Foreground(muted).Background(bg).Padding(0, 1)
	if cursorIdx < 0 || cursorIdx >= len(g.nodes) {
		return muted.Render("no selection")
	}
	n := g.nodes[cursorIdx]
	in, out := graphNeighbors(g, cursorIdx)
	parts := []string{"selected: " + n.Stage.Name}
	if n.Stage.Health != "" {
		parts = append(parts, n.Stage.Health)
	}
	parts = append(parts, fmt.Sprintf("%d in / %d out", len(in), len(out)))
	if right, ok := pickNeighbor(g, out, "right"); ok {
		parts = append(parts, "→ "+g.nodes[right].Stage.Name)
	}
	if left, ok := pickNeighbor(g, in, "left"); ok {
		parts = append(parts, "← "+g.nodes[left].Stage.Name)
	}
	return muted.Render(strings.Join(parts, " · "))
}

// graphNeighbors returns the indices of incoming and outgoing nodes for
// the node at idx.
func graphNeighbors(g graphLayout, idx int) (in, out []int) {
	for _, e := range g.edges {
		switch {
		case e.To == idx:
			in = append(in, e.From)
		case e.From == idx:
			out = append(out, e.To)
		}
	}
	return in, out
}

// pickNeighbor picks a representative neighbour for the given direction
// hint. "left" / "right" pick the one closest in slot to the current
// cursor; falls back to the first.
func pickNeighbor(g graphLayout, candidates []int, _ string) (int, bool) {
	if len(candidates) == 0 {
		return 0, false
	}
	return candidates[0], true
}

// rebuildGraph recomputes the layout from the current m.deploys. Called
// from refreshRows so the graph stays in sync with the rest of the UI.
func (m *Model) rebuildGraph() {
	m.graphLayout = layoutGraph(m.deploys, defaultGraphCfg())
	if m.graphCursor >= len(m.graphLayout.nodes) {
		m.graphCursor = len(m.graphLayout.nodes) - 1
	}
	if m.graphCursor < 0 {
		m.graphCursor = 0
	}
}

// selectedGraphStage returns the stage under the graph cursor, or nil.
func (m Model) selectedGraphStage() *kargo.Stage {
	if m.graphCursor < 0 || m.graphCursor >= len(m.graphLayout.nodes) {
		return nil
	}
	return m.graphLayout.nodes[m.graphCursor].Stage
}

// moveGraphCursor advances the graph cursor in a spatial direction. left
// / right step to the closest neighbour by edge; up / down step within
// the same layer to the previous / next slot.
func (m *Model) moveGraphCursor(dir string) {
	g := m.graphLayout
	if m.graphCursor < 0 || m.graphCursor >= len(g.nodes) {
		return
	}
	cur := g.nodes[m.graphCursor]
	switch dir {
	case "right":
		// Pick outgoing edge whose target is closest in slot.
		_, out := graphNeighbors(g, m.graphCursor)
		if len(out) == 0 {
			return
		}
		best := out[0]
		bestDist := abs(g.nodes[best].Slot - cur.Slot)
		for _, n := range out[1:] {
			d := abs(g.nodes[n].Slot - cur.Slot)
			if d < bestDist {
				best = n
				bestDist = d
			}
		}
		m.graphCursor = best
	case "left":
		in, _ := graphNeighbors(g, m.graphCursor)
		if len(in) == 0 {
			return
		}
		best := in[0]
		bestDist := abs(g.nodes[best].Slot - cur.Slot)
		for _, n := range in[1:] {
			d := abs(g.nodes[n].Slot - cur.Slot)
			if d < bestDist {
				best = n
				bestDist = d
			}
		}
		m.graphCursor = best
	case "up", "down":
		// Walk siblings within the same layer.
		var sibs []int
		for i, n := range g.nodes {
			if n.Layer == cur.Layer {
				sibs = append(sibs, i)
			}
		}
		sort.Slice(sibs, func(i, j int) bool {
			return g.nodes[sibs[i]].Slot < g.nodes[sibs[j]].Slot
		})
		curPos := -1
		for i, idx := range sibs {
			if idx == m.graphCursor {
				curPos = i
				break
			}
		}
		if curPos < 0 {
			return
		}
		next := curPos
		if dir == "up" && curPos > 0 {
			next = curPos - 1
		}
		if dir == "down" && curPos < len(sibs)-1 {
			next = curPos + 1
		}
		m.graphCursor = sibs[next]
	}
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}
