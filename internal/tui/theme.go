package tui

import (
	"context"
	"image/color"

	"charm.land/lipgloss/v2"
)

type themeMode int

const (
	themeDark themeMode = iota
	themeLight
)

// palette is the set of named colors the TUI uses. Every reference to a
// color in this package routes through one of the package-level vars
// declared in model.go; applyTheme reassigns those vars when the user
// toggles or the terminal background is detected.
type palette struct {
	bg          color.Color
	darkFg      color.Color
	selected    color.Color
	healthy     color.Color
	degraded    color.Color
	progressing color.Color
	muted       color.Color
	normal      color.Color
}

// Pierre Dark, mirroring the Ghostty theme of the same name.
var pierreDark = palette{
	bg:          lipgloss.Color("#070707"),
	darkFg:      lipgloss.Color("#070707"),
	selected:    lipgloss.Color("#009fff"),
	healthy:     lipgloss.Color("#00cab1"),
	degraded:    lipgloss.Color("#ff2e3f"),
	progressing: lipgloss.Color("#ffca00"),
	muted:       lipgloss.Color("#84848a"),
	normal:      lipgloss.Color("#fbfbfb"),
}

// Pierre Light, mirroring the Ghostty theme of the same name. darkFg
// stays light so reverse-video text on the selected-row bar (painted in
// selected) remains readable.
var pierreLight = palette{
	bg:          lipgloss.Color("#ffffff"),
	darkFg:      lipgloss.Color("#ffffff"),
	selected:    lipgloss.Color("#1a85d4"),
	healthy:     lipgloss.Color("#18a46c"),
	degraded:    lipgloss.Color("#d52c36"),
	progressing: lipgloss.Color("#d5a910"),
	muted:       lipgloss.Color("#737373"),
	normal:      lipgloss.Color("#0a0a0a"),
}

// applyTheme reassigns the package-level color vars so the hundreds of
// existing references across the package pick up the new palette on the
// next render.
func applyTheme(p palette) {
	bg = p.bg
	darkFg = p.darkFg
	selected = p.selected
	healthy = p.healthy
	degraded = p.degraded
	progressing = p.progressing
	muted = p.muted
	normal = p.normal
}

func paletteFor(mode themeMode) palette {
	if mode == themeLight {
		return pierreLight
	}
	return pierreDark
}

// WithDetectedDark applies an auto-detected light theme when the
// caller (main.go via OSC 11) determined the terminal background is
// light. No-op when the terminal is dark since themeDark is the
// default. Call before Run. The user can still override the result by
// pressing T at any time.
func (m Model) WithDetectedDark(dark bool) Model {
	if dark {
		return m
	}
	// Pre-Run path: no Update span exists yet, so the refreshRows trace
	// is a top-level boundary keyed off context.Background.
	m.setTheme(context.Background(), themeLight)
	return m
}

// setTheme switches the active palette, refreshes the table styles that
// bake colors in at construction time, and invalidates the graph render
// cache (its memoized output is colored by the previous palette).
func (m *Model) setTheme(ctx context.Context, mode themeMode) {
	if m.theme == mode {
		return
	}
	m.theme = mode
	applyTheme(paletteFor(mode))
	restyleTable(&m.deploysTable)
	restyleTable(&m.freightsTable)
	// Per-cell ANSI is baked at row-build time, so the cached rows still
	// carry the previous palette's colors. Drop the cache so refreshRows
	// rebuilds and pushes the new palette through immediately.
	m.lastDeployRows = nil
	m.lastFreightRows = nil
	m.refreshRows(ctx)
	if m.graphRender != nil {
		m.graphRender.valid = false
	}
	m.graphLayoutVersion++
	m.refreshPanel()
}
