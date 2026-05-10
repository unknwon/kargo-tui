package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// stageNameCell renders a stage name styled by health (green/red/yellow/
// default). Used in tables and detail panels to make the row's overall
// status visible at a glance.
func stageNameCell(name, health string) string {
	c := normal
	switch health {
	case "Healthy":
		c = healthy
	case "Unhealthy":
		c = degraded
	case "Progressing":
		c = progressing
	}
	return lipgloss.NewStyle().Foreground(c).Bold(true).Render(name)
}

// healthCell renders a Kargo Stage health state with a semantic color, or a
// muted dash for the empty case.
func healthCell(s string) string {
	switch s {
	case "Healthy":
		return lipgloss.NewStyle().Foreground(healthy).Render(s)
	case "Unhealthy":
		return lipgloss.NewStyle().Foreground(degraded).Render(s)
	case "Progressing":
		return lipgloss.NewStyle().Foreground(progressing).Render(s)
	case "":
		return lipgloss.NewStyle().Foreground(muted).Render("—")
	default:
		return lipgloss.NewStyle().Foreground(muted).Render(s)
	}
}

// argoHealthCell colors an Argo CD Application health state.
func argoHealthCell(s string) string {
	switch s {
	case "Healthy":
		return lipgloss.NewStyle().Foreground(healthy).Render(s)
	case "Progressing":
		return lipgloss.NewStyle().Foreground(progressing).Render(s)
	case "Degraded", "Missing":
		return lipgloss.NewStyle().Foreground(degraded).Render(s)
	case "Suspended":
		return lipgloss.NewStyle().Foreground(progressing).Italic(true).Render(s)
	case "":
		return lipgloss.NewStyle().Foreground(muted).Render("—")
	default:
		return lipgloss.NewStyle().Foreground(muted).Render(s)
	}
}

// argoSyncCell colors an Argo CD Application sync state.
func argoSyncCell(s string) string {
	switch s {
	case "Synced":
		return lipgloss.NewStyle().Foreground(healthy).Render(s)
	case "OutOfSync":
		return lipgloss.NewStyle().Foreground(degraded).Render(s)
	case "":
		return lipgloss.NewStyle().Foreground(muted).Render("—")
	default:
		return lipgloss.NewStyle().Foreground(muted).Render(s)
	}
}

// promoCell colors a Promotion phase string (Succeeded/Failed/Running/…).
func promoCell(s string) string {
	switch s {
	case "Succeeded":
		return lipgloss.NewStyle().Foreground(healthy).Render(s)
	case "Failed", "Errored", "Aborted":
		return lipgloss.NewStyle().Foreground(degraded).Render(s)
	case "Running", "Pending":
		return lipgloss.NewStyle().Foreground(progressing).Render(s)
	case "":
		return lipgloss.NewStyle().Foreground(muted).Render("—")
	default:
		return lipgloss.NewStyle().Foreground(muted).Render(s)
	}
}

// stageFreightSummary renders a Stage's FreightSummary cell. For control-flow
// stages with no summary it shows a passes-through hint; for single-freight
// summaries (a 40-char hex name) it shortens to 8 chars + alias.
func stageFreightSummary(s string, controlFlow bool, alias string) string {
	if s == "" {
		if controlFlow {
			return lipgloss.NewStyle().Foreground(muted).Italic(true).Render("(passes through)")
		}
		return lipgloss.NewStyle().Foreground(muted).Render("—")
	}
	if isFreightName(s) {
		short := s
		if len(short) > 8 {
			short = short[:8]
		}
		out := lipgloss.NewStyle().Foreground(normal).Render(short)
		if alias != "" {
			out += lipgloss.NewStyle().Foreground(muted).Render(" " + alias)
		}
		return out
	}
	if len(s) > 30 {
		s = s[:29] + "…"
	}
	return s
}

// isFreightName returns true when s looks like a Kargo freight resource name
// (40-char hex hash). Kargo also uses summaries like "3/5 Fulfilled" which
// should be left untouched.
func isFreightName(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// aliasOf returns the alias for a freight name, or "" if not found in the
// currently loaded freight list.
func (m Model) aliasOf(name string) string {
	for _, f := range m.freights {
		if f.Name == name {
			return f.Alias
		}
	}
	return ""
}

// freightNameCell renders a freight short SHA followed by its alias (when
// present), used in the freights table.
func freightNameCell(name, alias string) string {
	short := name
	if len(short) > 8 {
		short = short[:8]
	}
	short = lipgloss.NewStyle().Foreground(normal).Render(short)
	if alias == "" {
		return short
	}
	a := lipgloss.NewStyle().Foreground(muted).Render(" " + alias)
	return short + a
}

// stringOrDash renders a string, or a muted em-dash when the input is empty.
func stringOrDash(s string) string {
	if s == "" {
		return lipgloss.NewStyle().Foreground(muted).Render("—")
	}
	return s
}

// countCell renders a non-negative count, colored green when positive and
// red when zero (used for verified/approved counts on freight).
func countCell(n int) string {
	style := lipgloss.NewStyle().Foreground(degraded)
	if n > 0 {
		style = lipgloss.NewStyle().Foreground(healthy)
	}
	return style.Render(fmt.Sprintf("%d", n))
}

// emptyDash returns "—" for empty input, otherwise the input unchanged.
func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ageString formats a time.Time as a compact "Ns/Nm/Nh/Nd" age, or "—" when
// the time is zero.
func ageString(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// padRight returns s padded with spaces to width w (no-op when s is already
// at least w wide).
func padRight(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

// wrap inserts hard newlines every w runes so content fits a fixed-width
// column. Returns the input unchanged when it already fits.
func wrap(s string, w int) string {
	if w <= 0 || len(s) <= w {
		return s
	}
	var b strings.Builder
	for len(s) > w {
		b.WriteString(s[:w])
		b.WriteByte('\n')
		s = s[w:]
	}
	b.WriteString(s)
	return b.String()
}

// sortedKeys returns the alphabetically-sorted keys of a string-keyed map.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
