package tui

import (
	"sort"

	"charm.land/bubbles/v2/table"

	"unknwon.dev/kargo-tui/internal/kargo"
)

// sortMode is the sort order applied to a list view. The active mode is
// stored per-view on the Model so each list keeps its own sort.
type sortMode int

// sortDefault is "by age, newest first" because the kargo client sorts
// ListStages and ListFreight that way before handing data to the TUI.
// There is no separate sortByAge: it would be a duplicate of sortDefault.
const (
	sortDefault sortMode = iota
	sortByName
	sortByHealth
	sortByLastPromo
	sortByCurrentlyIn
	sortByVerifiedIn
	sortByApprovedFor
)

// String returns a short label for the bottom-bar sort indicator.
func (s sortMode) String() string {
	switch s {
	case sortByName:
		return "name"
	case sortByHealth:
		return "health"
	case sortByLastPromo:
		return "last-promo"
	case sortByCurrentlyIn:
		return "currently-in"
	case sortByVerifiedIn:
		return "verified-in"
	case sortByApprovedFor:
		return "approved-for"
	default:
		return "age"
	}
}

// cycleSort advances the sort mode for the current view through the modes
// that actually do something there, wrapping back to sortDefault after the
// last entry. Skipping inapplicable modes (e.g., sortByHealth on freights)
// keeps the bottom-bar hint and column-header arrow in sync with what the
// data is actually doing.
func (m *Model) cycleSort() {
	modes := sortModesForView(m.view)
	cur := m.sort[m.view]
	for i, mode := range modes {
		if mode == cur {
			m.sort[m.view] = modes[(i+1)%len(modes)]
			return
		}
	}
	m.sort[m.view] = modes[0]
}

// sortModesForView returns the sort modes that are meaningful in the given
// view, in cycle order. The deploys/control-flow views have all stage-level
// fields to sort on; the freights view only has name as a non-default
// alternative because the rest are stage-specific.
func sortModesForView(v view) []sortMode {
	if v == viewFreights {
		return []sortMode{sortDefault, sortByName, sortByCurrentlyIn, sortByVerifiedIn, sortByApprovedFor}
	}
	return []sortMode{sortDefault, sortByName, sortByHealth, sortByLastPromo}
}

// sortDeploys returns a copy of the input slice ordered by the current
// view's sort mode. sortDefault returns the input as-is.
func (m *Model) sortDeploys(in []kargo.Stage) []kargo.Stage {
	mode := m.sort[m.view]
	if mode == sortDefault {
		return in
	}
	out := make([]kargo.Stage, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool {
		switch mode {
		case sortByName:
			return out[i].Name < out[j].Name
		case sortByHealth:
			if ri, rj := healthRank(out[i].Health), healthRank(out[j].Health); ri != rj {
				return ri < rj
			}
			return out[i].Name < out[j].Name
		case sortByLastPromo:
			if !out[i].LastPromoAt.Equal(out[j].LastPromoAt) {
				return out[i].LastPromoAt.After(out[j].LastPromoAt)
			}
			return out[i].Name < out[j].Name
		}
		return false
	})
	return out
}

// sortFreights returns a copy of the input slice ordered by the current
// view's sort mode. Default and the stage-only modes fall through to the
// client's newest-first ordering. Count-based modes (verified-in,
// approved-for) sort descending so freight that has reached the most
// stages floats to the top, with name as a stable tiebreaker.
func (m *Model) sortFreights(in []kargo.Freight) []kargo.Freight {
	mode := m.sort[m.view]
	switch mode {
	case sortByName, sortByCurrentlyIn, sortByVerifiedIn, sortByApprovedFor:
	default:
		return in
	}
	out := make([]kargo.Freight, len(in))
	copy(out, in)
	controlFlowStages := map[string]bool{}
	if mode == sortByCurrentlyIn {
		controlFlowStages = m.controlFlowStages()
	}
	sort.SliceStable(out, func(i, j int) bool {
		switch mode {
		case sortByName:
			return out[i].Name < out[j].Name
		case sortByCurrentlyIn:
			currentI := currentStageNames(out[i], controlFlowStages)
			currentJ := currentStageNames(out[j], controlFlowStages)
			ci := len(currentI)
			cj := len(currentJ)
			if ci != cj {
				return ci > cj
			}
			return out[i].Name < out[j].Name
		case sortByVerifiedIn:
			if out[i].VerifiedIn != out[j].VerifiedIn {
				return out[i].VerifiedIn > out[j].VerifiedIn
			}
			return out[i].Name < out[j].Name
		case sortByApprovedFor:
			if out[i].ApprovedFor != out[j].ApprovedFor {
				return out[i].ApprovedFor > out[j].ApprovedFor
			}
			return out[i].Name < out[j].Name
		}
		return false
	})
	return out
}

// decorateColumnsWithSort returns a copy of cols with an arrow appended to
// the title of whichever column drives the given sort mode. The arrow points
// the way values actually flow in the column: ↑ for A→Z by name, ↓ for the
// newest/most-severe-first orderings used by age, health, and last-promo.
// Returns cols unchanged when no sort is active or no column in the slice
// matches the mode's target (e.g., the column has scrolled off-screen).
func decorateColumnsWithSort(cols []table.Column, mode sortMode) []table.Column {
	target, arrow := sortIndicatorFor(mode)
	if target == "" {
		return cols
	}
	out := make([]table.Column, len(cols))
	copy(out, cols)
	for i := range out {
		if out[i].Title == target {
			out[i].Title = target + " " + arrow
			break
		}
	}
	return out
}

// sortIndicatorFor maps a sort mode to the column title it decorates and the
// arrow glyph to append. sortDefault decorates Age because the client lists
// stages and freight newest-first with name as tiebreaker (see ListStages
// and ListFreight), so the default order is effectively by age. Treating it
// as "no arrow" would mislead the user into thinking nothing is sorted.
func sortIndicatorFor(mode sortMode) (column, arrow string) {
	switch mode {
	case sortDefault:
		return "Age", "↓"
	case sortByName:
		return "Name", "↑"
	case sortByHealth:
		return "Health", "↓"
	case sortByLastPromo:
		return "Last Promo", "↓"
	case sortByCurrentlyIn:
		return "Current", "↓"
	case sortByVerifiedIn:
		return "Verified", "↓"
	case sortByApprovedFor:
		return "Approved", "↓"
	}
	return "", ""
}

// healthRank orders worst-first so degraded stages float to the top when
// sorted by health.
func healthRank(h string) int {
	switch h {
	case "Unhealthy":
		return 0
	case "Progressing":
		return 1
	case "Healthy":
		return 2
	case "":
		return 4
	default:
		return 3
	}
}
