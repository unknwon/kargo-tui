package tui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"unknwon.dev/kargo-tui/internal/kargo"
)

// fgCell renders v with the given foreground over the table's bg. Used by
// the styled cell builders. Baking the background into every pre-styled
// cell is what keeps the column's padding cells from falling back to the
// terminal default when bubbles' inner Width().Inline() padding wraps an
// ANSI-styled value (the inner padding inherits no bg, and the outer Cell
// style's bg doesn't propagate through the inner SGR resets).
func fgCell(fg color.Color, v string) string {
	return lipgloss.NewStyle().Foreground(fg).Background(bg).Render(v)
}

// paintFrame paints a full-screen background by right-padding every line
// of s to width with bg-colored spaces, then appending bg-filled lines
// until the frame is height rows tall. Both dimensions are optional
// (0 disables that axis). Used at the top of every View entry point so
// the terminal's default colors never leak through trailing cells or
// rows below the rendered content.
func paintFrame(s string, width, height int) string {
	pad := lipgloss.NewStyle().Background(bg)
	lines := strings.Split(s, "\n")
	if width > 0 {
		for i, line := range lines {
			gap := width - lipgloss.Width(line)
			if gap <= 0 {
				continue
			}
			lines[i] = line + pad.Render(strings.Repeat(" ", gap))
		}
	}
	if height > 0 && len(lines) < height {
		blank := ""
		if width > 0 {
			blank = pad.Render(strings.Repeat(" ", width))
		}
		for len(lines) < height {
			lines = append(lines, blank)
		}
	}
	return strings.Join(lines, "\n")
}

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
	return lipgloss.NewStyle().Foreground(c).Background(bg).Bold(true).Render(name)
}

// healthCell renders a Kargo Stage health state with a semantic color, or a
// muted dash for the empty case.
func healthCell(s string) string {
	switch s {
	case "Healthy":
		return fgCell(healthy, s)
	case "Unhealthy":
		return fgCell(degraded, s)
	case "Progressing":
		return fgCell(progressing, s)
	case "":
		return fgCell(muted, "—")
	default:
		return fgCell(muted, s)
	}
}

// argoHealthCell colors an Argo CD Application health state.
func argoHealthCell(s string) string {
	switch s {
	case "Healthy":
		return fgCell(healthy, s)
	case "Progressing":
		return fgCell(progressing, s)
	case "Degraded", "Missing":
		return fgCell(degraded, s)
	case "Suspended":
		return lipgloss.NewStyle().Foreground(progressing).Background(bg).Italic(true).Render(s)
	case "":
		return fgCell(muted, "—")
	default:
		return fgCell(muted, s)
	}
}

// argoSyncCell colors an Argo CD Application sync state.
func argoSyncCell(s string) string {
	switch s {
	case "Synced":
		return fgCell(healthy, s)
	case "OutOfSync":
		return fgCell(degraded, s)
	case "":
		return fgCell(muted, "—")
	default:
		return fgCell(muted, s)
	}
}

// stageArgoCell summarises Argo CD state across a stage's referenced apps
// for the deploy list's "Argo" column as "<health>/<sync>", each piece
// colored independently. A muted dash is returned when the stage has no
// Argo apps so the column visually distinguishes "no Argo wiring" from
// "Argo says everything is fine".
func stageArgoCell(apps []kargo.ArgoCDAppRef) string {
	if len(apps) == 0 {
		return fgCell(muted, "—")
	}
	worstHealth, worstSync := "Healthy", "Synced"
	for _, a := range apps {
		// Health severity: Degraded > Missing > Suspended > Progressing >
		// Unknown > Healthy. Collapse "anything not Healthy" upward.
		switch a.Health {
		case "Degraded":
			worstHealth = "Degraded"
		case "Missing":
			if worstHealth != "Degraded" {
				worstHealth = "Missing"
			}
		case "Suspended":
			if worstHealth == "Healthy" || worstHealth == "Progressing" || worstHealth == "Unknown" {
				worstHealth = "Suspended"
			}
		case "Progressing":
			if worstHealth == "Healthy" || worstHealth == "Unknown" {
				worstHealth = "Progressing"
			}
		case "Unknown", "":
			if worstHealth == "Healthy" {
				worstHealth = "Unknown"
			}
		}
		switch a.Sync {
		case "OutOfSync":
			worstSync = "OutOfSync"
		case "Unknown", "":
			if worstSync == "Synced" {
				worstSync = "Unknown"
			}
		}
	}
	sep := fgCell(muted, "/")
	return argoHealthCell(worstHealth) + sep + argoSyncCell(worstSync)
}

// promoCell colors a Promotion phase string (Succeeded/Failed/Running/…).
func promoCell(s string) string {
	switch s {
	case "Succeeded":
		return fgCell(healthy, s)
	case "Failed", "Errored", "Aborted":
		return fgCell(degraded, s)
	case "Running", "Pending":
		return fgCell(progressing, s)
	case "":
		return fgCell(muted, "—")
	default:
		return fgCell(muted, s)
	}
}

// stageFreightSummary renders a Stage's FreightSummary cell. For control-flow
// stages with no summary it shows a passes-through hint; for single-freight
// summaries (a 40-char hex name) it shortens to 8 chars + alias.
func stageFreightSummary(s string, controlFlow bool, alias string) string {
	if s == "" {
		if controlFlow {
			return lipgloss.NewStyle().Foreground(muted).Background(bg).Italic(true).Render("(passes through)")
		}
		return fgCell(muted, "—")
	}
	if isFreightName(s) {
		short := s
		if len(short) > 8 {
			short = short[:8]
		}
		out := fgCell(normal, short)
		if alias != "" {
			out += fgCell(muted, " "+alias)
		}
		return out
	}
	if len(s) > 30 {
		s = s[:29] + "…"
	}
	return fgCell(normal, s)
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
	out := fgCell(normal, short)
	if alias == "" {
		return out
	}
	return out + fgCell(muted, " "+alias)
}

// stringOrDash renders a string, or a muted em-dash when the input is empty.
func stringOrDash(s string) string {
	if s == "" {
		return fgCell(muted, "—")
	}
	return fgCell(normal, s)
}

// countCell renders a non-negative count, colored green when positive and
// red when zero (used for verified/approved counts on freight).
func countCell(n int) string {
	fg := degraded
	if n > 0 {
		fg = healthy
	}
	return fgCell(fg, fmt.Sprintf("%d", n))
}

// emptyDash returns "—" for empty input, otherwise the input unchanged.
func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// whenString is the canonical "<relative> ago (<absolute UTC>)" format used
// in every detail view. Returns a muted em-dash when t is zero.
//
// Future timestamps (clock skew, OCI image build > now) are clamped to "0s
// ago" rather than rendering as negative.
func whenString(t time.Time) string {
	if t.IsZero() {
		return lipgloss.NewStyle().Foreground(muted).Render("—")
	}
	return ageString(t) + " ago (" + t.UTC().Format("2006-01-02 15:04:05 UTC") + ")"
}

// whenStringApprox is whenString prefixed with "~" to mark the timestamp as
// approximate (e.g. promotion-step times derived from the parent promotion's
// start when the controller hasn't recorded per-step timestamps yet).
func whenStringApprox(t time.Time) string {
	if t.IsZero() {
		return lipgloss.NewStyle().Foreground(muted).Render("—")
	}
	return lipgloss.NewStyle().Foreground(muted).Render("~") + ageString(t) + lipgloss.NewStyle().Foreground(muted).Render(" ago ("+t.UTC().Format("2006-01-02 15:04:05 UTC")+")")
}

// stepWhen renders a promotion step's "<relative> ago (<UTC>)" timestamp,
// preferring finished > started. Falls back to the parent promotion's start
// time prefixed with "~" to mark it approximate when the controller hasn't
// stamped the step yet (e.g. step still queued, or older Kargo server).
func stepWhen(s kargo.PromotionStep, fallback time.Time) string {
	if !s.FinishedAt.IsZero() {
		return whenString(s.FinishedAt)
	}
	if !s.StartedAt.IsZero() {
		return whenString(s.StartedAt)
	}
	return whenStringApprox(fallback)
}

// ageString formats a time.Time as a compact "Ns/Nm/Nh/Nd" age, or "—" when
// the time is zero.
func ageString(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
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
	// Pad by display width (cells), not byte length, so multi-byte glyphs
	// like ↑ ↓ ← → don't push following columns out of alignment.
	cells := lipgloss.Width(s)
	if cells >= w {
		return s
	}
	return s + strings.Repeat(" ", w-cells)
}

// wrap word-wraps s to width w, with a hard break for tokens longer than w
// (URLs, hash-shaped freight names). Multi-byte safe via ansi.Wrap.
// Returns the input unchanged when w <= 0 or it already fits.
func wrap(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	return ansi.Wrap(s, w, " -._/")
}

// wrapIndent word-wraps s to width w and prefixes every continuation line
// with indent — useful for keeping wrapped detail rows visually nested under
// their list marker.
func wrapIndent(s string, w int, indent string) string {
	wrapped := wrap(s, w)
	if !strings.Contains(wrapped, "\n") {
		return wrapped
	}
	parts := strings.Split(wrapped, "\n")
	for i := 1; i < len(parts); i++ {
		parts[i] = indent + parts[i]
	}
	return strings.Join(parts, "\n")
}

// popupInnerWidth returns the wrap target for popup body text given the
// terminal width. Accounts for the rounded border (2 cells) and Padding(1,2)
// (4 cells) used by every popup box, and caps at 100 cells so popups don't
// stretch uncomfortably wide on big terminals.
func popupInnerWidth(termWidth int) int {
	if termWidth <= 0 {
		return 80
	}
	w := termWidth - 6
	if w > 100 {
		w = 100
	}
	if w < 20 {
		w = 20
	}
	return w
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
