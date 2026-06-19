package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unknwon.dev/kargo-tui/internal/kargo"
)

// TestGraphEdgeWiring guards against edges connecting the wrong boxes. It
// renders the layout, then for every real edge checks that an arrowhead
// glyph lands exactly on the target node's left border row, and that no
// arrowhead lands on a non-target box. The topology has three independent
// chains, two of which end in same-column "ready" stages that sit close
// enough that a misrouted edge would point at the wrong one.
func TestGraphEdgeWiring(t *testing.T) {
	now := time.Now()
	stages := []kargo.Stage{
		{Name: "a-canary", FreightSummary: "n/a", Created: now},
		{Name: "a-staging", Health: "Healthy", FreightSummary: "sha00001", Created: now, Upstreams: []string{"a-canary"}},
		{Name: "a-tier1", Health: "Healthy", FreightSummary: "sha00002", Created: now, Upstreams: []string{"a-staging"}},
		{Name: "a-leaf1", Health: "Healthy", FreightSummary: "sha00003", Created: now, Upstreams: []string{"a-tier1"}},
		{Name: "a-leaf2", Health: "Healthy", FreightSummary: "sha00004", Created: now, Upstreams: []string{"a-tier1"}},
		{Name: "a-leaf3", Health: "Healthy", FreightSummary: "sha00004", Created: now, Upstreams: []string{"a-tier1"}},
		{Name: "b-canary", Health: "Healthy", FreightSummary: "sha00005", Created: now},
		{Name: "b-mid", Health: "Healthy", FreightSummary: "sha00006", Created: now, Upstreams: []string{"b-canary"}},
		{Name: "b-ready", Health: "Healthy", FreightSummary: "sha00006", Created: now, Upstreams: []string{"b-mid"}},
		{Name: "c-canary", Health: "Healthy", FreightSummary: "sha00005", Created: now},
		{Name: "c-leaf1", Health: "Healthy", FreightSummary: "sha00007", Created: now, Upstreams: []string{"c-canary"}},
		{Name: "c-leaf2", Health: "Healthy", FreightSummary: "sha00007", Created: now, Upstreams: []string{"c-canary"}},
		{Name: "c-ready", FreightSummary: "0/1 Fulfilled", Created: now, Upstreams: []string{"c-leaf1", "c-leaf2"}},
	}
	m := Model{}
	g := layoutGraph(stages, defaultGraphCfg(), m)

	idx := func(name string) int {
		i, ok := g.byName[name]
		require.Truef(t, ok, "node %q not found", name)
		return i
	}

	// Every arrowhead must sit one cell left of its target's left border, at
	// the target's vertical centre row. Build the expected set from the
	// upstream relationships and assert each is present in the painted canvas.
	cv := newCanvas(g.width, g.height)
	paintGraphEdges(g, cv, -1)

	type want struct{ from, to string }
	wants := []want{
		{"a-canary", "a-staging"},
		{"a-staging", "a-tier1"},
		{"a-tier1", "a-leaf1"},
		{"a-tier1", "a-leaf2"},
		{"a-tier1", "a-leaf3"},
		{"b-canary", "b-mid"},
		{"b-mid", "b-ready"},
		{"c-canary", "c-leaf1"},
		{"c-canary", "c-leaf2"},
		{"c-leaf1", "c-ready"},
		{"c-leaf2", "c-ready"},
	}
	for _, w := range wants {
		to := g.nodes[idx(w.to)]
		ax := to.X - 1
		ay := to.Y + to.H/2
		assert.Equalf(t, '▶', cv.cells[ay][ax].r,
			"edge %s -> %s: expected arrowhead at target left edge (%d,%d), got %q",
			w.from, w.to, ax, ay, string(cv.cells[ay][ax].r))
	}

	// No arrowhead may land on the WRONG box. Collect every arrowhead cell
	// and confirm it is exactly one of the expected target anchor points.
	validAnchors := map[[2]int]bool{}
	for _, w := range wants {
		to := g.nodes[idx(w.to)]
		validAnchors[[2]int{to.X - 1, to.Y + to.H/2}] = true
	}
	for y := range cv.cells {
		for x := range cv.cells[y] {
			if cv.cells[y][x].r == '▶' {
				assert.Truef(t, validAnchors[[2]int{x, y}],
					"stray arrowhead at (%d,%d) not on any target's left edge", x, y)
			}
		}
	}
}

// TestGraphNoEdgeThroughUnrelatedBox reproduces a dense topology where
// compaction once pulled an unrelated box onto another's row, so one chain's
// fan-in branch joined the other's exit stub and read as a spurious edge. It
// has three fan-out/fan-in clusters plus a multi-layer edge feeding a later
// fan-in, the same shape that surfaced the bug. Asserts no edge glyph lands
// inside any box and no arrowhead lands anywhere but a true target's left edge.
func TestGraphNoEdgeThroughUnrelatedBox(t *testing.T) {
	mk := func(name string, ups ...string) kargo.Stage {
		return kargo.Stage{Name: name, Health: "Healthy", FreightSummary: "abc123de", Created: time.Unix(1, 0), Upstreams: ups}
	}
	// Three chains. Chain s fans out to s1/s2/s3 then back into s-ready, which
	// then runs a long spine s-ready -> p-canary -> p-eu -> p-tier1 -> {e1,e2,
	// e3} -> p-ready. Chain w (w-canary -> w-mid -> w-ready) feeds p-canary via
	// a multi-layer edge. Chain g fans g-canary out to g1/g2 into g-ready.
	stages := []kargo.Stage{
		mk("s1", "s-tier1"),
		mk("g1", "g-canary"),
		mk("e1", "p-tier1"),
		mk("e2", "p-tier1"),
		mk("s2", "s-tier1"),
		mk("e3", "p-tier1"),
		mk("s3", "s-tier1"),
		mk("p-eu", "p-canary"),
		mk("g2", "g-canary"),
		mk("s-stage", "s-canary"),
		mk("w-mid", "w-canary"),
		mk("s-canary"),
		mk("s-ready", "s3", "s1", "s2"),
		mk("s-tier1", "s-stage"),
		mk("p-canary", "w-ready", "s-ready"),
		mk("p-ready", "e1", "e2", "e3"),
		mk("p-tier1", "p-eu"),
		mk("w-canary"),
		mk("w-ready", "w-mid"),
		mk("g-canary"),
		mk("g-ready", "g1", "g2"),
	}
	g := layoutGraph(stages, defaultGraphCfg(), Model{})

	occ := map[[2]int]string{}
	for _, n := range g.nodes {
		if n.Dummy {
			continue
		}
		for y := n.Y; y < n.Y+n.H; y++ {
			for x := n.X; x < n.X+n.W; x++ {
				occ[[2]int{x, y}] = n.Stage.Name
			}
		}
	}
	cv := newCanvas(g.width, g.height)
	paintGraphEdges(g, cv, -1)

	for y := range cv.cells {
		for x := range cv.cells[y] {
			if cv.cells[y][x].r != ' ' {
				if name, ok := occ[[2]int{x, y}]; ok {
					t.Errorf("edge glyph %q inside box %q at (%d,%d)", string(cv.cells[y][x].r), name, x, y)
				}
			}
		}
	}
	valid := map[[2]int]bool{}
	for _, e := range g.edges {
		to := g.nodes[e.To]
		if to.Dummy {
			continue
		}
		valid[[2]int{to.X - 1, to.Y + to.H/2}] = true
	}
	for y := range cv.cells {
		for x := range cv.cells[y] {
			if cv.cells[y][x].r == '▶' && !valid[[2]int{x, y}] {
				t.Errorf("stray arrowhead at (%d,%d)", x, y)
			}
		}
	}
}

// TestGraphSingleParentStaysStraight guards the bug where a node with a
// single parent but a fan-out of children drifted to the children's median,
// bending the straight incoming edge. A single parent/child link is a spine
// the eye expects to stay flat, so it must win over the fan on the other
// side: the node's vertical centre should equal its single parent's.
func TestGraphSingleParentStaysStraight(t *testing.T) {
	mk := func(name string, ups ...string) kargo.Stage {
		return kargo.Stage{Name: name, Health: "Healthy", FreightSummary: "abc123de", Created: time.Unix(1, 0), Upstreams: ups}
	}
	stages := []kargo.Stage{
		mk("spine"),
		mk("hub", "spine"),
		mk("leaf-a", "hub"),
		mk("leaf-b", "hub"),
		mk("leaf-c", "hub"),
	}
	g := layoutGraph(stages, defaultGraphCfg(), Model{})
	center := func(name string) int {
		i, ok := g.byName[name]
		require.Truef(t, ok, "node %q not found", name)
		n := g.nodes[i]
		return n.Y + n.H/2
	}
	// hub has a single parent (spine) and a 3-way fan-out. Its centre must
	// align with spine's so the spine->hub edge is a dead-straight line.
	assert.Equal(t, center("spine"), center("hub"),
		"hub should align to its single parent spine, not its children's median")
}

// TestGraphNoLongHorizontalOverlap guards the bug where a long multi-layer
// edge ran its horizontal across a box row already used by other edges, so
// two unrelated edges merged into one indistinguishable line. The w-ready ->
// p-canary edge spans several layers and converges with the s-fan-in at
// p-canary, which is the shape that surfaced it. Each edge is painted alone,
// the cells it touches are recorded per row, and we assert no two DISTINCT
// original edges share a long colinear horizontal run. Short shared spans are
// fine: bus verticals (a single column) and fan-out/fan-in junctions
// legitimately co-occupy a few cells.
func TestGraphNoLongHorizontalOverlap(t *testing.T) {
	mk := func(name string, ups ...string) kargo.Stage {
		return kargo.Stage{Name: name, Health: "Healthy", FreightSummary: "abc123de", Created: time.Unix(1, 0), Upstreams: ups}
	}
	stages := []kargo.Stage{
		mk("s1", "s-tier1"),
		mk("s2", "s-tier1"),
		mk("s3", "s-tier1"),
		mk("s-stage", "s-canary"),
		mk("w-mid", "w-canary"),
		mk("s-canary"),
		mk("s-ready", "s3", "s1", "s2"),
		mk("s-tier1", "s-stage"),
		mk("p-canary", "w-ready", "s-ready"),
		mk("w-canary"),
		mk("w-ready", "w-mid"),
	}
	g := layoutGraph(stages, defaultGraphCfg(), Model{})

	// The original endpoints of each edge index, walked back to the real
	// (non-dummy) source and forward to the real target, so a chain of dummy
	// segments shares one (source, target) identity. Two co-running edges
	// that share neither a source nor a target are the merge-into-one-line
	// failure; sharing a source (fan-out trunk) or a target (fan-in stub) is
	// the intended bus merge.
	rootSrc := make([]int, len(g.edges))
	rootDst := make([]int, len(g.edges))
	for i, e := range g.edges {
		rootSrc[i] = e.From
		for g.nodes[rootSrc[i]].Dummy {
			rootSrc[i] = backEdge(g, rootSrc[i])
		}
		rootDst[i] = e.To
		for g.nodes[rootDst[i]].Dummy {
			rootDst[i] = fwdEdge(g, rootDst[i])
		}
	}
	origSrc := map[int]int{}
	origDst := map[int]int{}
	for i, e := range g.edges {
		origSrc[e.Original] = rootSrc[i]
		origDst[e.Original] = rootDst[i]
	}

	// Paint each edge alone, recording which originals lay a horizontal glyph
	// at each cell.
	hOwners := map[[2]int]map[int]bool{}
	for ei := range g.edges {
		cv := newCanvas(g.width, g.height)
		gg := g
		gg.edges = []graphEdge{g.edges[ei]}
		paintGraphEdges(gg, cv, -1)
		for y := range cv.cells {
			for x := range cv.cells[y] {
				if cv.cells[y][x].r == '─' {
					if hOwners[[2]int{x, y}] == nil {
						hOwners[[2]int{x, y}] = map[int]bool{}
					}
					hOwners[[2]int{x, y}][g.edges[ei].Original] = true
				}
			}
		}
	}

	// unrelated reports whether a cell is shared by two originals that have
	// neither a common source nor a common target.
	unrelated := func(owners map[int]bool) (int, int, bool) {
		var os []int
		for o := range owners {
			os = append(os, o)
		}
		for i := 0; i < len(os); i++ {
			for j := i + 1; j < len(os); j++ {
				a, b := os[i], os[j]
				if origSrc[a] != origSrc[b] && origDst[a] != origDst[b] {
					return a, b, true
				}
			}
		}
		return 0, 0, false
	}

	const maxSharedRun = 4
	for y := 0; y < g.height; y++ {
		run := 0
		var prevPair [2]int
		havePrev := false
		for x := 0; x < g.width; x++ {
			a, b, bad := unrelated(hOwners[[2]int{x, y}])
			if bad {
				cur := [2]int{a, b}
				if havePrev && cur == prevPair {
					run++
				} else {
					run = 1
					prevPair = cur
					havePrev = true
				}
				if run > maxSharedRun {
					t.Errorf("row %d: unrelated edges %d and %d share a horizontal run ending at (%d,%d)", y, a, b, x, y)
				}
			} else {
				run = 0
				havePrev = false
			}
		}
	}
}

// backEdge returns the From node of the segment feeding dummy d (its sole
// predecessor in the chain). fwdEdge returns the To node of the segment
// leaving d. Used to walk a long-edge dummy chain back to its real endpoints.
func backEdge(g graphLayout, d int) int {
	for _, e := range g.edges {
		if e.To == d {
			return e.From
		}
	}
	return d
}

func fwdEdge(g graphLayout, d int) int {
	for _, e := range g.edges {
		if e.From == d {
			return e.To
		}
	}
	return d
}
