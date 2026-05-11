package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

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
	var t = &m.deploysTable
	if m.view == viewFreights {
		t = &m.freightsTable
	}
	if len(t.Rows()) == 0 {
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

// yankSelection copies the selected resource's identifier to the clipboard
// and posts a transient status message.
func (m *Model) yankSelection() {
	var label, value string
	switch m.view {
	case viewDeploys, viewControlFlow:
		s := m.selectedStage()
		if s == nil {
			return
		}
		label = "stage"
		value = s.Name
	case viewFreights:
		f := m.selectedFreight()
		if f == nil {
			return
		}
		label = "freight"
		value = f.Name
	}
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
			return fmt.Errorf("no clipboard helper found (install wl-copy, xclip, or xsel)")
		}
	default:
		return fmt.Errorf("clipboard not supported on %s", runtime.GOOS)
	}
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

// openArgoCDForSelection launches the user's browser at the Argo CD app page
// for the first ArgoCD app referenced by the selected stage. No-op if there
// is no selection or no discovered Argo CD base URL.
func (m *Model) openArgoCDForSelection() {
	if m.argoBaseURL == "" {
		m.yankedMessage = "no Argo CD URL discovered"
		m.yankedAt = time.Now()
		return
	}
	switch m.view {
	case viewDeploys, viewControlFlow:
		s := m.selectedStage()
		if s == nil || len(s.ArgoCDApps) == 0 {
			m.yankedMessage = "no Argo CD app linked to this stage"
			m.yankedAt = time.Now()
			return
		}
		app := s.ArgoCDApps[0]
		url := argoAppURL(m.argoBaseURL, app)
		if err := openBrowser(url); err != nil {
			m.yankedMessage = fmt.Sprintf("open failed: %v", err)
		} else {
			m.yankedMessage = "opened " + url
		}
		m.yankedAt = time.Now()
	}
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
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}
