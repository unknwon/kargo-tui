package tui

import (
	"strings"

	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"

	"unknwon.dev/kargo-tui/internal/kargo"
)

// columnPadding is the extra cells we allocate beyond the longest
// visible value in each column. The bubbles table renders cells flush
// to their declared Width with no inter-column gutter, so without a
// small reserve adjacent cells touch and become hard to read when the
// next column starts with a non-space character.
const columnPadding = 2

// fitColumnWidths returns a copy of cols whose widths are expanded to
// fit the longest visible cell value in each column (column 0, the
// cursor marker, is left at its declared width). The minimum width is
// max(declared width, header width). This eliminates the trailing "…"
// truncation the bubbles table inflicts on values that overflow the
// declared column width — see renderRow in bubbles/table/table.go,
// which calls ansi.Truncate(value, col.Width, "…") unconditionally.
//
// The marker column (index 0) is intentionally left at its small
// declared width so the visual cursor marker stays flush with the row
// content instead of floating in a wide gutter.
func fitColumnWidths(cols []table.Column, rows []table.Row) []table.Column {
	out := make([]table.Column, len(cols))
	copy(out, cols)
	for i := range out {
		if i == 0 {
			continue
		}
		w := lipgloss.Width(out[i].Title)
		for _, r := range rows {
			if i >= len(r) {
				continue
			}
			if rw := lipgloss.Width(r[i]); rw > w {
				w = rw
			}
		}
		w += columnPadding
		if w > out[i].Width {
			out[i].Width = w
		}
	}
	return out
}

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
//
// Used on cold paths (data load, view switch, mouse click) where the
// previous marker position is unknown so a full sweep is needed. For the
// hot mouse-wheel path use shiftCursorMarker, which only touches the two
// affected rows and avoids the second UpdateViewport that SetRows triggers.
func applyCursorMarker(t *table.Model) {
	rows := t.Rows()
	cur := t.Cursor()
	marked := cursorMarkerCell()
	blank := " "
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

// cursorMarkerCell returns the styled marker glyph for column 0.
func cursorMarkerCell() string {
	return lipgloss.NewStyle().Foreground(selected).Bold(true).Render(cursorMarkerGlyph)
}

// shiftCursorMarker repaints column 0 for a cursor moving from oldIdx to
// newIdx, touching only those two rows. Because t.Rows() returns the
// table's backing slice (not a copy), the in-place writes are visible to
// the next UpdateViewport call — and we deliberately do NOT call SetRows,
// so the caller's own MoveUp/MoveDown supplies the single UpdateViewport
// for this cursor move. The previous applyCursorMarker path ran one
// UpdateViewport on the move and a second on SetRows, doubling
// per-wheel-notch render work.
//
// Safe to call with equal indices or out-of-range values; both cases are
// effectively no-ops.
func shiftCursorMarker(t *table.Model, oldIdx, newIdx int) {
	rows := t.Rows()
	n := len(rows)
	if n == 0 {
		return
	}
	if oldIdx >= 0 && oldIdx < n && len(rows[oldIdx]) > 0 {
		rows[oldIdx][0] = " "
	}
	if newIdx >= 0 && newIdx < n && len(rows[newIdx]) > 0 {
		rows[newIdx][0] = cursorMarkerCell()
	}
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
				promoCellWithAge(s.LastPromo, s.LastPromoAt),
				stageFreightSummary(s.FreightSummary, s.IsControlFlow, m.aliasOf(s.FreightSummary)),
				ageString(s.Created),
				stringOrDash(s.Shard),
			})
			visible = append(visible, s)
		}
		idx := horizontalSlice(len(allStageColumns), m.deploysColOffset)
		slicedCols := decorateColumnsWithSort(sliceColumns(allStageColumns, idx), m.sort[m.view])
		slicedRows := make([]table.Row, len(rows))
		for i, r := range rows {
			slicedRows[i] = sliceRow(r, idx)
		}
		slicedCols = fitColumnWidths(slicedCols, slicedRows)
		// Skip the rebuild entirely when the rendered rows are unchanged.
		// SetRows + SetColumns reset the table's internal viewport offset
		// (no public accessor for YOffset), which causes the visible window
		// to "jump" on every auto-refresh even when nothing the user can see
		// has actually changed. The equality check compares the rendered
		// cells, not the source structs — anything the user sees in the
		// table participates by construction.
		if !sameRows(m.lastDeployRows, slicedRows) ||
			!sameColumnSlice(m.deploysTable.Columns(), slicedCols) {
			// Order matters: clear rows BEFORE changing columns. The bubbles
			// table re-renders on SetColumns, and panics if any row has more
			// cells than the new column count.
			m.deploysTable.SetRows(nil)
			m.deploysTable.SetColumns(slicedCols)
			m.deploysTable.SetRows(slicedRows)
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
		}
		m.visibleDeploys = visible
		m.lastDeployRows = slicedRows
		applyCursorMarker(&m.deploysTable)

	case viewFreights:
		// Same identity-preserving cursor logic as the deploys branch above.
		var prevName string
		if cur := m.freightsTable.Cursor(); cur >= 0 && cur < len(m.visibleFreights) {
			prevName = m.visibleFreights[cur].Name
		}
		controlFlowStages := m.controlFlowStages()
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
				stageSplitCountCell(f.CurrentlyIn, len(f.CurrentlyIn), controlFlowStages),
				stageSplitCountCell(f.VerifiedStages, f.VerifiedIn, controlFlowStages),
				stageSplitCountCell(f.ApprovedStages, f.ApprovedFor, controlFlowStages),
				stringOrDash(f.Warehouse),
			})
			visible = append(visible, f)
		}
		idx := horizontalSlice(len(allFreightColumns), m.freightsColOffset)
		slicedCols := decorateColumnsWithSort(sliceColumns(allFreightColumns, idx), m.sort[m.view])
		slicedRows := make([]table.Row, len(rows))
		for i, r := range rows {
			slicedRows[i] = sliceRow(r, idx)
		}
		slicedCols = fitColumnWidths(slicedCols, slicedRows)
		// See the deploys branch above for why we skip the rebuild on
		// equal-row refreshes — preserves the table's internal scroll.
		if !sameRows(m.lastFreightRows, slicedRows) ||
			!sameColumnSlice(m.freightsTable.Columns(), slicedCols) {
			m.freightsTable.SetRows(nil)
			m.freightsTable.SetColumns(slicedCols)
			m.freightsTable.SetRows(slicedRows)
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
		}
		m.visibleFreights = visible
		m.lastFreightRows = slicedRows
		applyCursorMarker(&m.freightsTable)
	}
}

// sameRows returns true when two row slices would render identically. The
// comparison is over the rendered cells themselves, so every input that
// affects what the user sees (struct fields, time-dependent age strings,
// cross-list lookups like aliasOf) participates without any helper having
// to enumerate them. Don't replace this with a struct-field equality check:
// the previous incarnation silently missed age ticks and freight-alias
// changes for stages because those inputs don't live on the stage struct.
//
// Column 0 is the cursor marker, mutated in place by applyCursorMarker
// after this comparison runs. The stored snapshot aliases the table's
// backing slices, so the marker glyph would otherwise leak into the next
// equality check on the cursor row and force a rebuild every refresh.
// Skip it: the marker carries no user-meaningful data and is reapplied
// unconditionally on every refresh.
//
// A nil snapshot is treated as unequal to any non-nil slice (including an
// empty one). Picker reset paths set lastDeployRows/lastFreightRows to nil
// specifically to invalidate the cache. Without this distinction, a
// refresh that builds zero rows while the new project's data is in flight
// would compare nil to []table.Row{} as equal and leave the previous
// project's rows on screen.
func sameRows(a, b []table.Row) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := 1; j < len(a[i]); j++ {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

// sameColumnSlice returns true when two column slices have the same titles
// and widths in the same order. Width participates because
// fitColumnWidths grows columns to fit the longest visible value; a
// width change alone (no title change) still needs to invalidate the
// rebuild-skip cache so the new column geometry takes effect.
func sameColumnSlice(a, b []table.Column) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Title != b[i].Title || a[i].Width != b[i].Width {
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
