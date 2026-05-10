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
	cur := t.Cursor()
	marked := lipgloss.NewStyle().Foreground(selected).Bold(true).Render("▌")
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

	switch m.view {
	case viewDeploys, viewControlFlow:
		controlOnly := m.view == viewControlFlow
		cursor := m.deploysTable.Cursor()
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
				stageFreightSummary(s.FreightSummary, s.IsControlFlow, m.aliasOf(s.FreightSummary)),
				promoCell(s.LastPromo),
				stringOrDash(s.Shard),
				ageString(s.Created),
			})
			visible = append(visible, s)
		}
		idx := horizontalSlice(len(allStageColumns), m.deploysColOffset)
		slicedRows := make([]table.Row, len(rows))
		for i, r := range rows {
			slicedRows[i] = sliceRow(r, idx)
		}
		// Order matters: clear rows BEFORE changing columns. The bubbles
		// table re-renders on SetColumns, and panics if any row has more
		// cells than the new column count.
		m.deploysTable.SetRows(nil)
		m.deploysTable.SetColumns(sliceColumns(allStageColumns, idx))
		m.deploysTable.SetRows(slicedRows)
		m.visibleDeploys = visible
		clampCursor(&m.deploysTable, cursor)
		applyCursorMarker(&m.deploysTable)

	case viewFreights:
		cursor := m.freightsTable.Cursor()
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
		slicedRows := make([]table.Row, len(rows))
		for i, r := range rows {
			slicedRows[i] = sliceRow(r, idx)
		}
		m.freightsTable.SetRows(nil)
		m.freightsTable.SetColumns(sliceColumns(allFreightColumns, idx))
		m.freightsTable.SetRows(slicedRows)
		m.visibleFreights = visible
		clampCursor(&m.freightsTable, cursor)
		applyCursorMarker(&m.freightsTable)
	}
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
	t.SetCursor(want)
}
