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
	promoteConfirming
	promoteSubmitting
	promoteDone
)

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

	var lines []string
	lines = append(lines, titleStyle.Render(m.overlayTitle))
	lines = append(lines, "")

	switch m.promoteStep {
	case promotePicking:
		if len(m.promoteCandidates) == 0 {
			lines = append(lines,
				hintStyle.Render("no candidate freight found for this stage"),
				"",
				hintStyle.Render("esc dismiss"),
			)
			break
		}
		lines = append(lines, hintStyle.Render("↑/↓ select · enter pick · esc cancel"))
		lines = append(lines, "")
		maxItems := m.height - len(lines) - 6
		if maxItems < 5 {
			maxItems = 5
		}
		start := 0
		if m.promoteCursor >= maxItems {
			start = m.promoteCursor - maxItems + 1
		}
		end := start + maxItems
		if end > len(m.promoteCandidates) {
			end = len(m.promoteCandidates)
		}
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
			marker := "  "
			if i == m.promoteCursor {
				marker = "▌ "
				lines = append(lines, selStyle.Render(marker+label))
			} else {
				lines = append(lines, itemStyle.Render(marker+label))
			}
		}

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

// updatePromoteOverlay routes a key press while the promote overlay is open.
// Each promoteStep accepts a different key set: pick (arrows + enter),
// confirm (y/n), submitting (esc to dismiss without cancelling the in-flight
// RPC), done (any key dismisses).
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
		case "enter":
			if len(m.promoteCandidates) == 0 {
				return m, nil
			}
			m.promoteStep = promoteConfirming
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
		m.overlay = overlayNone
		return m, nil
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
