package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"unknwon.dev/kargo-tui/internal/kargo"
)

// promoteStep tracks where the promote overlay is in its little flow.
type promoteStep int

const (
	promotePicking promoteStep = iota
	promoteViewing
	promoteApproving
	promoteConfirming
	promoteSubmitting
	promoteDone
)

// promoteCandidate is one row in the promote-overlay picker. Eligible is
// true when the freight already satisfies the target stage's promotion
// rules (verified upstream, direct-warehouse origin, or already approved
// for promote-to-stage; verified at the source for promote-downstream).
// Ineligible candidates are rendered with a marker and require an extra
// "approve first?" confirmation before the promotion is dispatched.
// Current is true when the freight is one of the target stage's
// currently-deployed freight (Stage.CurrentFreight), rendered with a
// distinct marker so the user can tell which row they'd be re-promoting.
type promoteCandidate struct {
	Freight  kargo.Freight
	Eligible bool
	Current  bool
}

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

// openPromoteOverlay seeds promote-to-stage state for the given stage.
// Candidates come from candidateFreight: every freight in the project,
// with the entries that already satisfy the stage's promotion rules
// (verified upstream, direct-warehouse origin, or already approved)
// marked eligible. Ineligible entries render with a "needs approval"
// marker and require an extra confirm before the promotion fires.
// Works for control-flow stages too.
func (m *Model) openPromoteOverlay(stage *kargo.Stage) {
	if stage == nil {
		return
	}
	m.overlay = overlayPromote
	m.overlayTitle = "Promote · " + stage.Name
	m.promoteStage = stage.Name
	m.promoteStep = promotePicking
	m.promoteCursor = 0
	m.promoteResult = ""
	m.promoteError = nil
	m.promoteDownstream = false
	m.promoteCandidates = candidateFreight(m.freights, stage)
	// Empty candidates → the picker renders "no candidate freight found
	// for this stage" with esc-to-dismiss. Only happens when the project
	// has no freight at all, since candidateFreight returns every
	// freight (eligible or not).
}

// openPromoteDownstreamOverlay seeds the overlay for a "promote from this
// stage to every downstream subscriber" flow. The picker lists every
// freight in the project, with entries verified at the source stage
// marked eligible. Non-eligible entries render with a "needs approval"
// marker; picking one approves the freight for every downstream stage
// of source before the PromoteDownstream call.
func (m *Model) openPromoteDownstreamOverlay(stage *kargo.Stage) {
	if stage == nil {
		return
	}
	m.overlay = overlayPromote
	m.overlayTitle = "Promote downstream from · " + stage.Name
	m.promoteStage = stage.Name
	m.promoteStep = promotePicking
	m.promoteCursor = 0
	m.promoteResult = ""
	m.promoteError = nil
	m.promoteDownstream = true
	m.promoteCandidates = downstreamCandidateFreight(m.freights, stage)
}

// downstreamCandidateFreight returns every freight in the project sorted
// newest-first, with Eligible set on the entries verified at the source
// stage (the natural PromoteDownstream candidates). Non-eligible entries
// are surfaced too so the user can opt them in by approving the freight
// for each downstream stage before the promote fires. Current is set on
// freight currently deployed at the source stage so the picker can mark
// the "you'd be re-promoting this" row distinctly.
func downstreamCandidateFreight(all []kargo.Freight, source *kargo.Stage) []promoteCandidate {
	if source == nil {
		return nil
	}
	current := make(map[string]struct{}, len(source.CurrentFreight))
	for _, c := range source.CurrentFreight {
		current[c] = struct{}{}
	}
	out := make([]promoteCandidate, 0, len(all))
	for _, f := range all {
		eligible := false
		for _, vs := range f.VerifiedStages {
			if vs == source.Name {
				eligible = true
				break
			}
		}
		_, isCurrent := current[f.Name]
		out = append(out, promoteCandidate{Freight: f, Eligible: eligible, Current: isCurrent})
	}
	sortPromoteCandidates(out)
	return out
}

// candidateFreight returns every freight in the project sorted
// newest-first, with Eligible set on the entries the target stage will
// already accept. A freight is eligible if any of:
//   - It's verified in one of the stage's upstream stages.
//   - It originated from a Warehouse the stage pulls directly from
//     (Spec.RequestedFreight[].Sources.Direct).
//   - It's explicitly approved for this stage via ApprovedStages.
//
// Non-eligible freight is still returned (marked Eligible=false) so the
// user can approve-and-promote in one flow from the picker.
func candidateFreight(all []kargo.Freight, target *kargo.Stage) []promoteCandidate {
	if target == nil {
		return nil
	}
	upstream := make(map[string]struct{}, len(target.Upstreams))
	for _, u := range target.Upstreams {
		upstream[u] = struct{}{}
	}
	directWH := make(map[string]struct{}, len(target.DirectWarehouses))
	for _, w := range target.DirectWarehouses {
		directWH[w] = struct{}{}
	}
	current := make(map[string]struct{}, len(target.CurrentFreight))
	for _, c := range target.CurrentFreight {
		current[c] = struct{}{}
	}
	out := make([]promoteCandidate, 0, len(all))
	for _, f := range all {
		eligible := false
		for _, vs := range f.VerifiedStages {
			if _, ok := upstream[vs]; ok {
				eligible = true
				break
			}
		}
		if !eligible {
			if _, ok := directWH[f.Warehouse]; ok {
				eligible = true
			}
		}
		if !eligible {
			for _, as := range f.ApprovedStages {
				if as == target.Name {
					eligible = true
					break
				}
			}
		}
		_, isCurrent := current[f.Name]
		out = append(out, promoteCandidate{Freight: f, Eligible: eligible, Current: isCurrent})
	}
	sortPromoteCandidates(out)
	return out
}

// sortPromoteCandidates orders the picker rows so eligible freight comes
// first (the common path), then ineligible freight, each group ordered
// newest-first with name as tiebreaker.
func sortPromoteCandidates(cs []promoteCandidate) {
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Eligible != cs[j].Eligible {
			return cs[i].Eligible
		}
		ai, aj := cs[i].Freight, cs[j].Freight
		if !ai.Created.Equal(aj.Created) {
			return ai.Created.After(aj.Created)
		}
		return ai.Name < aj.Name
	})
}

// promoteOverlayView renders the freight-picker / confirm / result frames.
func (m Model) promoteOverlayView() tea.View {
	titleStyle := lipgloss.NewStyle().Foreground(normal).Bold(true).Background(bg)
	hintStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	itemStyle := lipgloss.NewStyle().Foreground(normal).Background(bg)
	selStyle := lipgloss.NewStyle().Foreground(darkFg).Background(selected).Bold(true)
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
	case promoteApproving:
		// Guard the cursor the same way the confirming step does: a
		// restricted list could have ended up empty before reaching us.
		if m.promoteCursor < 0 || m.promoteCursor >= len(m.promoteCandidates) {
			lines = append(lines,
				errStyle.Render("no candidate freight to approve"),
				"",
				hintStyle.Render("esc dismiss"),
			)
			break
		}
		c := m.promoteCandidates[m.promoteCursor]
		f := c.Freight
		var prompt, sub string
		if m.promoteDownstream {
			prompt = fmt.Sprintf("Freight %s%s is not verified at %s.",
				shortFreight(f.Name), aliasSuffix(f.Alias), m.promoteStage)
			sub = fmt.Sprintf("Approve it for every downstream subscriber of %s, then promote?", m.promoteStage)
		} else {
			prompt = fmt.Sprintf("Freight %s%s is not eligible for %s.",
				shortFreight(f.Name), aliasSuffix(f.Alias), m.promoteStage)
			sub = fmt.Sprintf("Approve it for %s, then promote?", m.promoteStage)
		}
		verifiedLine := fmt.Sprintf("Verified in %d stage(s)", f.VerifiedIn)
		if len(f.VerifiedStages) > 0 {
			verifiedLine += ": " + strings.Join(f.VerifiedStages, ", ")
		}
		approvedLine := fmt.Sprintf("Approved for %d stage(s)", f.ApprovedFor)
		if len(f.ApprovedStages) > 0 {
			approvedLine += ": " + strings.Join(f.ApprovedStages, ", ")
		}
		lines = append(lines,
			itemStyle.Render(prompt),
			itemStyle.Render(sub),
			"",
			hintStyle.Render(verifiedLine),
			hintStyle.Render(approvedLine),
			"",
			hintStyle.Render("y approve and promote · n/esc cancel"),
		)

	case promoteConfirming:
		// Guard the index: the picker can land here from `>` with a
		// restricted candidate list that came up empty (e.g. the deploy
		// stage's CurrentFreight isn't yet verified-at-this-stage in
		// our local freight snapshot). Treat that as a cancel rather
		// than a render panic.
		if m.promoteCursor < 0 || m.promoteCursor >= len(m.promoteCandidates) {
			lines = append(lines,
				errStyle.Render("no candidate freight to promote"),
				"",
				hintStyle.Render("esc dismiss"),
			)
			break
		}
		f := m.promoteCandidates[m.promoteCursor].Freight
		var prompt string
		if m.promoteDownstream {
			prompt = fmt.Sprintf("Promote freight %s%s from %s to every downstream subscriber?",
				shortFreight(f.Name), aliasSuffix(f.Alias), m.promoteStage)
		} else {
			prompt = fmt.Sprintf("Promote freight %s%s to stage %s?",
				shortFreight(f.Name), aliasSuffix(f.Alias), m.promoteStage)
		}
		lines = append(lines,
			itemStyle.Render(prompt),
			"",
			hintStyle.Render("y confirm · n/esc cancel"),
		)

	case promoteSubmitting:
		lines = append(lines, hintStyle.Render("submitting promotion…"))

	case promoteDone:
		if m.promoteError != nil {
			lines = append(lines, errStyle.Render("error: "+m.promoteError.Error()))
		} else {
			label := "promoted: "
			if m.promoteDownstream {
				label = "created: "
			}
			lines = append(lines, okStyle.Render(label+m.promoteResult))
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
	box = paintFrame(box, m.width, m.height)

	v := tea.NewView(box)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = m.activeMouseMode()
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
		mutedStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
		currentStyle := lipgloss.NewStyle().Foreground(healthy).Background(bg).Bold(true)
		rows := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			c := m.promoteCandidates[i]
			f := c.Freight
			label := shortFreight(f.Name) + aliasSuffix(f.Alias)
			meta := []string{}
			if c.Current {
				meta = append(meta, "current")
			}
			if !c.Eligible {
				meta = append(meta, "needs approval")
			}
			meta = append(meta, fmt.Sprintf("v=%d a=%d", f.VerifiedIn, f.ApprovedFor))
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
			// Off-cursor: current ●, needs-approval ?, else blank. On the
			// cursor row the bar already signals focus and the meta strip
			// ("current" / "needs approval") carries the state in words,
			// so don't try to squeeze a state glyph next to the bar — it
			// produces a glued-on look without a space separator.
			marker := "  "
			switch {
			case c.Current:
				marker = "● "
			case !c.Eligible:
				marker = "? "
			}
			if i == m.promoteCursor {
				marker = "▌ "
			}
			row := marker + label
			switch {
			case i == m.promoteCursor:
				rows = append(rows, selStyle.Padding(0, 1).Render(row))
			case c.Current:
				rows = append(rows, currentStyle.Padding(0, 1).Render(row))
			case !c.Eligible:
				rows = append(rows, mutedStyle.Padding(0, 1).Render(row))
			default:
				rows = append(rows, itemStyle.Padding(0, 1).Render(row))
			}
		}
		body = strings.Join(rows, "\n")
	}

	content := lipgloss.JoinVertical(lipgloss.Left, header, body, hint)
	content = paintFrame(content, m.width, m.height)
	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = m.activeMouseMode()
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
	box = paintFrame(box, m.width, m.height)
	v := tea.NewView(box)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = m.activeMouseMode()
	return v
}

// promoteViewingContent builds the scrollable detail body for the
// highlighted candidate. Mirrors the freight-detail render used in the
// side panel.
func (m Model) promoteViewingContent(innerW int) string {
	if m.promoteCursor < 0 || m.promoteCursor >= len(m.promoteCandidates) {
		return ""
	}
	c := m.promoteCandidates[m.promoteCursor]
	f := c.Freight
	titleStyle := lipgloss.NewStyle().Foreground(normal).Bold(true).Background(bg)
	keyStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	valStyle := lipgloss.NewStyle().Foreground(normal).Background(bg)
	warnStyle := lipgloss.NewStyle().Foreground(progressing).Background(bg)
	currentStyle := lipgloss.NewStyle().Foreground(healthy).Background(bg).Bold(true)
	row := func(k, v string) string {
		return keyStyle.Render(padRight(k+":", 12)) + " " + valStyle.Render(wrap(v, innerW-13))
	}
	var lines []string
	lines = append(lines, titleStyle.Render(f.Name))
	if f.Alias != "" {
		lines = append(lines, keyStyle.Render(f.Alias))
	}
	if c.Current {
		lines = append(lines, currentStyle.Render("● Currently deployed at "+m.promoteStage+"."))
	}
	if !c.Eligible {
		if m.promoteDownstream {
			lines = append(lines, warnStyle.Render("Not verified at "+m.promoteStage+". Picking will require approval first."))
		} else {
			lines = append(lines, warnStyle.Render("Not eligible for "+m.promoteStage+". Picking will require approval first."))
		}
	}
	lines = append(lines, freightDetailLines(f, innerW, row, keyStyle, valStyle)...)
	return strings.Join(lines, "\n")
}

// updatePromoteOverlay routes a key press while the promote overlay is open.
// Each promoteStep accepts a different key set: pick (arrows + enter +
// v to view), view (scroll keys + enter to advance + v/esc to return),
// approve (y/n) for ineligible freight, confirm (y/n) for the regular
// path, submitting (esc to dismiss without cancelling the in-flight
// RPC), done (enter/esc to match the hint). Enter from pick/view routes
// to approve when the highlighted candidate is ineligible, otherwise to
// confirm.
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
			if !m.promoteCandidates[m.promoteCursor].Eligible {
				m.promoteStep = promoteApproving
			} else {
				m.promoteStep = promoteConfirming
			}
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
			if len(m.promoteCandidates) == 0 {
				return m, nil
			}
			if !m.promoteCandidates[m.promoteCursor].Eligible {
				m.promoteStep = promoteApproving
			} else {
				m.promoteStep = promoteConfirming
			}
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
	case promoteApproving:
		switch key {
		case "y", "Y":
			if m.promoteCursor < 0 || m.promoteCursor >= len(m.promoteCandidates) {
				m.overlay = overlayNone
				return m, nil
			}
			f := m.promoteCandidates[m.promoteCursor].Freight
			m.promoteStep = promoteSubmitting
			if m.promoteDownstream {
				stages := downstreamStagesOf(m.deploys, m.promoteStage)
				return m, approveAndPromoteDownstreamCmd(m.client, m.project, m.promoteStage, f.Name, stages)
			}
			return m, approveAndPromoteCmd(m.client, m.project, m.promoteStage, f.Name)
		case "n", "N", "esc":
			m.promoteStep = promotePicking
			return m, nil
		}
	case promoteConfirming:
		switch key {
		case "y", "Y":
			if m.promoteCursor < 0 || m.promoteCursor >= len(m.promoteCandidates) {
				// No candidate to submit (empty restricted list);
				// behave like cancel.
				m.overlay = overlayNone
				return m, nil
			}
			f := m.promoteCandidates[m.promoteCursor].Freight
			m.promoteStep = promoteSubmitting
			if m.promoteDownstream {
				return m, promoteDownstreamCmd(m.client, m.project, m.promoteStage, f.Name)
			}
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

// approveAndPromoteCmd approves the freight for the target stage and then
// dispatches the normal PromoteToStage call. Surfaces the approve error
// (if any) through promoteResultMsg so the overlay's submitting-step
// transition stays consistent.
func approveAndPromoteCmd(c *kargo.Client, project, stage, freight string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if err := c.ApproveFreight(ctx, project, freight, stage); err != nil {
			return promoteResultMsg{stage: stage, freight: freight, err: err}
		}
		entry, err := c.PromoteToStage(ctx, project, stage, freight)
		msg := promoteResultMsg{stage: stage, freight: freight, err: err}
		if entry != nil {
			msg.promotionName = entry.Name
			msg.phase = entry.Phase
		}
		return msg
	}
}

// approveAndPromoteDownstreamCmd approves the freight for every downstream
// stage of source (so each subscriber will accept it) and then dispatches
// the normal PromoteDownstream call. Any approve error short-circuits and
// surfaces through promoteDownstreamResultMsg.
func approveAndPromoteDownstreamCmd(c *kargo.Client, project, source, freight string, downstreams []string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		for _, ds := range downstreams {
			if err := c.ApproveFreight(ctx, project, freight, ds); err != nil {
				return promoteDownstreamResultMsg{source: source, freight: freight, err: err}
			}
		}
		entries, err := c.PromoteDownstream(ctx, project, source, freight)
		return promoteDownstreamResultMsg{
			source:     source,
			freight:    freight,
			promotions: len(entries),
			err:        err,
		}
	}
}

// downstreamStagesOf returns the names of every stage in stages that
// lists source as one of its upstream stages, i.e. the natural targets a
// PromoteDownstream from source will fan out to.
func downstreamStagesOf(stages []kargo.Stage, source string) []string {
	var out []string
	for _, s := range stages {
		for _, up := range s.Upstreams {
			if up == source {
				out = append(out, s.Name)
				break
			}
		}
	}
	return out
}
