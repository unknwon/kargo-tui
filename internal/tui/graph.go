package tui

import (
	"fmt"
	"image/color"
	"math"
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
		ColGap:  18, // room for staggered fan-in/fan-out buses without overlap
		RowGap:  2,  // vertical breathing room so edges separate
		HMargin: 1,
		VMargin: 0,
	}
}

// buildNodeRows produces the key/value lines a stage box should show.
//
// Compact mode (m.graphExpanded false, the default) mirrors the Kargo web
// UI card: just the freight SHA, its alias, and an age, all rendered
// without key labels so the values sit flush-left. State (health, sync,
// promo) is conveyed by the box border colour via worstState, and the
// selected stage's full detail lives in the side panel.
//
// Expanded mode adds the deploy list's columns back: Health, Argo, Sync,
// Promo, Shard. Rows with no value are omitted (e.g. a stage with no Argo
// apps drops the Argo/Sync rows entirely) so each box hugs only the data
// the stage actually has.
func buildNodeRows(s *kargo.Stage, m Model) []nodeRow {
	var rows []nodeRow
	if m.graphExpanded {
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
	// In compact mode the freight value has no key label so it sits flush
	// under the stage name like the web card. In expanded mode it keeps
	// the "Freight" key to line up with the rows above it.
	freightKey := ""
	if m.graphExpanded {
		freightKey = "Freight"
	}
	if freightSHA != "" {
		rows = append(rows, nodeRow{Key: freightKey, Value: freightSHA})
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
		ageKey := ""
		if m.graphExpanded {
			ageKey = "Age"
		}
		rows = append(rows, nodeRow{Key: ageKey, Value: age, ValueColor: muted})
	}
	if m.graphExpanded && s.Shard != "" {
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
		// title bar + one pad row + rows + bottom border. The pad row gives
		// the same breathing space under the header in every mode so compact
		// and expanded boxes read consistently.
		return 3 + len(rowsForNode[name])
	}
	for _, s := range stages {
		stage := byName[s.Name]
		rowsForNode[s.Name] = buildNodeRows(stage, m)
	}

	// Grow NodeW to fit the widest line across all real nodes so values
	// like full freight aliases (e.g. "fallacious-stingray-...") render
	// without an ellipsis. Box layout stays uniform: every column uses
	// the same width, derived from the widest content in the graph.
	//
	// Required inner width per row = keyColumnWidth(rows) + 1 (separator)
	// + value width. Required box width = inner + 2 (borders). The stage
	// name (rendered on row 0) only needs name + 2.
	required := cfg.NodeW
	for _, s := range stages {
		rows := rowsForNode[s.Name]
		keyW := keyColumnWidth(rows)
		for _, r := range rows {
			if w := keyW + 1 + ansi.StringWidth(r.Value) + 2; w > required {
				required = w
			}
		}
		if nameW := ansi.StringWidth(s.Name) + 2; nameW > required {
			required = nameW
		}
	}
	cfg.NodeW = required

	// Vertical coordinate assignment. Instead of snapping every node to a
	// shared per-slot centerline (which forces edges to jog whenever two
	// connected nodes sit at different slot indices), give each node its
	// own Y centre and run median-alignment sweeps: pull each node toward
	// the median centre of its neighbours, then push apart any boxes that
	// would overlap inside a column. Single-parent / single-child chains
	// straighten to a flat line, matching the Kargo web UI, while fan-outs
	// still bend only where they must.
	segChildren := make(map[string][]string)
	for _, s := range segments {
		segChildren[s.from] = append(segChildren[s.from], s.to)
	}

	// Initial centres: stack each layer's nodes top-to-bottom by height with
	// RowGap between them, so the starting layout has no overlaps.
	center := make(map[string]float64)
	for l := 0; l <= maxLayer; l++ {
		y := float64(cfg.VMargin)
		for _, name := range byLayer[l] {
			h := float64(heightFor(name))
			center[name] = y + h/2
			y += h + float64(cfg.RowGap)
		}
	}

	// median returns the median of a sorted-able slice of centres. For an
	// even count it averages the two middle values, which keeps a node fed
	// by two neighbours centred between them.
	median := func(vals []float64) float64 {
		if len(vals) == 0 {
			return 0
		}
		sort.Float64s(vals)
		n := len(vals)
		if n%2 == 1 {
			return vals[n/2]
		}
		return (vals[n/2-1] + vals[n/2]) / 2
	}

	// minGapBetween is the minimum centre-to-centre distance two slot
	// neighbours need to not overlap: half of each box plus the row gap.
	minGapBetween := func(a, b string) float64 {
		return float64(heightFor(a))/2 + float64(heightFor(b))/2 + float64(cfg.RowGap)
	}

	// resolveOverlaps separates a layer's boxes without dragging the whole
	// column to the top. It first pushes overlapping pairs DOWN in slot
	// order, then re-centres the resulting block on the mean of the desired
	// centres, so a fan-in node placed at its parents' median stays near the
	// middle instead of being yanked up against the margin.
	resolveOverlaps := func(layerNodes []string) {
		if len(layerNodes) == 0 {
			return
		}
		desiredSum := 0.0
		for _, n := range layerNodes {
			desiredSum += center[n]
		}
		// Push down so each box clears the previous one.
		for i := 1; i < len(layerNodes); i++ {
			prev, cur := layerNodes[i-1], layerNodes[i]
			gap := minGapBetween(prev, cur)
			if center[cur]-center[prev] < gap {
				center[cur] = center[prev] + gap
			}
		}
		// The push-down only ever moves boxes down, shifting the block's
		// centroid below where the nodes wanted to be. Shift the whole layer
		// back up by that drift so it re-centres on the desired mean.
		newSum := 0.0
		for _, n := range layerNodes {
			newSum += center[n]
		}
		drift := (newSum - desiredSum) / float64(len(layerNodes))
		for _, n := range layerNodes {
			center[n] -= drift
		}
	}

	// Alignment sweeps. Forward passes align children to parents; backward
	// passes align parents to children. A handful of iterations converges
	// for the small DAGs Kargo produces.
	const alignSweeps = 6
	for sweep := 0; sweep < alignSweeps; sweep++ {
		// Forward: layers left to right, pull toward parents' median.
		for l := 1; l <= maxLayer; l++ {
			for _, name := range byLayer[l] {
				ps := segParents[name]
				if len(ps) == 0 {
					continue
				}
				vals := make([]float64, 0, len(ps))
				for _, p := range ps {
					vals = append(vals, center[p])
				}
				center[name] = median(vals)
			}
			resolveOverlaps(byLayer[l])
		}
		// Backward: layers right to left, pull toward children's median.
		for l := maxLayer - 1; l >= 0; l-- {
			for _, name := range byLayer[l] {
				cs := segChildren[name]
				if len(cs) == 0 {
					continue
				}
				vals := make([]float64, 0, len(cs))
				for _, c := range cs {
					vals = append(vals, center[c])
				}
				center[name] = median(vals)
			}
			resolveOverlaps(byLayer[l])
		}
	}

	// Compaction. The sweeps can leave large vertical gaps: a deep fan-out
	// pulls its chain's root far down to centre on the subtree, while a
	// short sibling chain stays near the top, so independent components
	// drift apart and leave dead rows between them. Compact that slack by
	// pulling boxes up toward their predecessor, but move whole "straight
	// runs" together so a horizontal spine doesn't get bent.
	//
	// A straight run is the set of nodes joined by edges whose endpoints
	// share a Y (centre). Union them so compaction shifts the entire run as
	// one rigid body.
	parent := make(map[string]string, len(center))
	find := func(a string) string {
		for parent[a] != a {
			parent[a] = parent[parent[a]]
			a = parent[a]
		}
		return a
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for name := range center {
		parent[name] = name
	}
	for _, s := range segments {
		if center[s.from] == center[s.to] {
			union(s.from, s.to)
		}
	}

	// Gravity compaction: a few passes, each pulling every layer's boxes up
	// to just clear the box above. A box can only rise as far as the highest
	// box in its straight run permits, so we compute the slack per run and
	// move the whole run by the minimum slack. Repeated passes let a run
	// settle once the runs it depends on have moved.
	const compactPasses = 8
	for pass := 0; pass < compactPasses; pass++ {
		// runSlack[root] = how far this straight run may rise (min over its
		// members of the gap above each member within its column).
		runSlack := make(map[string]float64)
		hasSlack := make(map[string]bool)
		for l := 0; l <= maxLayer; l++ {
			ln := byLayer[l]
			for i, name := range ln {
				var ceiling float64
				if i == 0 {
					ceiling = float64(cfg.VMargin) + float64(heightFor(name))/2
				} else {
					ceiling = center[ln[i-1]] + minGapBetween(ln[i-1], name)
				}
				slack := center[name] - ceiling
				root := find(name)
				if !hasSlack[root] || slack < runSlack[root] {
					runSlack[root] = slack
					hasSlack[root] = true
				}
			}
		}
		moved := false
		for root, slack := range runSlack {
			if slack <= 0 {
				continue
			}
			for name := range center {
				if find(name) == root {
					center[name] -= slack
				}
			}
			moved = true
		}
		if !moved {
			break
		}
	}

	// Straighten pass: after compaction, pull every node toward the centre of
	// its neighbours one more time, but ONLY accept the move when the new
	// centre still clears its slot neighbours (so we never reintroduce an
	// overlap). This recovers straight horizontal runs that compaction's
	// upward packing knocked off-axis.
	//
	// A node aligns to whichever side forms its "spine": a single parent or a
	// single child is a straight link the eye expects to stay flat, so it wins
	// over a fan on the other side. When the chosen side has a single
	// neighbour the node snaps exactly onto it (a dead-straight run); a fan
	// side uses the median. This keeps e.g. a single-parent node that fans out
	// to three children aligned with its parent instead of drifting to the
	// children's median and bending the incoming edge.
	straighten := func() {
		for l := 0; l <= maxLayer; l++ {
			ln := byLayer[l]
			for i, name := range ln {
				ps, cs := segParents[name], segChildren[name]
				var side []string
				switch {
				case len(ps) == 0 && len(cs) == 0:
					continue
				case len(ps) == 1:
					side = ps // straight link from the single parent wins
				case len(cs) == 1:
					side = cs // straight link to the single child wins
				case len(ps) > 0:
					side = ps
				default:
					side = cs
				}
				vals := make([]float64, 0, len(side))
				for _, nb := range side {
					vals = append(vals, center[nb])
				}
				want := median(vals)
				// Floor: must clear the box above in this slot.
				lo := math.Inf(-1)
				if i > 0 {
					lo = center[ln[i-1]] + minGapBetween(ln[i-1], name)
				}
				// Ceiling: must stay above the box below.
				hi := math.Inf(1)
				if i < len(ln)-1 {
					hi = center[ln[i+1]] - minGapBetween(name, ln[i+1])
				}
				if want >= lo && want <= hi {
					center[name] = want
				}
			}
		}
	}
	for sweep := 0; sweep < 4; sweep++ {
		straighten()
	}

	// Normalise so the topmost box sits at the vertical margin (centres can
	// have drifted negative or far down during the sweeps).
	minCenter := 0.0
	first := true
	for l := 0; l <= maxLayer; l++ {
		for _, name := range byLayer[l] {
			top := center[name] - float64(heightFor(name))/2
			if first || top < minCenter {
				minCenter = top
				first = false
			}
		}
	}
	shift := float64(cfg.VMargin) - minCenter

	// Materialise nodes. Box width is uniform (cfg.NodeW); box height is
	// per-node so each box hugs its content. Y comes from the aligned
	// centre minus half the box height.
	nodes := make([]graphNode, 0, len(stages)+len(dummyNames))
	idxByName := make(map[string]int, len(stages)+len(dummyNames))
	for l := 0; l <= maxLayer; l++ {
		for s, name := range byLayer[l] {
			h := heightFor(name)
			x := cfg.HMargin + l*(cfg.NodeW+cfg.ColGap)
			y := int(center[name]+shift) - h/2
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

	// Total canvas size. Width spans every layer column plus gaps; height
	// is the lowest box bottom across all nodes (centres were normalised so
	// the topmost box sits at the vertical margin) plus a margin.
	width := cfg.HMargin
	if maxLayer >= 0 {
		width += (maxLayer+1)*cfg.NodeW + maxLayer*cfg.ColGap
	}
	height := cfg.VMargin
	for _, n := range nodes {
		if b := n.Y + n.H; b > height {
			height = b
		}
	}
	height += cfg.VMargin

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

// connectorAt sets a line glyph at (x, y), merging it with any line glyph
// already there so crossings and tees pick the right box-drawing rune. The
// glyph is computed from which of the four directions the new and existing
// runes occupy, then looked up in a bitmask table. Non-line cells (box
// borders, spaces, text) are overwritten plainly so edges never garble a
// node. dirMask: 1=up, 2=down, 4=left, 8=right.
func (c *canvas) connectorAt(x, y int, dirs int, style lipgloss.Style) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	existing := lineDirs(c.cells[y][x].r)
	merged := dirs
	if existing != 0 {
		merged |= existing
	}
	r := lineGlyph(merged)
	if r == 0 {
		r = lineGlyph(dirs)
	}
	c.set(x, y, r, style)
}

// lineDirs returns the direction bitmask a box-drawing rune occupies, or 0
// if r isn't one of the connector glyphs the router produces.
func lineDirs(r rune) int {
	switch r {
	case '─':
		return 4 | 8
	case '│':
		return 1 | 2
	case '┌', '╭':
		return 2 | 8
	case '┐', '╮':
		return 2 | 4
	case '└', '╰':
		return 1 | 8
	case '┘', '╯':
		return 1 | 4
	case '├':
		return 1 | 2 | 8
	case '┤':
		return 1 | 2 | 4
	case '┬':
		return 2 | 4 | 8
	case '┴':
		return 1 | 4 | 8
	case '┼':
		return 1 | 2 | 4 | 8
	}
	return 0
}

// lineGlyph maps a direction bitmask back to a box-drawing rune. Returns 0
// for masks with no glyph (e.g. a single direction, which a connector
// never stands alone as).
func lineGlyph(dirs int) rune {
	switch dirs {
	case 4 | 8:
		return '─'
	case 1 | 2:
		return '│'
	case 2 | 8:
		return '╭'
	case 2 | 4:
		return '╮'
	case 1 | 8:
		return '╰'
	case 1 | 4:
		return '╯'
	case 1 | 2 | 8:
		return '├'
	case 1 | 2 | 4:
		return '┤'
	case 2 | 4 | 8:
		return '┬'
	case 1 | 4 | 8:
		return '┴'
	case 1 | 2 | 4 | 8:
		return '┼'
	}
	return 0
}

func (c *canvas) writeAt(x, y int, s string, style lipgloss.Style) {
	for _, r := range s {
		c.set(x, y, r, style)
		x++
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

// graphRenderCache memoises the final rendered string for a given set
// of inputs. The output of renderGraph is byte-identical for the same
// (layoutVersion, cursorIdx, viewW, viewH, panX, panY) tuple, so a fast
// wheel burst past the top or bottom of the layer (where the cursor
// doesn't move) hits the cache and skips the full canvas paint.
type graphRenderCache struct {
	layoutVersion int
	cursorIdx     int
	viewW, viewH  int
	panX, panY    int
	out           string
	valid         bool
}

// renderGraphCached is renderGraph with a single-slot cache keyed on
// every input that affects the rendered output. The cache pointer comes
// from the Model so it survives across value copies. Pass a nil cache to
// bypass.
func renderGraphCached(cache *graphRenderCache, layoutVersion int, g graphLayout, cursorIdx, viewW, viewH, panX, panY int) string {
	if cache != nil && cache.valid &&
		cache.layoutVersion == layoutVersion &&
		cache.cursorIdx == cursorIdx &&
		cache.viewW == viewW && cache.viewH == viewH &&
		cache.panX == panX && cache.panY == panY {
		return cache.out
	}
	out := renderGraph(g, cursorIdx, viewW, viewH, panX, panY)
	if cache != nil {
		cache.layoutVersion = layoutVersion
		cache.cursorIdx = cursorIdx
		cache.viewW, cache.viewH = viewW, viewH
		cache.panX, cache.panY = panX, panY
		cache.out = out
		cache.valid = true
	}
	return out
}

// renderGraph paints g onto a fresh canvas, then crops a viewW × viewH
// viewport anchored at (panX, panY). The caller (graphView via
// recomputeGraphPan) is responsible for choosing a pan that keeps the
// cursor node on screen; cursorIdx is used only for cursor highlighting.
func renderGraph(g graphLayout, cursorIdx, viewW, viewH, panX, panY int) string {
	cv := newCanvas(g.width, g.height)
	bgStyle := lipgloss.NewStyle().Background(bg)
	cursorBorderStyle := lipgloss.NewStyle().Foreground(selected).Background(bg).Bold(true)

	paintGraphEdges(g, cv, cursorIdx)

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

// paintGraphEdges paints every edge of g onto cv. cursorIdx (-1 for none)
// highlights every edge touching the cursor node, incoming and outgoing;
// those are painted last in the selected colour so they win at shared cells.
//
// Routing model, mirroring the Kargo web UI. Each edge makes a single
// vertical turn so converging/diverging edges read as trunks, not a tangle:
//
//   - Fan-in (target has >1 incoming): all sources converge on one vertical
//     "bus" at the gap midpoint before the target.
//   - Fan-out / single edge: the edge turns on the source's bus, so a source's
//     branches split from one trunk.
//   - Multi-layer edges (routed through dummy nodes): each rides its dummy's
//     reserved clear row for the long horizontal run, never a box row.
//   - An edge already on its target's row draws as a single flat line.
//
// connectorAt merges box-drawing glyphs so a bus grows ┤ / ┬ / ┴ tees where
// edges join and rounds the pure corners (╭╮╰╯).
func paintGraphEdges(g graphLayout, cv *canvas, cursorIdx int) {
	edgeStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	cursorEdgeStyle := lipgloss.NewStyle().Foreground(selected).Background(bg).Bold(true)

	// Determine which original edges the cursor highlights. EVERY edge that
	// touches the cursor lights up, both incoming and outgoing, so selecting a
	// node shows its full connectivity at once. An edge is touched when either
	// of its segment endpoints is the cursor; for chained long edges, the
	// segment adjacent to the cursor matches and every segment sharing that
	// Original lights up via the same set.
	highlightedOrigs := make(map[int]bool)
	if cursorIdx >= 0 && cursorIdx < len(g.nodes) {
		for _, e := range g.edges {
			if e.From == cursorIdx || e.To == cursorIdx {
				highlightedOrigs[e.Original] = true
			}
		}
	}

	// Bus routing, mirroring the Kargo web UI. Each edge makes a single
	// vertical turn at one shared "bus" column inside the gap, so converging
	// or diverging edges read as one trunk instead of a tangle of private
	// lanes. Which column an edge uses depends on the shape it's part of:
	//
	//   - Fan-in (target has >1 incoming edge): the bus is anchored to the
	//     TARGET — one column just before it where every source's horizontal
	//     converges. Stacked fan-in targets in the same layer stagger by slot
	//     so one target's vertical never runs through another's approach rows.
	//   - Otherwise (the target has a single incoming edge): the bus is
	//     anchored to the SOURCE — one column shared by all of that source's
	//     outgoing edges. A pure fan-out then splits at a single trunk with no
	//     per-target offset, instead of each branch jogging at its own column.
	//
	// Source horizontals sit at distinct rows so they never overlap, and they
	// all meet the same vertical. connectorAt merges glyphs so the bus grows
	// ┤ / ┬ / ┴ tees where edges join and rounds the pure corners. An edge
	// whose source already sits on the target's row draws as one flat line.
	inDeg := make([]int, len(g.nodes))
	for _, e := range g.edges {
		inDeg[e.To]++
	}
	gapMidFor := func(layer int) int {
		gapStart := g.cfg.HMargin + (layer-1)*(g.cfg.NodeW+g.cfg.ColGap) + g.cfg.NodeW
		return gapStart + g.cfg.ColGap/2
	}
	clampBus := func(layer, x int) int {
		gapStart := g.cfg.HMargin + (layer-1)*(g.cfg.NodeW+g.cfg.ColGap) + g.cfg.NodeW
		lo := gapStart + 1
		hi := gapStart + g.cfg.ColGap - 1
		if x < lo {
			x = lo
		}
		if x > hi {
			x = hi
		}
		return x
	}
	// targetBusOf: fan-in convergence column, staggered by the target's slot.
	// sourceBusOf: fan-out trunk column, one per source (gap midpoint, no
	// stagger so all of a source's branches split at the same vertical).
	targetBusOf := make([]int, len(g.nodes))
	sourceBusOf := make([]int, len(g.nodes))
	for i, n := range g.nodes {
		targetBusOf[i] = clampBus(n.Layer, gapMidFor(n.Layer)+n.Slot%3)
		sourceBusOf[i] = clampBus(n.Layer+1, gapMidFor(n.Layer+1))
	}

	// Endpoint geometry for an edge: the source-exit and target-entry cells.
	endpoints := func(e graphEdge) (sx, sy, ex, ey int) {
		from, to := g.nodes[e.From], g.nodes[e.To]
		sx = from.X + from.W
		sy = from.Y + from.H/2
		if from.Dummy {
			sx = from.X
		}
		ex = to.X - 1
		ey = to.Y + to.H/2
		if to.Dummy {
			ex = to.X + to.W - 1
		}
		return sx, sy, ex, ey
	}

	// busColOf: the vertical-turn column an edge uses. Fan-in edges (target
	// with >1 incoming) converge on the target bus; everything else splits
	// from the source bus.
	busColOf := func(e graphEdge, sx, ex int) int {
		bus := sourceBusOf[e.From]
		if inDeg[e.To] > 1 {
			bus = targetBusOf[e.To]
		}
		if bus <= sx {
			bus = sx + 1
		}
		if bus >= ex {
			bus = ex - 1
		}
		return bus
	}

	// A multi-layer edge is split into a chain of single-layer segments
	// through dummy nodes (see layoutGraph). Each dummy occupies its own
	// reserved 1-cell row that the layout keeps clear of real boxes, so a
	// long edge already has a dedicated horizontal lane: its dummy row. The
	// trick is to get the edge ONTO that row immediately on leaving the
	// source, so the long horizontal run lives entirely on the clear dummy
	// row instead of on the source's box row (where it would co-run with
	// every other edge leaving that row and merge into one line).
	//
	// So a segment feeding a dummy turns vertically right at the source's
	// right edge (one cell out) down/up to the dummy row, then runs flat to
	// the dummy. A segment leaving a dummy runs flat along the clear dummy row
	// and only turns into the target row at the last cell before the target,
	// so the long run still owns the dummy row. Only genuine real→real turns
	// use the shared bus, where merging is wanted (fan-out trunk, fan-in
	// convergence).
	paintEdge := func(ei int, style lipgloss.Style) {
		e := g.edges[ei]
		from, to := g.nodes[e.From], g.nodes[e.To]
		sx, sy, ex, ey := endpoints(e)
		if sy == ey {
			// Already on one row: a single flat horizontal.
			for x := sx; x <= ex; x++ {
				cv.connectorAt(x, sy, 4|8, style)
			}
			if !to.Dummy {
				cv.set(ex, ey, '▶', style)
			}
			return
		}
		// Segment touching a dummy: turn onto the clear dummy row as early as
		// possible (feeding a dummy) or stay on it until the last cell
		// (leaving a dummy), so the long horizontal run owns the dummy's
		// reserved row instead of co-running on a box row.
		if from.Dummy || to.Dummy {
			if to.Dummy {
				// Feeding a dummy: short stub on the source row, turn down/up
				// at sx+1, then flat along the dummy row to the dummy.
				turn := sx + 1
				cv.connectorAt(sx, sy, 4|8, style)
				y1, y2 := sy, ey
				if y1 > y2 {
					y1, y2 = y2, y1
				}
				for y := y1 + 1; y < y2; y++ {
					cv.connectorAt(turn, y, 1|2, style)
				}
				if ey > sy {
					cv.connectorAt(turn, sy, 2|4, style) // ╮
					cv.connectorAt(turn, ey, 1|8, style) // ╰
				} else {
					cv.connectorAt(turn, sy, 1|4, style) // ╯
					cv.connectorAt(turn, ey, 2|8, style) // ╭
				}
				for x := turn + 1; x <= ex; x++ {
					cv.connectorAt(x, ey, 4|8, style)
				}
				return
			}
			// Leaving a dummy: flat along the dummy row to ex-1, turn into the
			// target row, short stub into the target.
			turn := ex - 1
			for x := sx; x < turn; x++ {
				cv.connectorAt(x, sy, 4|8, style)
			}
			y1, y2 := sy, ey
			if y1 > y2 {
				y1, y2 = y2, y1
			}
			for y := y1 + 1; y < y2; y++ {
				cv.connectorAt(turn, y, 1|2, style)
			}
			if ey > sy {
				cv.connectorAt(turn, sy, 2|4, style) // ╮ flat-in from left, down
				cv.connectorAt(turn, ey, 1|8, style) // ╰ up-in, out right to target
			} else {
				cv.connectorAt(turn, sy, 1|4, style) // ╯ flat-in from left, up
				cv.connectorAt(turn, ey, 2|8, style) // ╭ down-in, out right to target
			}
			cv.connectorAt(ex, ey, 4|8, style)
			cv.set(ex, ey, '▶', style)
			return
		}
		// Real → real turn: route over the shared bus so fan-outs split from a
		// single trunk and fan-ins converge on one vertical.
		bus := busColOf(e, sx, ex)
		for x := sx; x < bus; x++ {
			cv.connectorAt(x, sy, 4|8, style)
		}
		y1, y2 := sy, ey
		if y1 > y2 {
			y1, y2 = y2, y1
		}
		for y := y1 + 1; y < y2; y++ {
			cv.connectorAt(bus, y, 1|2, style)
		}
		if ey > sy {
			cv.connectorAt(bus, sy, 2|4, style) // ╮
			cv.connectorAt(bus, ey, 1|8, style) // ╰
		} else {
			cv.connectorAt(bus, sy, 1|4, style) // ╯
			cv.connectorAt(bus, ey, 2|8, style) // ╭
		}
		for x := bus + 1; x <= ex; x++ {
			cv.connectorAt(x, ey, 4|8, style)
		}
		cv.set(ex, ey, '▶', style)
	}

	// Non-highlighted edges first, then highlighted on top so the selected
	// node's connections win at any shared cell.
	for ei, e := range g.edges {
		if highlightedOrigs[e.Original] {
			continue
		}
		paintEdge(ei, edgeStyle)
	}
	for ei, e := range g.edges {
		if highlightedOrigs[e.Original] {
			paintEdge(ei, cursorEdgeStyle)
		}
	}
}

// graphPanOffsetFor returns the top-left canvas coordinate the renderer
// pans to. It keeps the cursor node fully on screen with a small margin
// but otherwise preserves the incoming prevX/prevY so the viewport
// doesn't shift when the cursor moves between already-visible nodes.
// Called by recomputeGraphPan once per Update. The result is stored on
// the Model and consumed by the renderer (graphView) and the click
// hit-tester (hitTestGraphNode) so they agree on the visible region.
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

// drawNode paints a single node box. The top row is a filled title bar
// carrying the stage name (web-UI card header); each body row below is one
// "key: value" pair from buildNodeRows. Box height is set per-node during
// layout so each box hugs its content.
func drawNode(cv *canvas, n graphNode, border, bgStyle lipgloss.Style, cursor bool, borderColor color.Color) {
	x, y := n.X, n.Y
	w, h := n.W, n.H

	// Title bar: the top row is a solid colour strip carrying the stage
	// name, mirroring the Kargo web UI card header. The name renders in the
	// dark reverse-video foreground on the state colour so it reads as a
	// filled chip; on the cursor it flips to the selected colour. The strip
	// spans the full box width so it caps the box top corner-to-corner and the
	// side borders meet it flush, with the name inset one cell from the edge.
	barBG := borderColor
	titleStyle := lipgloss.NewStyle().Foreground(darkFg).Background(barBG).Bold(true)
	title := " " + n.Stage.Name
	cv.writeAt(x, y, fitToWidth(title, w), titleStyle)

	// Body border below the title bar — rounded corners on the bottom,
	// vertical sides. Heavy double-line on the cursor so selection always
	// wins against any state colour.
	bl, br, hor, ver := '╰', '╯', '─', '│'
	if cursor {
		bl, br, hor, ver = '╚', '╝', '═', '║'
	}
	cv.set(x, y+h-1, bl, border)
	cv.set(x+w-1, y+h-1, br, border)
	for i := x + 1; i < x+w-1; i++ {
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

	// Body rows start one pad row below the title bar (row y+1 stays blank)
	// so the header has consistent breathing space above the first line.
	rowY := y + 2

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
		Render("v details · x expand · P promote · l logs · / search · n/N next/prev · ? help")

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
		Render(renderGraphCached(m.graphRender, m.graphLayoutVersion, g, cursorIdx, bodyW, bodyH, m.graphPanX, m.graphPanY))

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
		yankColor := healthy
		if m.yankedIsError {
			yankColor = degraded
		}
		yankLine := lipgloss.NewStyle().Foreground(yankColor).Background(bg).Padding(0, 1).Render(m.yankedMessage)
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
	// Bump the version up front so any cache check that runs against the
	// new layout sees a fresh key (and so does the cache write after the
	// next renderGraphCached call).
	m.graphLayoutVersion++
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
// / right step to the closest neighbour by edge; when the current node has
// no edge in that direction, the cursor "flows" to the nearest non-dummy
// node on the next existing layer in that direction, measured by terminal
// row (cell Y). up / down step within the same layer to the previous /
// next slot.
// Returns true when the cursor actually moved so callers can skip
// selection-driven work (panel reset, panel refresh) on a no-op step,
// which is the common case when the wheel keeps firing past the top or
// bottom of the layer.
func (m *Model) moveGraphCursor(dir string) bool {
	g := m.graphLayout
	if m.graphCursor < 0 || m.graphCursor >= len(g.nodes) {
		return false
	}
	prev := m.graphCursor
	cur := g.nodes[m.graphCursor]
	switch dir {
	case "right":
		// Pick outgoing edge whose target is closest in slot.
		_, out := graphNeighbors(g, m.graphCursor)
		if len(out) == 0 {
			if flow, ok := nearestNodeInDir(g, m.graphCursor, +1); ok {
				m.graphCursor = flow
			} else {
				return false
			}
		} else {
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
		}
	case "left":
		in, _ := graphNeighbors(g, m.graphCursor)
		if len(in) == 0 {
			if flow, ok := nearestNodeInDir(g, m.graphCursor, -1); ok {
				m.graphCursor = flow
			} else {
				return false
			}
		} else {
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
		}
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
			return false
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
	return m.graphCursor != prev
}

// moveGraphCursorWithin shifts the cursor by delta non-dummy siblings inside
// the current layer. Used by pgup/pgdown/home/end. Positive delta moves
// down, negative up; the value is clamped to the layer bounds so a big
// delta (e.g. len(nodes) for home/end) lands on the first or last slot.
func (m *Model) moveGraphCursorWithin(delta int) bool {
	g := m.graphLayout
	if m.graphCursor < 0 || m.graphCursor >= len(g.nodes) {
		return false
	}
	cur := g.nodes[m.graphCursor]
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
	if curPos < 0 || len(sibs) == 0 {
		return false
	}
	next := curPos + delta
	if next < 0 {
		next = 0
	}
	if next > len(sibs)-1 {
		next = len(sibs) - 1
	}
	if sibs[next] == m.graphCursor {
		return false
	}
	m.graphCursor = sibs[next]
	return true
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

// nearestNodeInDir finds the closest non-dummy node to walk into when the
// current node has no edge in the requested direction. step is +1 for
// right, -1 for left. The search advances one layer at a time and returns
// the candidate on the first layer that has any non-dummy nodes, picking
// the one whose vertical centre sits closest (in terminal rows, the cell
// Y coordinate from graphNode) to the current node's centre. Tie-breaks
// pick the smaller Slot for stable behaviour. Returns (-1, false) when
// no further layer in that direction holds a real node.
func nearestNodeInDir(g graphLayout, idx, step int) (int, bool) {
	if idx < 0 || idx >= len(g.nodes) || (step != 1 && step != -1) {
		return -1, false
	}
	cur := g.nodes[idx]
	curMid := cur.Y + cur.H/2

	maxLayer := 0
	for _, n := range g.nodes {
		if n.Layer > maxLayer {
			maxLayer = n.Layer
		}
	}

	for layer := cur.Layer + step; layer >= 0 && layer <= maxLayer; layer += step {
		best := -1
		bestDist := 0
		bestSlot := 0
		for i, n := range g.nodes {
			if n.Layer != layer || n.Dummy || n.Stage == nil {
				continue
			}
			mid := n.Y + n.H/2
			d := abs(mid - curMid)
			if best < 0 || d < bestDist || (d == bestDist && n.Slot < bestSlot) {
				best = i
				bestDist = d
				bestSlot = n.Slot
			}
		}
		if best >= 0 {
			return best, true
		}
	}
	return -1, false
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
