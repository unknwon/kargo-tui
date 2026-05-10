package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"unknwon.dev/kargo-tui/internal/kargo"
)

// promoteStep tracks where the promote overlay is in its little flow.
type promoteStep int

const (
	promotePicking promoteStep = iota
	promoteViewing
	promoteConfirming
	promoteSubmitting
	promoteDone
)

// promotePickerPage returns the number of rows pgup/pgdown moves the
// picker cursor — the visible body height minus one row of overlap so
// the user keeps a row of context across page jumps. Body height
// mirrors promotePickingView (m.height - 2 for the header + hint
// chrome), so the page jump is (m.height - 2) - 1 = m.height - 3.
// Clamped to at least one row so a pathologically tiny terminal
// doesn't return zero and freeze pgup/pgdown.
func promotePickerPage(termHeight int) int {
	n := termHeight - 3
	if n < 1 {
		n = 1
	}
	return n
}

// openPromoteOverlay seeds promote state for the given stage and computes
// the freight pickable for that stage's warehouse.
func (m *Model) openPromoteOverlay(stage *kargo.Stage) {
	if stage == nil {
		return
	}
	if stage.IsControlFlow {
		m.yankedMessage = "control-flow stages cannot be promoted to directly"
		m.yankedAt = time.Now()
		return
	}
	m.overlay = overlayPromote
	m.overlayTitle = "Promote · " + stage.Name
	m.promoteStage = stage.Name
	m.promoteStep = promotePicking
	m.promoteCursor = 0
	m.promoteResult = ""
	m.promoteError = nil
	m.promoteCandidates = candidateFreight(m.freights, stage)
	// Empty candidates → the picker renders "no candidate freight found
	// for this stage" with esc-to-dismiss. We deliberately don't fall
	// back to the full freight list because Kargo will reject any
	// promotion of freight that isn't verified upstream of the target.
}

// candidateFreight returns the freight a user might reasonably promote to
// stage, sorted newest-first. We include any freight whose VerifiedStages
// contains an upstream of the target stage, plus any freight whose
// ApprovedStages explicitly lists this stage.
func candidateFreight(all []kargo.Freight, target *kargo.Stage) []kargo.Freight {
	if target == nil {
		return nil
	}
	upstream := make(map[string]struct{}, len(target.Upstreams))
	for _, u := range target.Upstreams {
		upstream[u] = struct{}{}
	}
	var out []kargo.Freight
	for _, f := range all {
		match := false
		for _, vs := range f.VerifiedStages {
			if _, ok := upstream[vs]; ok {
				match = true
				break
			}
		}
		if !match {
			for _, as := range f.ApprovedStages {
				if as == target.Name {
					match = true
					break
				}
			}
		}
		if match {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// promoteOverlayView renders the freight-picker / confirm / result frames.
func (m Model) promoteOverlayView() tea.View {
	titleStyle := lipgloss.NewStyle().Foreground(normal).Bold(true).Background(bg)
	hintStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	itemStyle := lipgloss.NewStyle().Foreground(normal).Background(bg)
	selStyle := lipgloss.NewStyle().Foreground(bg).Background(selected).Bold(true)
	errStyle := lipgloss.NewStyle().Foreground(degraded).Background(bg)
	okStyle := lipgloss.NewStyle().Foreground(healthy).Background(bg)

	innerW := popupInnerWidth(m.width)

	// The picking and viewing steps run full-screen so a long candidate
	// list (or detail body) can't push content past the terminal edge.
	// Confirm / submitting / done remain in the compact popup frame
	// below — they're 1–3 lines and the popup looks better there.
	if m.promoteStep == promoteViewing {
		return m.promoteViewingView(titleStyle, hintStyle)
	}
	if m.promoteStep == promotePicking {
		return m.promotePickingView(titleStyle, hintStyle, itemStyle, selStyle)
	}

	var lines []string
	lines = append(lines, titleStyle.Render(m.overlayTitle))
	lines = append(lines, "")

	switch m.promoteStep {
	case promoteConfirming:
		f := m.promoteCandidates[m.promoteCursor]
		lines = append(lines,
			itemStyle.Render(fmt.Sprintf("Promote freight %s%s to stage %s?",
				shortFreight(f.Name), aliasSuffix(f.Alias), m.promoteStage)),
			"",
			hintStyle.Render("y confirm · n/esc cancel"),
		)

	case promoteSubmitting:
		lines = append(lines, hintStyle.Render("submitting promotion…"))

	case promoteDone:
		if m.promoteError != nil {
			lines = append(lines, errStyle.Render("error: "+m.promoteError.Error()))
		} else {
			lines = append(lines, okStyle.Render("promoted: "+m.promoteResult))
		}
		lines = append(lines, "", hintStyle.Render("enter/esc dismiss"))
	}

	body := strings.Join(lines, "\n")
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(muted).
		Background(bg).
		Padding(1, 2).
		Width(innerW).
		Render(body)

	v := tea.NewView(box)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// promotePickingView renders the freight-candidate picker as a
// full-screen frame: 1-row header, N-row body, 1-row hint. Because the
// chrome is a fixed 2 rows the body height is unambiguous (`m.height -
// 2`) and the cursor windowing always keeps the highlighted row visible.
func (m Model) promotePickingView(titleStyle, hintStyle, itemStyle, selStyle lipgloss.Style) tea.View {
	w := m.width
	if w <= 0 {
		w = 80
	}
	// Each row uses Padding(0, 1) (2 horizontal cells) and the marker
	// occupies 2 leading cells inside the padded area. Clip the label
	// to (terminal width - 2 padding - 2 marker) so a wide freight
	// name + warehouse + age can never wrap to a second terminal row.
	// Clamp to >= 1 rather than a comfortable minimum: on very narrow
	// terminals a floor above (w - 4) would itself cause wrap.
	rowBudget := w - 4
	if rowBudget < 1 {
		rowBudget = 1
	}
	// Padding(0, 1) on header/hint claims 2 horizontal cells, so the
	// text budget is w-2. Clamp to >= 1 because clipToWidth treats
	// width<=0 as "don't clip", which would re-introduce wrap.
	headHintBudget := w - 2
	if headHintBudget < 1 {
		headHintBudget = 1
	}

	header := titleStyle.Padding(0, 1).Render(clipToWidth(m.overlayTitle, headHintBudget))
	hint := hintStyle.Padding(0, 1).Render(clipToWidth("↑/↓ select · pgup/pgdn/space/home/end jump · enter pick · v details · esc cancel", headHintBudget))

	bodyH := m.height - 2
	if bodyH < 1 {
		bodyH = 1
	}

	var body string
	if len(m.promoteCandidates) == 0 {
		body = hintStyle.Padding(0, 1).Render("no candidate freight found for this stage")
	} else {
		// Window the candidate slice so the cursor row stays inside
		// the visible body.
		start := 0
		if m.promoteCursor >= bodyH {
			start = m.promoteCursor - bodyH + 1
		}
		end := start + bodyH
		if end > len(m.promoteCandidates) {
			end = len(m.promoteCandidates)
		}
		rows := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			f := m.promoteCandidates[i]
			label := shortFreight(f.Name) + aliasSuffix(f.Alias)
			meta := []string{}
			if f.Warehouse != "" {
				meta = append(meta, "wh="+f.Warehouse)
			}
			if !f.Created.IsZero() {
				meta = append(meta, ageString(f.Created)+" old")
			}
			if len(meta) > 0 {
				label += "  " + strings.Join(meta, " · ")
			}
			label = clipToWidth(label, rowBudget)
			marker := "  "
			if i == m.promoteCursor {
				marker = "▌ "
			}
			row := marker + label
			if i == m.promoteCursor {
				rows = append(rows, selStyle.Padding(0, 1).Render(row))
			} else {
				rows = append(rows, itemStyle.Padding(0, 1).Render(row))
			}
		}
		body = strings.Join(rows, "\n")
	}

	content := lipgloss.JoinVertical(lipgloss.Left, header, body, hint)
	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// promoteViewingChromeRows counts the non-viewport rows the viewing
// box renders: top border (1) + top padding (1) + header (1) + blank
// (1) + blank-after-viewport (1) + hint (1) + bottom padding (1) +
// bottom border (1).
const promoteViewingChromeRows = 8

// promoteViewingChromeCols counts the non-content columns the viewing
// box renders: left border (1) + left padding (2) + right padding (2)
// + right border (1).
const promoteViewingChromeCols = 6

// preparePromoteViewingViewport sizes overlayVP and loads the highlighted
// candidate's detail block. Called from two places:
//
//   - The key handler (updatePromoteOverlay), via a pointer receiver, so
//     the resized/loaded viewport state persists into the real model and
//     subsequent ScrollUp/ScrollDown have a non-zero maxYOffset to clamp
//     against. Required for scroll keys to do anything.
//   - The View path (promoteViewingView), where it's harmless: View()
//     runs on a value-copy and any state set here is thrown away after
//     rendering, but rendering does need width/height/content set first.
//
// Both callers must invoke this — the handler call drives scroll, the
// View call drives display.
func (m *Model) preparePromoteViewingViewport() {
	w := m.width
	if w <= 0 {
		w = 80
	}
	innerW := w - promoteViewingChromeCols
	if innerW < 1 {
		innerW = 1
	}
	innerH := m.height - promoteViewingChromeRows
	if innerH < 1 {
		innerH = 1
	}
	m.overlayVP.SetWidth(innerW)
	m.overlayVP.SetHeight(innerH)
	m.overlayVP.SetContent(m.promoteViewingContent(innerW))
}

// promoteViewingView renders the highlighted candidate's full freight
// detail inside the shared overlayVP viewport so long detail bodies are
// scrollable (j/k, pgup/pgdn, home/end).
func (m Model) promoteViewingView(titleStyle, hintStyle lipgloss.Style) tea.View {
	m.preparePromoteViewingViewport()
	w := m.width
	if w <= 0 {
		w = 80
	}
	innerW := w - promoteViewingChromeCols
	if innerW < 1 {
		innerW = 1
	}
	// Clip the title and hint so a narrow terminal can't wrap them —
	// any extra row would push the viewport content past the bottom
	// of the screen and hide the hint.
	header := titleStyle.Render(clipToWidth(m.overlayTitle, innerW))
	hint := hintStyle.Render(clipToWidth("v/esc back · enter promote · j/k/↑/↓ scroll · pgup/pgdn/space page · home/end top/bottom", innerW))
	body := lipgloss.JoinVertical(lipgloss.Left, header, "", m.overlayVP.View(), "", hint)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(muted).
		Background(bg).
		Padding(1, 2).
		Render(body)
	v := tea.NewView(box)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// promoteViewingContent builds the scrollable detail body for the
// highlighted candidate. Mirrors the freight-detail render used in the
// side panel.
func (m Model) promoteViewingContent(innerW int) string {
	if m.promoteCursor < 0 || m.promoteCursor >= len(m.promoteCandidates) {
		return ""
	}
	f := m.promoteCandidates[m.promoteCursor]
	titleStyle := lipgloss.NewStyle().Foreground(normal).Bold(true).Background(bg)
	keyStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	valStyle := lipgloss.NewStyle().Foreground(normal).Background(bg)
	row := func(k, v string) string {
		return keyStyle.Render(padRight(k+":", 12)) + " " + valStyle.Render(wrap(v, innerW-13))
	}
	var lines []string
	lines = append(lines, titleStyle.Render(f.Name))
	if f.Alias != "" {
		lines = append(lines, keyStyle.Render(f.Alias))
	}
	lines = append(lines, freightDetailLines(f, innerW, row, keyStyle, valStyle)...)
	return strings.Join(lines, "\n")
}

// updatePromoteOverlay routes a key press while the promote overlay is open.
// Each promoteStep accepts a different key set: pick (arrows + enter +
// v to view), view (scroll keys + enter to advance to confirm + v/esc
// to return), confirm (y/n), submitting (esc to dismiss without
// cancelling the in-flight RPC), done (enter/esc to match the hint).
func (m Model) updatePromoteOverlay(key string) (tea.Model, tea.Cmd) {
	switch m.promoteStep {
	case promotePicking:
		switch key {
		case "esc", "q":
			m.overlay = overlayNone
			return m, nil
		case "up", "k":
			if m.promoteCursor > 0 {
				m.promoteCursor--
			}
			return m, nil
		case "down", "j":
			if m.promoteCursor < len(m.promoteCandidates)-1 {
				m.promoteCursor++
			}
			return m, nil
		case "pgup":
			m.promoteCursor -= promotePickerPage(m.height)
			if m.promoteCursor < 0 {
				m.promoteCursor = 0
			}
			return m, nil
		case "pgdown", "pgdn", " ":
			if len(m.promoteCandidates) == 0 {
				return m, nil
			}
			m.promoteCursor += promotePickerPage(m.height)
			if m.promoteCursor > len(m.promoteCandidates)-1 {
				m.promoteCursor = len(m.promoteCandidates) - 1
			}
			return m, nil
		case "home":
			m.promoteCursor = 0
			return m, nil
		case "end":
			if len(m.promoteCandidates) == 0 {
				return m, nil
			}
			m.promoteCursor = len(m.promoteCandidates) - 1
			return m, nil
		case "enter":
			if len(m.promoteCandidates) == 0 {
				return m, nil
			}
			m.promoteStep = promoteConfirming
			return m, nil
		case "v":
			if len(m.promoteCandidates) == 0 {
				return m, nil
			}
			m.promoteStep = promoteViewing
			m.preparePromoteViewingViewport()
			m.overlayVP.GotoTop()
			return m, nil
		}
	case promoteViewing:
		m.preparePromoteViewingViewport()
		switch key {
		case "v", "esc", "q":
			m.promoteStep = promotePicking
			return m, nil
		case "enter":
			m.promoteStep = promoteConfirming
			return m, nil
		case "up", "k":
			m.overlayVP.ScrollUp(1)
			return m, nil
		case "down", "j":
			m.overlayVP.ScrollDown(1)
			return m, nil
		case "pgup":
			m.overlayVP.PageUp()
			return m, nil
		case "pgdown", "pgdn", " ":
			m.overlayVP.PageDown()
			return m, nil
		case "home":
			m.overlayVP.GotoTop()
			return m, nil
		case "end":
			m.overlayVP.GotoBottom()
			return m, nil
		}
	case promoteConfirming:
		switch key {
		case "y", "Y":
			f := m.promoteCandidates[m.promoteCursor]
			m.promoteStep = promoteSubmitting
			return m, promoteCmd(m.client, m.project, m.promoteStage, f.Name)
		case "n", "N", "esc":
			m.promoteStep = promotePicking
			return m, nil
		}
	case promoteSubmitting:
		if key == "esc" {
			// User dismissed while submitting; the goroutine still posts
			// a promoteResultMsg which the global handler converts to a
			// status line.
			m.overlay = overlayNone
			return m, nil
		}
	case promoteDone:
		switch key {
		case "enter", "esc":
			m.overlay = overlayNone
			return m, nil
		}
	}
	return m, nil
}

// promoteCmd dispatches a PromoteToStage call as a tea.Cmd.
func promoteCmd(c *kargo.Client, project, stage, freight string) tea.Cmd {
	return func() tea.Msg {
		entry, err := c.PromoteToStage(context.Background(), project, stage, freight)
		msg := promoteResultMsg{stage: stage, freight: freight, err: err}
		if entry != nil {
			msg.promotionName = entry.Name
			msg.phase = entry.Phase
		}
		return msg
	}
}

// promoteDownstreamCmd dispatches a PromoteDownstream call as a tea.Cmd.
func promoteDownstreamCmd(c *kargo.Client, project, source, freight string) tea.Cmd {
	return func() tea.Msg {
		entries, err := c.PromoteDownstream(context.Background(), project, source, freight)
		return promoteDownstreamResultMsg{
			source:     source,
			freight:    freight,
			promotions: len(entries),
			err:        err,
		}
	}
}
