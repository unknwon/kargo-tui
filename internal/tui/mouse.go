package tui

import (
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"unknwon.dev/kargo-tui/internal/kargo"
)

// cursorMarkerGlyph is the character applyCursorMarker writes into the
// cursor row's column 0. Hit-testing scans the rendered table for this
// glyph to locate the cursor row's screen position.
const cursorMarkerGlyph = "▌"

// handleMouseClick dispatches a mouse click. Left-click selects the row /
// node under the cursor; right-click opens a context menu of actions
// applicable to the clicked target. Clicks outside any selectable region
// are ignored except that any left-click also dismisses an open context
// menu (with the exception of left-click ON a menu row, which picks it).
func (m *Model) handleMouseClick(msg tea.MouseClickMsg) (Model, tea.Cmd) {
	if m.menuOpen {
		if msg.Button == tea.MouseLeft {
			if idx := m.menuHitTest(msg.X, msg.Y); idx >= 0 {
				return m.invokeMenuItem(idx)
			}
			m.closeMenu()
			return *m, nil
		}
		if msg.Button == tea.MouseRight {
			m.closeMenu()
			return *m, nil
		}
		return *m, nil
	}

	// Pickers, overlays, help, and the panic popup own their own input.
	// Clicks while filtering also fall through to keep the filter input
	// in charge of its caret.
	if m.phase != phaseRunning ||
		m.showHelp ||
		m.overlay != overlayNone ||
		m.panicMessage != "" ||
		m.filtering ||
		m.detailsOnly {
		return *m, nil
	}

	switch msg.Button {
	case tea.MouseLeft:
		return m.handleLeftClick(msg.X, msg.Y)
	case tea.MouseRight:
		return m.handleRightClick(msg.X, msg.Y)
	}
	return *m, nil
}

// handleLeftClick selects whatever the click landed on: a table row in
// list views, or a node in the graph view. Tree view is not handled yet
// because its rendered Y → node mapping requires its own tracker.
func (m *Model) handleLeftClick(x, y int) (Model, tea.Cmd) {
	switch {
	case m.view == viewGraph:
		if idx, ok := m.hitTestGraphNode(x, y); ok {
			m.graphCursor = idx
			m.resetPanelScroll()
			m.refreshPanel()
		}
	case m.activeTable() != nil:
		if row, ok := m.hitTestTableRow(y); ok {
			m.setTableCursor(row)
		}
	}
	return *m, nil
}

// handleRightClick opens a context menu of actions applicable to the
// clicked target. The right-click also selects the target so any
// subsequent same-target action (keyboard or menu) lines up with what
// the user clicked.
func (m *Model) handleRightClick(x, y int) (Model, tea.Cmd) {
	switch {
	case m.view == viewGraph:
		idx, ok := m.hitTestGraphNode(x, y)
		if !ok {
			return *m, nil
		}
		m.graphCursor = idx
		m.refreshPanel()
		if s := m.selectedGraphStage(); s != nil {
			m.openMenuForStage(x, y, s)
		}
	case m.activeTable() != nil:
		row, ok := m.hitTestTableRow(y)
		if !ok {
			return *m, nil
		}
		m.setTableCursor(row)
		switch m.view {
		case viewDeploys, viewControlFlow:
			if s := m.selectedStage(); s != nil {
				m.openMenuForStage(x, y, s)
			}
		case viewFreights:
			if f := m.selectedFreight(); f != nil {
				m.openMenuForFreight(x, y, f)
			}
		}
	}
	return *m, nil
}

// hitTestTableRow maps a screen Y to a 0-based row index within the
// active table's rows slice. Returns ok=false when y is outside the
// table body (header bar, filter line, help line, etc.).
//
// The bubbles table doesn't expose its internal viewport offset, so we
// locate the cursor's drawn row in the rendered table output (it carries
// the cursor marker glyph applyCursorMarker writes into column 0) and
// translate the click's delta from that screen row back into a row
// index.
func (m *Model) hitTestTableRow(y int) (int, bool) {
	t := m.activeTable()
	if t == nil {
		return 0, false
	}
	const headerRows = 2 // app header + table column header
	if y < headerRows {
		return 0, false
	}
	visibleRow := y - headerRows
	if visibleRow >= t.Height() {
		return 0, false
	}
	cursorScreen, ok := tableCursorScreenRow(t)
	if !ok {
		return 0, false
	}
	idx := t.Cursor() + (visibleRow - cursorScreen)
	rows := len(t.Rows())
	if idx < 0 || idx >= rows {
		return 0, false
	}
	return idx, true
}

// tableCursorScreenRow returns the cursor row's Y within the table's
// body (0-based, excluding the column-header row). Returns ok=false when
// the cursor marker can't be located in the rendered output, which
// happens when there are no rows.
func tableCursorScreenRow(t *table.Model) (int, bool) {
	if len(t.Rows()) == 0 {
		return 0, false
	}
	rendered := t.View()
	lines := strings.Split(rendered, "\n")
	// The first line is the table's column header; body lines follow.
	for i := 1; i < len(lines); i++ {
		if strings.Contains(lines[i], cursorMarkerGlyph) {
			return i - 1, true
		}
	}
	return 0, false
}

// hitTestGraphNode maps a screen click to a graph node index. The graph
// pans so the cursor stays visible; we replicate the same pan math here
// so a click on a visible node resolves to its node index.
func (m *Model) hitTestGraphNode(x, y int) (int, bool) {
	g := m.graphLayout
	if len(g.nodes) == 0 {
		return 0, false
	}
	// graphView's row order is: header, body, [banner/error/yank],
	// [searchLine], statusLine, hint. The header is one line, so the
	// body's top is at screen Y = 1; shift the click into body
	// coordinates before applying the renderer's pan offset.
	bodyY := y - 1
	bodyW, bodyH := m.graphBodyDims()
	if bodyY < 0 || bodyY >= bodyH {
		return 0, false
	}
	// Body is rendered with Padding(0, 1) so the canvas starts at screen
	// X = 1 and extends bodyW cells right.
	bodyX := x - 1
	if bodyX < 0 || bodyX >= bodyW {
		return 0, false
	}
	cursorIdx := -1
	if m.graphCursor >= 0 && m.graphCursor < len(g.nodes) {
		cursorIdx = m.graphCursor
	}
	x0, y0 := graphPanOffsetFor(g, cursorIdx, bodyW, bodyH)
	cx := bodyX + x0
	cy := bodyY + y0
	for i, n := range g.nodes {
		if n.Dummy {
			continue
		}
		if cx >= n.X && cx < n.X+n.W && cy >= n.Y && cy < n.Y+n.H {
			return i, true
		}
	}
	return 0, false
}

// graphBodyDims returns the body width/height passed to renderGraph,
// mirroring the reservation math in graphView so the click hit-tester
// agrees with the renderer on the pan offset.
func (m Model) graphBodyDims() (int, int) {
	reserved := 3
	if m.authExpired || m.deploysError != nil || m.yankedMessage != "" {
		reserved++
	}
	// searchLine only consumes a row when filtering or there are
	// matches with a non-blank query; mirrors the same conditions as
	// graphView (which trims whitespace before comparing).
	if (m.filtering && m.view == viewGraph) ||
		(len(m.graphSearchMatches) > 0 && strings.TrimSpace(m.filter.Value()) != "") {
		reserved++
	}
	w := m.width - 2
	if w < 20 {
		w = 20
	}
	h := m.height - reserved
	if h < 5 {
		h = 5
	}
	return w, h
}

// openMenuForStage anchors a right-click context menu at (x, y) with the
// actions applicable to a stage.
func (m *Model) openMenuForStage(x, y int, s *kargo.Stage) {
	stage := *s
	items := []menuItem{
		{label: "Details", action: func(m *Model) tea.Cmd {
			m.detailsOnly = true
			return nil
		}},
		{label: "Logs", action: func(m *Model) tea.Cmd {
			m.openLogsOverlay(stage.Name)
			return loadLogsCmd(m.client, m.project, stage.Name)
		}},
		{label: "Diff", action: func(m *Model) tea.Cmd {
			m.openDiffOverlayForStage(&stage)
			return nil
		}},
		{label: "Promote", action: func(m *Model) tea.Cmd {
			m.openPromoteOverlay(&stage)
			return nil
		}},
		{label: "Promote downstream", action: func(m *Model) tea.Cmd {
			m.openPromoteDownstreamOverlay(&stage)
			return nil
		}},
	}
	if !stage.IsControlFlow {
		items = append(items, menuItem{label: "Open in Argo CD", action: func(m *Model) tea.Cmd {
			m.openArgoCDForStage(&stage)
			return nil
		}})
	}
	items = append(items, menuItem{label: "Yank name", action: func(m *Model) tea.Cmd {
		m.yankStage(&stage)
		return nil
	}})
	m.openMenu(x, y, items)
}

// openMenuForFreight anchors a right-click context menu at (x, y) with
// the actions applicable to a freight.
func (m *Model) openMenuForFreight(x, y int, f *kargo.Freight) {
	freight := *f
	items := []menuItem{
		{label: "Details", action: func(m *Model) tea.Cmd {
			m.detailsOnly = true
			return nil
		}},
		{label: "Yank name", action: func(m *Model) tea.Cmd {
			m.yankValue("freight", freight.Name)
			return nil
		}},
	}
	m.openMenu(x, y, items)
}

// activeMouseMode returns the mouse capture mode to install on the
// current view. Defaults to cell-motion (click + drag only); upgrades
// to all-motion while the right-click context menu is open so hover
// events fire and the menu highlight can track the cursor.
func (m Model) activeMouseMode() tea.MouseMode {
	if m.menuOpen {
		return tea.MouseModeAllMotion
	}
	return tea.MouseModeCellMotion
}

func (m *Model) openMenu(x, y int, items []menuItem) {
	m.menuOpen = true
	m.menuX = x
	m.menuY = y
	m.menuItems = items
	m.menuCursor = 0
}

func (m *Model) closeMenu() {
	m.menuOpen = false
	m.menuItems = nil
	m.menuCursor = 0
}

// menuHitTest returns the menu item index at screen (x, y), or -1 when
// the click lands outside the menu's inner content area. The 1-cell
// border on every side is treated as non-clickable so users can't pick
// a row by clicking its top/bottom/left/right frame.
func (m *Model) menuHitTest(x, y int) int {
	if !m.menuOpen || len(m.menuItems) == 0 {
		return -1
	}
	mx, my, mw, mh := m.menuBounds()
	if x <= mx || x >= mx+mw-1 || y <= my || y >= my+mh-1 {
		return -1
	}
	row := y - my - 1
	if row < 0 || row >= len(m.menuItems) {
		return -1
	}
	return row
}

// menuBounds returns the menu's screen rectangle as (x, y, w, h). Width
// fits the longest label (measured in display cells) + 2 padding + 2
// border; height = items + 2 borders. The rectangle is clamped to the
// terminal so the menu never extends past either edge, shrinking the
// width if a label would not fit at all.
func (m Model) menuBounds() (int, int, int, int) {
	maxLabel := 0
	for _, it := range m.menuItems {
		if n := lipgloss.Width(it.label); n > maxLabel {
			maxLabel = n
		}
	}
	w := maxLabel + 4
	h := len(m.menuItems) + 2
	if w > m.width {
		w = m.width
	}
	if h > m.height {
		h = m.height
	}
	x := m.menuX
	y := m.menuY
	if x+w > m.width {
		x = m.width - w
	}
	if y+h > m.height {
		y = m.height - h
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y, w, h
}

// composeWithMenu overlays the right-click context menu (if open) onto
// the base view content by splicing the menu's lines into the base
// view's lines at the menu's anchor. Avoids lipgloss canvas compositing
// because base content here is already a fully-styled multi-line string
// with backgrounds and the canvas path was rendering an empty screen.
func (m Model) composeWithMenu(base string) string {
	if !m.menuOpen || len(m.menuItems) == 0 || m.width <= 0 || m.height <= 0 {
		return base
	}
	x, y, _, _ := m.menuBounds()
	menu := m.renderMenu()
	return spliceOverlay(base, menu, x, y)
}

// spliceOverlay stamps overlay onto base at (col, row), one line at a
// time. Both inputs may contain ANSI escape sequences; each overlay
// line is written verbatim, and stampLine replaces only the cells the
// overlay's display width occupies in the matching base line.
func spliceOverlay(base, overlay string, col, row int) string {
	if overlay == "" {
		return base
	}
	baseLines := strings.Split(base, "\n")
	overlayLines := strings.Split(overlay, "\n")
	for i, ol := range overlayLines {
		r := row + i
		if r < 0 || r >= len(baseLines) {
			continue
		}
		baseLines[r] = stampLine(baseLines[r], ol, col)
	}
	return strings.Join(baseLines, "\n")
}

// stampLine replaces the cells at [col, col+width(overlay)) in base
// with overlay's cells, keeping ANSI escapes intact on both sides.
// Uses ansi.Cut for width-aware substring extraction so styled bases
// stay styled in the surviving fragments.
func stampLine(base, overlay string, col int) string {
	baseW := lipgloss.Width(base)
	overlayW := lipgloss.Width(overlay)
	var left string
	switch {
	case col <= 0:
		left = ""
	case col >= baseW:
		left = base + strings.Repeat(" ", col-baseW)
	default:
		left = ansi.Cut(base, 0, col)
	}
	var right string
	end := col + overlayW
	if end < baseW {
		right = ansi.Cut(base, end, baseW)
	}
	return left + overlay + right
}

// renderMenu draws the floating menu box as a styled string with a
// border, one row per menu item, highlighting the keyboard cursor row.
func (m Model) renderMenu() string {
	width := 0
	for _, it := range m.menuItems {
		if n := lipgloss.Width(it.label); n > width {
			width = n
		}
	}
	rowStyle := lipgloss.NewStyle().
		Foreground(normal).Background(bg).Padding(0, 1).Width(width + 2)
	cursorStyle := rowStyle.Background(selected).Foreground(darkFg).Bold(true)

	var rows []string
	for i, it := range m.menuItems {
		style := rowStyle
		if i == m.menuCursor {
			style = cursorStyle
		}
		rows = append(rows, style.Render(it.label))
	}
	body := strings.Join(rows, "\n")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(muted).
		Background(bg).
		Render(body)
}

// invokeMenuItem runs the action stored on menu item idx and closes the
// menu. Returns the command (if any) the action yields so the runtime
// can dispatch it.
func (m *Model) invokeMenuItem(idx int) (Model, tea.Cmd) {
	if idx < 0 || idx >= len(m.menuItems) {
		m.closeMenu()
		return *m, nil
	}
	item := m.menuItems[idx]
	m.closeMenu()
	if item.action == nil {
		return *m, nil
	}
	cmd := item.action(m)
	return *m, cmd
}
