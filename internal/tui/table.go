package tui

import (
	"strings"

	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"

	"unknwon.dev/kargo-tui/internal/kargo"
)

func (m *Model) scrollLeft() {
	switch m.view {
	case viewDeploys, viewControlFlow:
		if m.deploysColOffset > 0 {
			m.deploysColOffset--
			m.refreshRows()
		}
	case viewFreights:
		if m.freightsColOffset > 0 {
			m.freightsColOffset--
			m.refreshRows()
		}
	}
}

func (m *Model) scrollRight() {
	switch m.view {
	case viewDeploys, viewControlFlow:
		if m.deploysColOffset < maxColOffset(len(allStageColumns)) {
			m.deploysColOffset++
			m.refreshRows()
		}
	case viewFreights:
		if m.freightsColOffset < maxColOffset(len(allFreightColumns)) {
			m.freightsColOffset++
			m.refreshRows()
		}
	}
}

// applyCursorMarker rewrites column 0 of every row so only the cursor row has
// a visible marker. The bubbles table's Selected style is a row-level wrapper
// that loses to per-cell ANSI foreground codes on most terminals, so an
// in-cell glyph guarantees a visible cursor indicator.
func applyCursorMarker(t *table.Model) {
	rows := t.Rows()
	cols := t.Columns()
	cur := t.Cursor()
	w := 0
	if len(cols) > 0 {
		w = cols[0].Width
	}
	bgPad := lipgloss.NewStyle().Background(bg)
	marker := lipgloss.NewStyle().Foreground(selected).Background(bg).Bold(true).Render(cursorMarkerGlyph)
	marked := marker
	blank := bgPad.Render(" ")
	if w > 1 {
		marked = marker + bgPad.Render(strings.Repeat(" ", w-lipgloss.Width(marker)))
		blank = bgPad.Render(strings.Repeat(" ", w))
	}
	for i := range rows {
		if len(rows[i]) == 0 {
			continue
		}
		if i == cur {
			rows[i][0] = marked
		} else {
			rows[i][0] = blank
		}
	}
	t.SetRows(rows)
}

// horizontalSlice returns the column index window to render. Columns 0
// (cursor marker) and 1 (Name) are always pinned; offset advances the start
// of the remaining columns so they scroll past the pinned pair.
func horizontalSlice(total, offset int) []int {
	if total <= 2 {
		idx := make([]int, total)
		for i := range idx {
			idx[i] = i
		}
		return idx
	}
	if offset < 0 {
		offset = 0
	}
	if offset > total-3 {
		offset = total - 3
	}
	idx := []int{0, 1}
	for i := 2 + offset; i < total; i++ {
		idx = append(idx, i)
	}
	return idx
}

func sliceColumns(all []table.Column, idx []int) []table.Column {
	out := make([]table.Column, 0, len(idx))
	for _, i := range idx {
		out = append(out, all[i])
	}
	return out
}

func sliceRow(row table.Row, idx []int) table.Row {
	out := make(table.Row, 0, len(idx))
	for _, i := range idx {
		if i < len(row) {
			out = append(out, row[i])
		} else {
			out = append(out, "")
		}
	}
	return out
}

// padRowCellsBg right-pads every cell in row to its column width with
// bg-colored spaces. The bubbles table wraps each cell in an inner
// Width(col.Width).Inline().Render(value), and that inner padding uses
// whatever whitespace style the inner style carries — which has no bg,
// so the terminal default leaks through between columns. Pre-padding
// the value to col.Width makes the inner Width() a no-op so the
// trailing gap is painted with our bg.
func padRowCellsBg(row table.Row, cols []table.Column) table.Row {
	out := make(table.Row, len(row))
	pad := lipgloss.NewStyle().Background(bg)
	for i, c := range row {
		w := 0
		if i < len(cols) {
			w = cols[i].Width
		}
		if w <= 0 {
			out[i] = c
			continue
		}
		gap := w - lipgloss.Width(c)
		if gap <= 0 {
			out[i] = c
			continue
		}
		out[i] = c + pad.Render(strings.Repeat(" ", gap))
	}
	return out
}

func maxColOffset(total int) int {
	if total <= 2 {
		return 0
	}
	return total - 3
}

func (m *Model) refreshRows() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))

	// The tree and graph views need no filter/sort/column logic — just
	// rebuild them from m.deploys whenever the source data changes.
	m.rebuildTree()
	m.rebuildGraph()

	switch m.view {
	case viewDeploys, viewControlFlow:
		controlOnly := m.view == viewControlFlow
		// Preserve the selected stage by name across refreshes. Cursor index
		// alone is not enough: when the server returns rows in a slightly
		// different order, the cursor would land on a different stage even
		// though its numeric position is unchanged.
		var prevName string
		if cur := m.deploysTable.Cursor(); cur >= 0 && cur < len(m.visibleDeploys) {
			prevName = m.visibleDeploys[cur].Name
		}
		sorted := m.sortDeploys(m.deploys)
		rows := make([]table.Row, 0, len(sorted))
		visible := make([]kargo.Stage, 0, len(sorted))
		for _, s := range sorted {
			if controlOnly && !s.IsControlFlow {
				continue
			}
			if !controlOnly && s.IsControlFlow {
				continue
			}
			if q != "" {
				hay := strings.ToLower(s.Name + " " + s.Shard + " " + s.FreightSummary)
				if !strings.Contains(hay, q) {
					continue
				}
			}
			rows = append(rows, table.Row{
				" ",
				stageNameCell(s.Name, s.Health),
				healthCell(s.Health),
				stageArgoCell(s.ArgoCDApps),
				promoCell(s.LastPromo),
				stageFreightSummary(s.FreightSummary, s.IsControlFlow, m.aliasOf(s.FreightSummary)),
				ageString(s.Created),
				stringOrDash(s.Shard),
			})
			visible = append(visible, s)
		}
		idx := horizontalSlice(len(allStageColumns), m.deploysColOffset)
		slicedCols := sliceColumns(allStageColumns, idx)
		slicedRows := make([]table.Row, len(rows))
		for i, r := range rows {
			slicedRows[i] = padRowCellsBg(sliceRow(r, idx), slicedCols)
		}
		// Skip the rebuild entirely when the visible rows are unchanged.
		// SetRows + SetColumns reset the table's internal viewport offset
		// (no public accessor for YOffset) which causes the visible window
		// to "jump" on every auto-refresh even though nothing the user can
		// see has actually changed.
		if !sameStageRows(m.visibleDeploys, visible) || !sameColumnSlice(m.deploysTable.Columns(), slicedCols) {
			// Order matters: clear rows BEFORE changing columns. The bubbles
			// table re-renders on SetColumns, and panics if any row has more
			// cells than the new column count.
			m.deploysTable.SetRows(nil)
			m.deploysTable.SetColumns(slicedCols)
			m.deploysTable.SetRows(slicedRows)
			m.visibleDeploys = visible
			want := -1
			if prevName != "" {
				for i, s := range visible {
					if s.Name == prevName {
						want = i
						break
					}
				}
			}
			clampCursor(&m.deploysTable, want)
		} else {
			m.visibleDeploys = visible
		}
		applyCursorMarker(&m.deploysTable)

	case viewFreights:
		// Same identity-preserving cursor logic as the deploys branch above.
		var prevName string
		if cur := m.freightsTable.Cursor(); cur >= 0 && cur < len(m.visibleFreights) {
			prevName = m.visibleFreights[cur].Name
		}
		sorted := m.sortFreights(m.freights)
		rows := make([]table.Row, 0, len(sorted))
		visible := make([]kargo.Freight, 0, len(sorted))
		for _, f := range sorted {
			if q != "" {
				hay := strings.ToLower(f.Name + " " + f.Warehouse + " " + f.Alias)
				if !strings.Contains(hay, q) {
					continue
				}
			}
			rows = append(rows, table.Row{
				" ",
				freightNameCell(f.Name, f.Alias),
				ageString(f.Created),
				countCell(f.VerifiedIn),
				countCell(f.ApprovedFor),
				stringOrDash(f.Warehouse),
			})
			visible = append(visible, f)
		}
		idx := horizontalSlice(len(allFreightColumns), m.freightsColOffset)
		slicedCols := sliceColumns(allFreightColumns, idx)
		slicedRows := make([]table.Row, len(rows))
		for i, r := range rows {
			slicedRows[i] = padRowCellsBg(sliceRow(r, idx), slicedCols)
		}
		// See the deploys branch above for why we skip the rebuild on
		// equal-row refreshes — preserves the table's internal scroll.
		if !sameFreightRows(m.visibleFreights, visible) || !sameColumnSlice(m.freightsTable.Columns(), slicedCols) {
			m.freightsTable.SetRows(nil)
			m.freightsTable.SetColumns(slicedCols)
			m.freightsTable.SetRows(slicedRows)
			m.visibleFreights = visible
			want := -1
			if prevName != "" {
				for i, f := range visible {
					if f.Name == prevName {
						want = i
						break
					}
				}
			}
			clampCursor(&m.freightsTable, want)
		} else {
			m.visibleFreights = visible
		}
		applyCursorMarker(&m.freightsTable)
	}
}

// sameStageRows returns true when two stage slices would render identically
// in the table — same length, same names in the same order, same health,
// freight summary, last-promo phase, shard, and Argo CD state (the visible
// columns). When this is true, the auto-refresh can skip SetRows entirely
// and preserve the table's scroll offset.
func sameStageRows(a, b []kargo.Stage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name ||
			a[i].Health != b[i].Health ||
			a[i].FreightSummary != b[i].FreightSummary ||
			a[i].LastPromo != b[i].LastPromo ||
			a[i].Shard != b[i].Shard ||
			!sameArgoApps(a[i].ArgoCDApps, b[i].ArgoCDApps) {
			return false
		}
	}
	return true
}

// sameArgoApps returns true when two Argo app slices render identically in
// the deploy list's "Argo" cell — same set of apps in the same order with
// the same health and sync state. Cell rendering only consumes Health and
// Sync, so other ArgoCDAppRef fields don't affect equality.
func sameArgoApps(a, b []kargo.ArgoCDAppRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Namespace != b[i].Namespace ||
			a[i].Name != b[i].Name ||
			a[i].Health != b[i].Health ||
			a[i].Sync != b[i].Sync {
			return false
		}
	}
	return true
}

// sameFreightRows is the freight-side counterpart to sameStageRows.
func sameFreightRows(a, b []kargo.Freight) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name ||
			a[i].Alias != b[i].Alias ||
			a[i].Warehouse != b[i].Warehouse ||
			a[i].VerifiedIn != b[i].VerifiedIn ||
			a[i].ApprovedFor != b[i].ApprovedFor {
			return false
		}
	}
	return true
}

// sameColumnSlice returns true when two column slices have the same titles
// in the same order. Used to detect when horizontal scroll has changed and
// we therefore must rebuild the table.
func sameColumnSlice(a, b []table.Column) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Title != b[i].Title {
			return false
		}
	}
	return true
}

func clampCursor(t *table.Model, want int) {
	n := len(t.Rows())
	if n == 0 {
		t.SetCursor(0)
		return
	}
	if want >= n {
		want = n - 1
	}
	if want < 0 {
		want = 0
	}
	// SetCursor moves the cursor index but does NOT adjust the viewport's
	// Y-offset, so after a refresh the visible window can scroll
	// independently of where the cursor lands. Reset to top first, then
	// MoveDown so the underlying offset tracks the cursor — this matches the
	// motion the table does when arrow keys are pressed.
	t.GotoTop()
	if want > 0 {
		t.MoveDown(want)
	}
}
