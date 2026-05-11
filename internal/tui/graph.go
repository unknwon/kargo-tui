package tui

import (
	"fmt"
	"image/color"
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
	byName map[string]int
	width  int // total cell width
	height int // total cell height
	cfg    graphCfg
}

type graphNode struct {
	Stage *kargo.Stage
	Layer int // 0-based column
	Slot  int // 0-based row within layer
	X, Y  int // top-left cell of the node box
	W, H  int // box dimensions (varies per node so each fits its rows)
	Rows  []nodeRow
	Dummy bool
	// LongEdge identifies which original edge a dummy node belongs to,
	// so the segment renderer can colour them consistently when the
	// cursor highlights an edge.
	LongEdge int
}

// nodeRow is one "key: value" line inside a stage box. ValueColor of
// nil renders the value in normal/muted; a non-nil colour overrides
// (used for state cells like Health/LastPromo so they keep their
// semantic colour from the deploy list).
type nodeRow struct {
	Key        string
	Value      string
	ValueColor color.Color // nil → normal foreground
	ValueBold  bool
}

type graphEdge struct {
	From, To int // indices into nodes
	// Original is the index into the *user-visible* edge that this
	// segment belongs to. For non-dummy single-layer edges it equals the
	// segment's own index; for dummy chain segments, every segment in the
	// chain shares the originating edge's index.
	Original int
}

type graphCfg struct {
	NodeW   int
	NodeH   int
	ColGap  int
	RowGap  int
	HMargin int
	VMargin int
}

func defaultGraphCfg() graphCfg {
	return graphCfg{
		NodeW:   28, // wide enough for "Argo: Healthy/Synced" lines
		NodeH:   0,  // unused — box height now derives from row count
		ColGap:  6,
		RowGap:  1,
		HMargin: 1,
		VMargin: 0,
	}
}

// buildNodeRows produces the key/value lines a stage box should show,
// mirroring the deploy list's columns. Rows with no value are omitted
// (e.g. a stage with no Argo apps drops the Argo/Sync rows entirely)
// so each box hugs only the data the stage actually has.
func buildNodeRows(s *kargo.Stage, m Model) []nodeRow {
	var rows []nodeRow
	if s.Health != "" {
		rows = append(rows, nodeRow{
			Key: "Health", Value: s.Health,
			ValueColor: stageHealthColor(s.Health), ValueBold: true,
		})
	}
	if s.IsControlFlow {
		rows = append(rows, nodeRow{Key: "Argo", Value: "control-flow", ValueColor: progressing})
	} else if len(s.ArgoCDApps) > 0 {
		ah, as := worstArgo(s.ArgoCDApps)
		if ah != "" {
			rows = append(rows, nodeRow{Key: "Argo", Value: ah, ValueColor: argoHealthColorVal(ah)})
		}
		if as != "" {
			rows = append(rows, nodeRow{Key: "Sync", Value: as, ValueColor: argoSyncColorVal(as)})
		}
	}
	if s.LastPromo != "" {
		rows = append(rows, nodeRow{Key: "Promo", Value: s.LastPromo, ValueColor: promoColorVal(s.LastPromo)})
	}
	var freightSHA, freightAlias string
	switch {
	case len(s.CurrentFreight) > 0:
		freightSHA = shortFreight(s.CurrentFreight[0])
		freightAlias = m.aliasOf(s.CurrentFreight[0])
	case isFreightName(s.FreightSummary):
		freightSHA = shortFreight(s.FreightSummary)
	case s.FreightSummary != "":
		freightSHA = s.FreightSummary
	}
	if freightSHA != "" {
		rows = append(rows, nodeRow{Key: "Freight", Value: freightSHA})
		if freightAlias != "" {
			// Continuation row: empty key so the alias sits flush under
			// the SHA, indented by the key column the renderer reserves.
			rows = append(rows, nodeRow{Key: "", Value: freightAlias, ValueColor: muted})
		}
	}
	var age string
	switch {
	case !s.LastPromoAt.IsZero():
		age = ageString(s.LastPromoAt) + " ago"
	case !s.Created.IsZero():
		age = ageString(s.Created) + " ago"
	}
	if age != "" {
		rows = append(rows, nodeRow{Key: "Age", Value: age, ValueColor: muted})
	}
	if s.Shard != "" {
		rows = append(rows, nodeRow{Key: "Shard", Value: s.Shard, ValueColor: muted})
	}
	return rows
}

func stageHealthColor(h string) color.Color {
	switch h {
	case "Healthy":
		return healthy
	case "Unhealthy":
		return degraded
	case "Progressing":
		return progressing
	default:
		return muted
	}
}

func argoHealthColorVal(h string) color.Color {
	switch h {
	case "Healthy":
		return healthy
	case "Progressing", "Suspended":
		return progressing
	case "Degraded", "Missing":
		return degraded
	default:
		return muted
	}
}

func argoSyncColorVal(s string) color.Color {
	switch s {
	case "Synced":
		return healthy
	case "OutOfSync":
		return degraded
	default:
		return muted
	}
}

func promoColorVal(p string) color.Color {
	switch p {
	case "Succeeded":
		return healthy
	case "Failed", "Errored", "Aborted":
		return degraded
	case "Running", "Pending":
		return progressing
	default:
		return muted
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
func layoutGraph(stages []kargo.Stage, cfg graphCfg, m Model) graphLayout {
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

	// Long-edge handling: split any (parent, child) edge spanning more
	// than one layer into a chain of single-layer segments by inserting
	// dummy node names into intermediate layers. Dummies carry no Stage
	// and aren't selectable; they exist only so the renderer routes the
	// chain through dedicated gutter slots in each layer instead of
	// drawing across other nodes' columns.
	type segment struct{ from, to string }
	var segments []segment
	dummyNames := make(map[string]bool)
	dummyOrigEdge := make(map[string]int) // dummy → original edge index (for cursor highlight)
	dummyLayer := make(map[string]int)
	origEdges := make([]struct{ from, to string }, 0)
	for parent, kids := range children {
		for _, k := range kids {
			origEdgeIdx := len(origEdges)
			origEdges = append(origEdges, struct{ from, to string }{parent, k})
			lp, lc := layer[parent], layer[k]
			if lc-lp <= 1 {
				segments = append(segments, segment{parent, k})
				continue
			}
			prev := parent
			for l := lp + 1; l < lc; l++ {
				name := fmt.Sprintf("__dummy:%s→%s@%d", parent, k, l)
				dummyNames[name] = true
				dummyOrigEdge[name] = origEdgeIdx
				dummyLayer[name] = l
				segments = append(segments, segment{prev, name})
				prev = name
			}
			segments = append(segments, segment{prev, k})
		}
	}
	// origEdgeOf returns the original edge index for a single-layer
	// segment (for non-dummy real edges, equal to the per-edge index;
	// for chain segments, equal to the chain's source edge).
	origEdgeOf := func(seg segment) int {
		if i, ok := dummyOrigEdge[seg.from]; ok {
			return i
		}
		if i, ok := dummyOrigEdge[seg.to]; ok {
			return i
		}
		// Real single-layer edge — find it by linear scan; cheap (small N).
		for i, e := range origEdges {
			if e.from == seg.from && e.to == seg.to {
				return i
			}
		}
		return -1
	}

	// Group node names (real + dummy) by layer.
	byLayer := make(map[int][]string)
	maxLayer := 0
	for name, l := range layer {
		byLayer[l] = append(byLayer[l], name)
		if l > maxLayer {
			maxLayer = l
		}
	}
	for name, l := range dummyLayer {
		byLayer[l] = append(byLayer[l], name)
		if l > maxLayer {
			maxLayer = l
		}
	}
	// Initial within-layer order: alphabetical for determinism (dummies
	// sort by their generated name, which keeps siblings of the same
	// origin edge clustered).
	for l := range byLayer {
		sort.Strings(byLayer[l])
	}

	// Build a segment-aware parents map (real + dummy). Used only for
	// the barycenter ordering pass below.
	segParents := make(map[string][]string)
	for _, s := range segments {
		segParents[s.to] = append(segParents[s.to], s.from)
	}

	// Barycenter pass: for layers > 0, reorder by mean parent slot using
	// the segment graph so dummies pull toward their real source.
	slot := make(map[string]int)
	for i, n := range byLayer[0] {
		slot[n] = i
	}
	for l := 1; l <= maxLayer; l++ {
		layerNodes := byLayer[l]
		score := make(map[string]float64, len(layerNodes))
		for _, n := range layerNodes {
			ps := segParents[n]
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

	// Per-node row data + height. Dummies are 1 row tall (the routing
	// line passes through their middle). Real nodes size to fit the
	// rows buildNodeRows produces.
	rowsForNode := make(map[string][]nodeRow, len(stages))
	heightFor := func(name string) int {
		if dummyNames[name] {
			return 1
		}
		return 2 + len(rowsForNode[name]) // top border + rows + bottom border
	}
	for _, s := range stages {
		stage := byName[s.Name]
		rowsForNode[s.Name] = buildNodeRows(stage, m)
	}

	// Per-slot row height = max box height across all layers for that
	// slot. Keeps boxes in the same row visually aligned.
	maxSlot := 0
	for _, ns := range byLayer {
		if len(ns) > maxSlot {
			maxSlot = len(ns)
		}
	}
	slotH := make([]int, maxSlot)
	for _, layerNodes := range byLayer {
		for slotIdx, name := range layerNodes {
			h := heightFor(name)
			if h > slotH[slotIdx] {
				slotH[slotIdx] = h
			}
		}
	}
	// Y offset per slot (cumulative height + RowGap between slots).
	slotY := make([]int, maxSlot)
	cum := cfg.VMargin
	for i := 0; i < maxSlot; i++ {
		slotY[i] = cum
		cum += slotH[i] + cfg.RowGap
	}

	// Per-slot centerline = midpoint of the slot's row band. Boxes get
	// placed so their own vertical centre (Y + H/2, integer division)
	// lands exactly on this line. That keeps every box's centre aligned
	// across columns even when box heights differ by an odd number of
	// rows, which is what allows the connector arrows to enter and exit
	// at a consistent row.
	slotCenter := make([]int, maxSlot)
	for i := 0; i < maxSlot; i++ {
		slotCenter[i] = slotY[i] + slotH[i]/2
	}

	// Per-layer pixel height = sum of slot heights this layer actually
	// occupies + gaps between them. Used to center shorter columns
	// against the tallest column so the graph reads as middle-aligned
	// instead of top-aligned.
	layerPixelH := make([]int, maxLayer+1)
	maxLayerH := 0
	for l := 0; l <= maxLayer; l++ {
		n := len(byLayer[l])
		if n == 0 {
			continue
		}
		h := 0
		for s := 0; s < n; s++ {
			h += slotH[s]
		}
		h += (n - 1) * cfg.RowGap
		layerPixelH[l] = h
		if h > maxLayerH {
			maxLayerH = h
		}
	}

	// Materialise nodes. Box width is uniform (cfg.NodeW); box height is
	// per-node so each box hugs its content. Y is chosen so the box's
	// own midline aligns with the slot's centerline, then offset by the
	// layer's centering shift.
	nodes := make([]graphNode, 0, len(stages)+len(dummyNames))
	idxByName := make(map[string]int, len(stages)+len(dummyNames))
	for l := 0; l <= maxLayer; l++ {
		yOffset := (maxLayerH - layerPixelH[l]) / 2
		for s, name := range byLayer[l] {
			h := heightFor(name)
			x := cfg.HMargin + l*(cfg.NodeW+cfg.ColGap)
			y := slotCenter[s] - h/2 + yOffset
			idxByName[name] = len(nodes)
			node := graphNode{
				Layer: l,
				Slot:  s,
				X:     x,
				Y:     y,
				W:     cfg.NodeW,
				H:     h,
			}
			if dummyNames[name] {
				node.Dummy = true
				node.LongEdge = dummyOrigEdge[name]
			} else {
				node.Stage = byName[name]
				node.Rows = rowsForNode[name]
			}
			nodes = append(nodes, node)
		}
	}

	// Edges = single-layer segments, each tagged with its originating
	// edge index so the cursor highlight follows the whole chain.
	edges := make([]graphEdge, 0, len(segments))
	for _, s := range segments {
		fi, ok1 := idxByName[s.from]
		ti, ok2 := idxByName[s.to]
		if !ok1 || !ok2 {
			continue
		}
		edges = append(edges, graphEdge{From: fi, To: ti, Original: origEdgeOf(s)})
	}

	// Total canvas size. Height already accounted for via slotY/cum.
	width := cfg.HMargin
	if maxLayer >= 0 {
		width += (maxLayer+1)*cfg.NodeW + maxLayer*cfg.ColGap
	}
	height := cum
	if maxSlot == 0 {
		height = cfg.VMargin
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

// renderGraph paints g onto a fresh canvas, then crops a viewW × viewH
// viewport anchored at (panX, panY). The caller (graphView via
// recomputeGraphPan) is responsible for choosing a pan that keeps the
// cursor node on screen; cursorIdx is used only for cursor highlighting.
func renderGraph(g graphLayout, cursorIdx, viewW, viewH, panX, panY int) string {
	cv := newCanvas(g.width, g.height)

	edgeStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	cursorEdgeStyle := lipgloss.NewStyle().Foreground(selected).Background(bg).Bold(true)
	cursorBorderStyle := lipgloss.NewStyle().Foreground(selected).Background(bg).Bold(true)
	bgStyle := lipgloss.NewStyle().Background(bg)

	// Determine which original edge the cursor highlights (if any). An
	// edge is highlighted when either of its real endpoints is the
	// cursor; for chained long edges, every segment with a matching
	// Original index lights up too.
	highlightedOrig := -1
	if cursorIdx >= 0 && cursorIdx < len(g.nodes) {
		for _, e := range g.edges {
			if e.From == cursorIdx || e.To == cursorIdx {
				highlightedOrig = e.Original
				break
			}
		}
	}

	// Paint each segment. Real edges and dummy chain segments share the
	// same routing math: out the source's right edge, vertical in a
	// per-segment gutter, into the target's left edge.
	for _, e := range g.edges {
		from := g.nodes[e.From]
		to := g.nodes[e.To]
		style := edgeStyle
		if e.Original == highlightedOrig && highlightedOrig != -1 {
			style = cursorEdgeStyle
		}
		startX := from.X + from.W
		startY := from.Y + from.H/2
		endX := to.X - 1
		endY := to.Y + to.H/2
		// When the source is a dummy, "right side" is the centre of the
		// dummy's reserved slot — there's no box edge to paint outside
		// of, so start one cell into the gutter.
		if from.Dummy {
			startX = from.X
		}
		if to.Dummy {
			endX = to.X + to.W - 1
		}
		gutter := from.X + from.W + g.cfg.ColGap/2
		cv.hLine(startX, gutter, startY, style)
		if startY != endY {
			cv.vLine(gutter, startY, endY, style)
			if endY > startY {
				cv.set(gutter, startY, '┐', style)
				cv.set(gutter, endY, '└', style)
			} else {
				cv.set(gutter, startY, '┘', style)
				cv.set(gutter, endY, '┌', style)
			}
		}
		cv.hLine(gutter, endX, endY, style)
		// Arrowhead only when the target is a real node (terminal segment
		// of any chain). Mid-chain dummies extend the line plainly.
		if !to.Dummy {
			cv.set(endX, endY, '▶', style)
		}
	}

	// Nodes (skip dummies — they're invisible routing slots). Border
	// colour = worst-of-state so the picture reads as a heatmap; the
	// cursor swaps to selected-blue + heavy double-line so selection
	// wins against any state colour.
	for i, n := range g.nodes {
		if n.Dummy {
			continue
		}
		_, stateColor := worstState(n.Stage)
		borderColor := stateColor
		if i == cursorIdx {
			borderColor = selected
		}
		border := lipgloss.NewStyle().Foreground(borderColor).Background(bg)
		if i == cursorIdx {
			border = cursorBorderStyle
		}
		drawNode(cv, n, border, bgStyle, i == cursorIdx, borderColor)
	}

	if viewW <= 0 {
		viewW = g.width
	}
	if viewH <= 0 {
		viewH = g.height
	}
	return cv.renderRect(panX, panY, viewW, viewH)
}

// graphPanOffsetFor returns the top-left canvas coordinate the renderer
// pans to. It keeps the cursor node fully on screen with a small margin
// but otherwise preserves the incoming prevX/prevY so the viewport
// doesn't shift when the cursor moves between already-visible nodes.
// Shared by the renderer and the mouse hit-tester so a click on a visible
// node resolves back to its node index.
func graphPanOffsetFor(g graphLayout, cursorIdx, viewW, viewH, prevX, prevY int) (int, int) {
	if viewW <= 0 {
		viewW = g.width
	}
	if viewH <= 0 {
		viewH = g.height
	}
	maxX := g.width - viewW
	if maxX < 0 {
		maxX = 0
	}
	maxY := g.height - viewH
	if maxY < 0 {
		maxY = 0
	}
	clamp := func(x0, y0 int) (int, int) {
		if x0 < 0 {
			x0 = 0
		} else if x0 > maxX {
			x0 = maxX
		}
		if y0 < 0 {
			y0 = 0
		} else if y0 > maxY {
			y0 = maxY
		}
		return x0, y0
	}
	x0, y0 := prevX, prevY
	if cursorIdx < 0 || cursorIdx >= len(g.nodes) {
		return clamp(x0, y0)
	}
	n := g.nodes[cursorIdx]
	const margin = 2
	nodeRight := n.X + n.W
	if n.X-margin < x0 {
		x0 = n.X - margin
	}
	if nodeRight+margin > x0+viewW {
		x0 = nodeRight + margin - viewW
	}
	maxLayer := 0
	for _, gn := range g.nodes {
		if gn.Layer > maxLayer {
			maxLayer = gn.Layer
		}
	}
	// When the cursor isn't in the right-most layer, try to also keep
	// the next layer's column on screen so the user can see where "→"
	// will land. Only applies when the viewport is wide enough to fit
	// both columns, otherwise the cursor-visible pan above wins.
	if n.Layer < maxLayer {
		nextRight := n.X + n.W + g.cfg.ColGap + g.cfg.NodeW
		if nextRight+margin > x0+viewW {
			shifted := nextRight + margin - viewW
			if shifted <= n.X-margin {
				x0 = shifted
			}
		}
	}
	nodeBottom := n.Y + n.H
	if n.Y-margin < y0 {
		y0 = n.Y - margin
	}
	if nodeBottom+margin > y0+viewH {
		y0 = nodeBottom + margin - viewH
	}
	// Clamp to the canvas so the viewport never extends past the edge.
	// Without this, a cursor on the rightmost / bottommost node leaves
	// x0+viewW > g.width (margin pushes past the edge); renderRect then
	// crops, producing a body that's a few cells narrower / shorter than
	// requested.
	return clamp(x0, y0)
}

// drawNode paints a single node box. The first inner row is the stage
// name (bold, in the border colour); each subsequent row is one
// "key: value" pair from buildNodeRows. Box height is set per-node
// during layout so each box hugs its content.
func drawNode(cv *canvas, n graphNode, border, bgStyle lipgloss.Style, cursor bool, borderColor color.Color) {
	x, y := n.X, n.Y
	w, h := n.W, n.H

	// Border — heavy double-line on the cursor so selection always wins.
	tl, tr, bl, br, hor, ver := '┌', '┐', '└', '┘', '─', '│'
	if cursor {
		tl, tr, bl, br, hor, ver = '╔', '╗', '╚', '╝', '═', '║'
	}
	cv.set(x, y, tl, border)
	cv.set(x+w-1, y, tr, border)
	cv.set(x, y+h-1, bl, border)
	cv.set(x+w-1, y+h-1, br, border)
	for i := x + 1; i < x+w-1; i++ {
		cv.set(i, y, hor, border)
		cv.set(i, y+h-1, hor, border)
	}
	for i := y + 1; i < y+h-1; i++ {
		cv.set(x, i, ver, border)
		cv.set(x+w-1, i, ver, border)
	}
	// Clear interior.
	for iy := y + 1; iy < y+h-1; iy++ {
		for ix := x + 1; ix < x+w-1; ix++ {
			cv.set(ix, iy, ' ', bgStyle)
		}
	}

	innerW := w - 2
	if innerW < 1 || h < 3 {
		return
	}

	// Row 0: stage name in bold, painted in the border colour so the
	// outside-and-inside read as one unit ("the red box belongs to qa").
	rowY := y + 1
	nameStyle := bgStyle.Foreground(borderColor).Bold(true)
	cv.writeAt(x+1, rowY, fitToWidth(n.Stage.Name, innerW), nameStyle)
	rowY++

	// Subsequent rows: the key/value pairs from buildNodeRows. Key in
	// muted, value in its semantic colour (or normal foreground when
	// the row didn't pick one).
	keyStyle := bgStyle.Foreground(muted)
	for _, r := range n.Rows {
		if rowY >= y+h-1 {
			break
		}
		// Render "key  value" with the key padded to a small fixed
		// width so values line up. Truncate the value if the combined
		// line would overrun innerW.
		keyW := keyColumnWidth(n.Rows)
		if keyW > innerW-2 {
			keyW = innerW - 2
		}
		key := fitToWidth(r.Key, keyW)
		valW := innerW - keyW - 1 // 1 cell separator
		if valW < 1 {
			valW = 1
		}
		val := fitToWidth(r.Value, valW)
		cv.writeAt(x+1, rowY, key, keyStyle)
		cv.set(x+1+keyW, rowY, ' ', bgStyle)
		valColor := r.ValueColor
		if valColor == nil {
			valColor = normal
		}
		valStyle := bgStyle.Foreground(valColor)
		if r.ValueBold {
			valStyle = valStyle.Bold(true)
		}
		cv.writeAt(x+1+keyW+1, rowY, val, valStyle)
		rowY++
	}
}

// keyColumnWidth returns the width to reserve for the key column —
// width of the longest key + 1 for the colon-style separator we
// effectively render via spaces.
func keyColumnWidth(rows []nodeRow) int {
	w := 0
	for _, r := range rows {
		if n := ansi.StringWidth(r.Key); n > w {
			w = n
		}
	}
	return w
}

// worstState picks the most urgent state to surface as the node's
// border colour. Argo OutOfSync is intentionally NOT escalated —
// out-of-sync is the normal mid-promotion state and shouldn't paint
// the box red; the Sync row inside still surfaces it. Severity, most
// urgent first:
//
//  1. Promo failed/aborted     → "Promo failed"   (red)
//  2. Stage health unhealthy   → "Unhealthy"      (red)
//  3. Argo degraded/missing    → "Argo degraded"  (red)
//  4. Promo running/pending    → "Promoting…"     (yellow)
//  5. Stage progressing        → "Progressing"    (yellow)
//  6. Argo progressing/sus.    → "Argo syncing"   (yellow)
//  7. Control-flow stage       → "Control-flow"   (yellow)
//  8. Healthy                  → "Healthy"        (green)
//  9. Anything else / unknown  → "—"              (muted)
func worstState(s *kargo.Stage) (string, color.Color) {
	if s == nil {
		return "—", muted
	}
	switch s.LastPromo {
	case "Failed", "Errored", "Aborted":
		return "Promo failed", degraded
	}
	if s.Health == "Unhealthy" {
		return "Unhealthy", degraded
	}
	ah, _ := worstArgo(s.ArgoCDApps)
	switch ah {
	case "Degraded", "Missing":
		return "Argo " + lower(ah), degraded
	}
	switch s.LastPromo {
	case "Running", "Pending":
		return "Promoting…", progressing
	}
	if s.Health == "Progressing" {
		return "Progressing", progressing
	}
	if ah == "Progressing" || ah == "Suspended" {
		return "Argo syncing", progressing
	}
	if s.IsControlFlow {
		return "Control-flow", progressing
	}
	if s.Health == "Healthy" {
		return "Healthy", healthy
	}
	return "—", muted
}

func lower(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'A' && b[0] <= 'Z' {
		b[0] += 'a' - 'A'
	}
	return string(b)
}

// worstArgo collapses a stage's per-app Argo CD state into a single
// (health, sync) pair, mirroring stageArgoCell's severity ordering.
func worstArgo(apps []kargo.ArgoCDAppRef) (health, sync string) {
	if len(apps) == 0 {
		return "", ""
	}
	health, sync = "Healthy", "Synced"
	for _, a := range apps {
		switch a.Health {
		case "Degraded":
			health = "Degraded"
		case "Missing":
			if health != "Degraded" {
				health = "Missing"
			}
		case "Suspended":
			if health == "Healthy" || health == "Progressing" || health == "Unknown" {
				health = "Suspended"
			}
		case "Progressing":
			if health == "Healthy" || health == "Unknown" {
				health = "Progressing"
			}
		case "Unknown", "":
			if health == "Healthy" {
				health = "Unknown"
			}
		}
		switch a.Sync {
		case "OutOfSync":
			sync = "OutOfSync"
		case "Unknown", "":
			if sync == "Synced" {
				sync = "Unknown"
			}
		}
	}
	return health, sync
}

// fitToWidth returns s sized to exactly w visible cells: truncated with
// an ellipsis when too long, padded with spaces when too short. Unlike
// padOrTrim it always uses an ellipsis on truncation so callers know
// the row was clipped.
func fitToWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	cur := ansi.StringWidth(s)
	if cur == w {
		return s
	}
	if cur > w {
		t := ansi.Truncate(s, w, "…")
		// Truncate may return slightly under w when the ellipsis pushes
		// the boundary; re-pad to guarantee exact width.
		if tw := ansi.StringWidth(t); tw < w {
			t += strings.Repeat(" ", w-tw)
		}
		return t
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

	statusLine := graphStatusLine(g, cursorIdx)

	// Search line: while filtering, show the live textinput so the user
	// can see what they're typing (list views get this for free via the
	// global filter line; graph view has to render its own). After enter
	// commits, keep a slim counter line up so n/N has a visible anchor.
	// When no search is in progress this slot is empty.
	var searchLine string
	switch {
	case m.filtering && m.view == viewGraph:
		input := lipgloss.NewStyle().Background(bg).Render(m.filter.View())
		var meta string
		switch {
		case len(m.graphSearchMatches) > 0:
			meta = lipgloss.NewStyle().Foreground(selected).Background(bg).Bold(true).
				Render(fmt.Sprintf("  match %d of %d · enter commit · esc cancel",
					m.graphSearchPos+1, len(m.graphSearchMatches)))
		case strings.TrimSpace(m.filter.Value()) != "":
			meta = lipgloss.NewStyle().Foreground(degraded).Background(bg).
				Render("  no matches · esc cancel")
		}
		searchLine = lipgloss.NewStyle().Background(bg).Padding(0, 1).
			Render(lipgloss.JoinHorizontal(lipgloss.Top, input, meta))
	case len(m.graphSearchMatches) > 0 && strings.TrimSpace(m.filter.Value()) != "":
		searchLine = lipgloss.NewStyle().Foreground(selected).Background(bg).
			Padding(0, 1).Bold(true).
			Render(fmt.Sprintf("search: %q · match %d of %d · n/N step · esc clear",
				m.filter.Value(), m.graphSearchPos+1, len(m.graphSearchMatches)))
	}

	hint := lipgloss.NewStyle().Foreground(muted).Background(bg).Padding(0, 1).
		Render("v details · P promote · l logs · / search · n/N next/prev · ? help")

	// Body sizing: header + statusLine + hint = 3 lines guaranteed.
	// Banner / error / yank message and the search line each cost one
	// more row when present. Compute the actual reservation so the body
	// shrinks to fit instead of overflowing the terminal.
	reserved := 3
	if m.authExpired || m.deploysError != nil || m.yankedMessage != "" {
		reserved++
	}
	if searchLine != "" {
		reserved++
	}
	bodyW := m.width - 2
	if bodyW < 20 {
		bodyW = 20
	}
	bodyH := m.height - reserved
	if bodyH < 5 {
		bodyH = 5
	}
	body := lipgloss.NewStyle().Background(bg).Padding(0, 1).
		Render(renderGraph(g, cursorIdx, bodyW, bodyH, m.graphPanX, m.graphPanY))

	// Compose the frame. Slot order: header, body, [banner|error|yank],
	// [searchLine], statusLine, hint. searchLine is only emitted when it
	// has content so we don't reserve dead vertical space.
	parts := []string{header, body}
	switch {
	case m.authExpired:
		parts = append(parts, m.renderAuthBanner())
	case m.deploysError != nil:
		errLine := lipgloss.NewStyle().Foreground(degraded).Background(bg).Padding(0, 1).Render(m.deploysError.Error())
		parts = append(parts, errLine)
	case m.yankedMessage != "":
		yankLine := lipgloss.NewStyle().Foreground(healthy).Background(bg).Padding(0, 1).Render(m.yankedMessage)
		parts = append(parts, yankLine)
	}
	searchLineRow := -1
	if searchLine != "" {
		searchLineRow = lipgloss.Height(lipgloss.JoinVertical(lipgloss.Left, parts...))
		parts = append(parts, searchLine)
	}
	parts = append(parts, statusLine, hint)
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)
	content = m.composeWithMenu(content)
	content = paintFrame(content, m.width, m.height)

	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = m.activeMouseMode()
	if m.filtering && m.view == viewGraph && searchLineRow >= 0 {
		if c := m.filter.Cursor(); c != nil {
			// searchLine is wrapped in Padding(0, 1) — shift the input's
			// intrinsic (x, 0) cursor right by one cell to land on the
			// rendered text inside the padded line.
			c.X += 1
			c.Y += searchLineRow
			v.Cursor = c
		}
	}
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
	// Dummies have no Stage pointer; rebuildGraph normally fixes a stale
	// cursor before we render, but a refresh that races with View has
	// been observed to land here mid-flight. Bail out cleanly instead of
	// dereferencing nil.
	if n.Dummy || n.Stage == nil {
		return muted.Render("no selection")
	}
	in, out := graphNeighbors(g, cursorIdx)
	parts := []string{"selected: " + n.Stage.Name}
	if n.Stage.Health != "" {
		parts = append(parts, n.Stage.Health)
	}
	parts = append(parts, fmt.Sprintf("%d in / %d out", len(in), len(out)))
	if left, ok := pickNeighbor(in, "left"); ok {
		if ln := g.nodes[left]; !ln.Dummy && ln.Stage != nil {
			parts = append(parts, "← "+ln.Stage.Name)
		}
	}
	if right, ok := pickNeighbor(out, "right"); ok {
		if rn := g.nodes[right]; !rn.Dummy && rn.Stage != nil {
			parts = append(parts, "→ "+rn.Stage.Name)
		}
	}
	return muted.Render(strings.Join(parts, " · "))
}

// graphNeighbors returns the indices of real (non-dummy) incoming and
// outgoing nodes for the node at idx, walking through any dummy chain
// nodes that sit on multi-layer edges. The cursor never lands on a
// dummy, so navigation always jumps to the real node at the far end.
func graphNeighbors(g graphLayout, idx int) (in, out []int) {
	// Outgoing: follow each successor; if it's a dummy, recurse to
	// its successor until we hit a real node.
	var followForward func(start int) int
	followForward = func(start int) int {
		if !g.nodes[start].Dummy {
			return start
		}
		for _, e := range g.edges {
			if e.From == start {
				return followForward(e.To)
			}
		}
		return -1
	}
	var followBackward func(start int) int
	followBackward = func(start int) int {
		if !g.nodes[start].Dummy {
			return start
		}
		for _, e := range g.edges {
			if e.To == start {
				return followBackward(e.From)
			}
		}
		return -1
	}
	for _, e := range g.edges {
		switch {
		case e.To == idx:
			if r := followBackward(e.From); r >= 0 {
				in = append(in, r)
			}
		case e.From == idx:
			if r := followForward(e.To); r >= 0 {
				out = append(out, r)
			}
		}
	}
	return in, out
}

// pickNeighbor picks a representative neighbour for the given direction
// hint. "left" / "right" pick the one closest in slot to the current
// cursor; falls back to the first.
func pickNeighbor(candidates []int, _ string) (int, bool) {
	if len(candidates) == 0 {
		return 0, false
	}
	return candidates[0], true
}

// rebuildGraph recomputes the layout from the current m.deploys. Called
// from refreshRows so the graph stays in sync with the rest of the UI.
// Ensures the cursor lands on a real (non-dummy) node afterwards, and
// re-resolves any active name search since stage indices in the new
// layout do not necessarily match the old ones.
func (m *Model) rebuildGraph() {
	// Snapshot the name of the currently-selected search match against
	// the OLD layout before we overwrite it. Indices don't survive a
	// rebuild — capturing prevName after the layout swap would look up
	// an unrelated stage and restore the cursor to the wrong match.
	prevMatchName := ""
	if m.filter.Value() != "" && m.view == viewGraph &&
		m.graphSearchPos >= 0 && m.graphSearchPos < len(m.graphSearchMatches) {
		idx := m.graphSearchMatches[m.graphSearchPos]
		if idx >= 0 && idx < len(m.graphLayout.nodes) {
			if n := m.graphLayout.nodes[idx]; !n.Dummy && n.Stage != nil {
				prevMatchName = n.Stage.Name
			}
		}
	}
	m.graphLayout = layoutGraph(m.deploys, defaultGraphCfg(), *m)
	defer func() {
		// Rehydrate the saved search against the fresh layout. The
		// non-search cursor-fixup further down is search-agnostic, so
		// we move the cursor here to track the same match by name
		// across the rebuild — otherwise the "match X of Y" counter
		// would lie about where the cursor actually is.
		if q := m.filter.Value(); q != "" && m.view == viewGraph {
			qLower := strings.ToLower(strings.TrimSpace(q))
			var matches []int
			restored := -1
			for i, n := range m.graphLayout.nodes {
				if n.Dummy || n.Stage == nil {
					continue
				}
				if strings.Contains(strings.ToLower(n.Stage.Name), qLower) {
					if prevMatchName != "" && n.Stage.Name == prevMatchName {
						restored = len(matches)
					}
					matches = append(matches, i)
				}
			}
			m.graphSearchMatches = matches
			switch {
			case len(matches) == 0:
				m.graphSearchPos = 0
			case restored >= 0:
				m.graphSearchPos = restored
				m.graphCursor = matches[restored]
			default:
				m.graphSearchPos = 0
				m.graphCursor = matches[0]
			}
		}
	}()
	if len(m.graphLayout.nodes) == 0 {
		m.graphCursor = 0
		return
	}
	if m.graphCursor >= len(m.graphLayout.nodes) {
		m.graphCursor = len(m.graphLayout.nodes) - 1
	}
	if m.graphCursor < 0 {
		m.graphCursor = 0
	}
	if m.graphLayout.nodes[m.graphCursor].Dummy {
		// Walk forward until we find a real node — happens when the
		// previous cursor index now points at a freshly-inserted dummy
		// after a layout change.
		for i, n := range m.graphLayout.nodes {
			if !n.Dummy {
				m.graphCursor = i
				return
			}
		}
	}
}

// recomputeGraphPan updates m.graphPanX/Y so the cursor node stays
// visible, otherwise preserving the existing offset. Called once at the
// tail of Update so every key/mouse/resize/data-load message re-derives
// the pan without per-handler instrumentation.
func (m *Model) recomputeGraphPan() {
	g := m.graphLayout
	if len(g.nodes) == 0 {
		m.graphPanX, m.graphPanY = 0, 0
		return
	}
	cursorIdx := -1
	if m.graphCursor >= 0 && m.graphCursor < len(g.nodes) {
		cursorIdx = m.graphCursor
	}
	bodyW, bodyH := m.graphBodyDims()
	m.graphPanX, m.graphPanY = graphPanOffsetFor(g, cursorIdx, bodyW, bodyH, m.graphPanX, m.graphPanY)
}

// selectedGraphStage returns the stage under the graph cursor, or nil
// for dummy waypoints / out-of-range cursors.
func (m Model) selectedGraphStage() *kargo.Stage {
	if m.graphCursor < 0 || m.graphCursor >= len(m.graphLayout.nodes) {
		return nil
	}
	n := m.graphLayout.nodes[m.graphCursor]
	if n.Dummy {
		return nil
	}
	return n.Stage
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
		// Walk siblings within the same layer; skip dummies since they're
		// not selectable.
		var sibs []int
		for i, n := range g.nodes {
			if n.Layer == cur.Layer && !n.Dummy {
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

// beginGraphSearch snapshots the current cursor so esc can restore it
// and clears any leftover match state. Called on `/` press in graph view.
func (m *Model) beginGraphSearch() {
	m.graphSearchSaved = m.graphCursor
	m.graphSearchMatches = nil
	m.graphSearchPos = 0
	m.graphSearchActive = true
}

// recomputeGraphMatches walks the layout's real nodes (dummies excluded)
// looking for stage names containing q (case-insensitive), in cursor
// order — layer-major then slot. The cursor jumps to the first match;
// the full list is remembered so n/N can cycle after the search is
// committed. An empty query clears matches without moving the cursor.
func (m *Model) recomputeGraphMatches(q string) {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		m.graphSearchMatches = nil
		m.graphSearchPos = 0
		return
	}
	var matches []int
	for i, n := range m.graphLayout.nodes {
		if n.Dummy || n.Stage == nil {
			continue
		}
		if strings.Contains(strings.ToLower(n.Stage.Name), q) {
			matches = append(matches, i)
		}
	}
	m.graphSearchMatches = matches
	if len(matches) == 0 {
		return
	}
	m.graphSearchPos = 0
	m.graphCursor = matches[0]
	m.refreshPanel()
}

// stepGraphMatch advances through the saved match list by delta (+1 / -1)
// with wrap-around. Used by n/N after the search is committed. No-op
// when there are no matches yet (e.g. the user pressed n outside of an
// active search session).
func (m *Model) stepGraphMatch(delta int) {
	if len(m.graphSearchMatches) == 0 {
		return
	}
	pos := m.graphSearchPos + delta
	for pos < 0 {
		pos += len(m.graphSearchMatches)
	}
	pos %= len(m.graphSearchMatches)
	m.graphSearchPos = pos
	m.graphCursor = m.graphSearchMatches[pos]
	m.refreshPanel()
}

// cancelGraphSearch restores the pre-search cursor and drops any saved
// match list. Called on esc while a search is in progress (filter still
// focused). The textinput clear is left to the caller because the
// regular filter-mode esc path already handles it.
func (m *Model) cancelGraphSearch() {
	if !m.graphSearchActive {
		return
	}
	m.graphSearchActive = false
	m.graphSearchMatches = nil
	m.graphSearchPos = 0
	if m.graphSearchSaved >= 0 && m.graphSearchSaved < len(m.graphLayout.nodes) {
		m.graphCursor = m.graphSearchSaved
		m.refreshPanel()
	}
}
