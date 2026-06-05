package tui

import (
	"fmt"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/viewport"
	"github.com/cockroachdb/errors"

	"unknwon.dev/kargo-tui/internal/kargo"
)

// setView swaps the active view, persisting the outgoing filter so each list
// keeps its own search state, and restoring the incoming view's stored
// filter. Also resyncs panels and rows.
func (m *Model) setView(v view) {
	m.filterValues[m.view] = m.filter.Value()
	m.view = v
	m.detailsOnly = false
	m.filter.SetValue(m.filterValues[v])
	m.refreshRows()
	if v == viewGraph {
		// Graph view doesn't filter rows from m.filter; instead the
		// saved query rehydrates the search match list so n/N still
		// step through results after a view switch. An empty query
		// clears any leftover matches from a prior session.
		m.recomputeGraphMatches(m.filter.Value())
	} else {
		// Leaving graph view: drop the cached match list so future
		// graph-view sessions start clean unless the user re-opens the
		// search.
		m.graphSearchMatches = nil
		m.graphSearchPos = 0
		m.graphSearchActive = false
	}
	m.resetPanelScroll()
	m.refreshPanel()
}

// moveCursor advances the active table's cursor by delta rows. It uses the
// bubbles table's MoveUp/MoveDown rather than SetCursor because those also
// adjust the viewport's Y-offset — without them the cursor can scroll past
// the bottom of the visible window when driven by the mouse wheel.
func (m *Model) moveCursor(delta int) {
	t := m.activeTable()
	if t == nil || len(t.Rows()) == 0 {
		return
	}
	switch {
	case delta < 0:
		t.MoveUp(-delta)
	case delta > 0:
		t.MoveDown(delta)
	}
	applyCursorMarker(t)
	m.resetPanelScroll()
	m.refreshPanel()
}

// setTableCursorAtScreen moves the cursor to row idx and pins the viewport
// so the row stays at screenRow within the table body. Mouse clicks use
// this so the clicked row keeps its on-screen position, instead of relying
// on the bubbles table's MoveUp heuristic, which clamps the new YOffset
// against the pre-move content size and silently jumps when the move would
// grow the rendered window.
func (m *Model) setTableCursorAtScreen(idx, screenRow int) {
	t := m.activeTable()
	if t == nil {
		return
	}
	rows := len(t.Rows())
	if rows == 0 {
		return
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= rows {
		idx = rows - 1
	}
	t.SetCursor(idx)
	yOffset := idx - screenRow
	if maxOffset := rows - t.Height(); yOffset > maxOffset {
		yOffset = maxOffset
	}
	if yOffset < 0 {
		yOffset = 0
	}
	if vp := tableViewport(t); vp != nil {
		vp.SetYOffset(yOffset)
	}
	applyCursorMarker(t)
	m.resetPanelScroll()
	m.refreshPanel()
}

// tableViewport returns a pointer to the bubbles table's embedded viewport.
// The field is unexported, so we reach it through reflection. Used by
// click handling to pin YOffset directly, which the table's MoveUp path
// cannot do reliably (it clamps against stale content; see
// setTableCursorAtScreen).
//
// The field name and type are verified at startup by init() so an upstream
// rename fails loudly instead of silently falling back to the buggy click
// path.
func tableViewport(t *table.Model) *viewport.Model {
	v := reflect.ValueOf(t).Elem().FieldByName(tableViewportFieldName)
	return (*viewport.Model)(unsafe.Pointer(v.UnsafeAddr()))
}

const tableViewportFieldName = "viewport"

func init() {
	f, ok := reflect.TypeFor[table.Model]().FieldByName(tableViewportFieldName)
	if !ok {
		panic("tui: bubbles table.Model no longer has a `viewport` field. Update tableViewport.")
	}
	if f.Type != reflect.TypeFor[viewport.Model]() {
		panic("tui: bubbles table.Model.viewport is no longer a viewport.Model. Update tableViewport.")
	}
}

// activeTable returns a pointer to the table backing the current list
// view, or nil for non-table views.
func (m *Model) activeTable() *table.Model {
	switch m.view {
	case viewDeploys, viewControlFlow:
		return &m.deploysTable
	case viewFreights:
		return &m.freightsTable
	}
	return nil
}

// yankSelection copies the selected resource's identifier to the clipboard
// and posts a transient status message.
func (m *Model) yankSelection() {
	switch m.view {
	case viewDeploys, viewControlFlow, viewTree, viewGraph:
		// selectedStage routes to the per-view picker (table / tree /
		// graph), so this branch covers every stage-centric view,
		// matching what the per-view hint lines advertise.
		m.yankStage(m.selectedStage())
	case viewFreights:
		f := m.selectedFreight()
		if f == nil {
			return
		}
		m.yankValue("freight", f.Name)
	}
}

// yankStage is the stage-scoped variant used by the right-click context menu
// so the action always targets the clicked stage even if selection shifts
// before the menu item fires.
func (m *Model) yankStage(s *kargo.Stage) {
	if s == nil {
		return
	}
	m.yankValue("stage", s.Name)
}

func (m *Model) yankValue(label, value string) {
	if value == "" {
		return
	}
	if err := writeClipboard(value); err != nil {
		m.yankedMessage = fmt.Sprintf("yank failed: %v", err)
	} else {
		m.yankedMessage = fmt.Sprintf("yanked %s %s", label, value)
	}
	m.yankedAt = time.Now()
}

// writeClipboard pipes s into the platform's clipboard utility. Best-effort —
// returns an error if no helper is available.
func writeClipboard(s string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		if _, err := exec.LookPath("wl-copy"); err == nil {
			cmd = exec.Command("wl-copy")
		} else if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		} else if _, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--clipboard", "--input")
		} else {
			return errors.New("no clipboard helper found (install wl-copy, xclip, or xsel)")
		}
	default:
		return errors.Newf("clipboard not supported on %s", runtime.GOOS)
	}
	cmd.Stdin = strings.NewReader(s)
	if err := cmd.Run(); err != nil {
		return errors.Wrap(err, "run clipboard helper")
	}
	return nil
}

// openArgoCDForSelection launches the user's browser at the Argo CD app page
// for the first ArgoCD app referenced by the selected stage. No-op if there
// is no selection or no discovered Argo CD base URL.
func (m *Model) openArgoCDForSelection() {
	switch m.view {
	case viewDeploys, viewControlFlow, viewTree, viewGraph:
		m.openArgoCDForStage(m.selectedStage())
	}
}

// openArgoCDForStage is the stage-scoped variant used by the right-click
// context menu so the action always targets the clicked stage even if
// selection shifts before the menu item fires.
func (m *Model) openArgoCDForStage(s *kargo.Stage) {
	if s == nil || len(s.ArgoCDApps) == 0 {
		m.yankedMessage = "no Argo CD app linked to this stage"
		m.yankedAt = time.Now()
		return
	}
	app := s.ArgoCDApps[0]
	shardKey := s.Labels[kargo.ShardLabelKey]
	base := m.argoShards.BaseURLFor(shardKey)
	if base == "" {
		m.yankedMessage = "no Argo CD shard URL for " + app.Namespace + "/" + app.Name
		m.yankedAt = time.Now()
		return
	}
	url := argoAppURL(base, app)
	if err := openBrowser(url); err != nil {
		m.yankedMessage = fmt.Sprintf("open failed: %v", err)
	} else {
		m.yankedMessage = "opened " + url
	}
	m.yankedAt = time.Now()
}

func argoAppURL(base string, app kargo.ArgoCDAppRef) string {
	base = strings.TrimRight(base, "/")
	return fmt.Sprintf("%s/applications/%s/%s", base, app.Namespace, app.Name)
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return errors.Newf("unsupported platform: %s", runtime.GOOS)
	}
	if err := cmd.Start(); err != nil {
		return errors.Wrap(err, "start browser")
	}
	return nil
}
