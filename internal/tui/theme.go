package tui

import (
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

// setTheme switches the active palette, refreshes the table styles that
// bake colors in at construction time, and invalidates the graph render
// cache (its memoized output is colored by the previous palette).
func (m *Model) setTheme(mode themeMode) {
	if m.theme == mode {
		return
	}
	m.theme = mode
	applyTheme(paletteFor(mode))
	restyleTable(&m.deploysTable)
	restyleTable(&m.freightsTable)
	if m.graphRender != nil {
		m.graphRender.valid = false
	}
	m.graphLayoutVersion++
	m.refreshPanel()
}
