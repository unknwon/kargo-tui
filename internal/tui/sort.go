package tui

import (
	"sort"

	"unknwon.dev/kargo-tui/internal/kargo"
)

// sortMode is the sort order applied to a list view. The active mode is
// stored per-view on the Model so each list keeps its own sort.
type sortMode int

const (
	sortDefault sortMode = iota
	sortByName
	sortByAge
	sortByHealth
	sortByLastPromo
)

// String returns a short label for the bottom-bar sort indicator.
func (s sortMode) String() string {
	switch s {
	case sortByName:
		return "name"
	case sortByAge:
		return "age"
	case sortByHealth:
		return "health"
	case sortByLastPromo:
		return "last-promo"
	default:
		return "default"
	}
}

// cycleSort advances the sort mode for the current view, wrapping back to
// sortDefault after the last entry.
func (m *Model) cycleSort() {
	cur := m.sort[m.view]
	next := cur + 1
	if next > sortByLastPromo {
		next = sortDefault
	}
	m.sort[m.view] = next
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
		case sortByAge:
			if !out[i].Created.Equal(out[j].Created) {
				return out[i].Created.After(out[j].Created)
			}
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
// view's sort mode. Only sortByName and sortByAge are meaningful here;
// other modes fall through to insertion order.
func (m *Model) sortFreights(in []kargo.Freight) []kargo.Freight {
	mode := m.sort[m.view]
	if mode == sortDefault {
		return in
	}
	out := make([]kargo.Freight, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool {
		switch mode {
		case sortByName:
			return out[i].Name < out[j].Name
		case sortByAge:
			if !out[i].Created.Equal(out[j].Created) {
				return out[i].Created.After(out[j].Created)
			}
			return out[i].Name < out[j].Name
		}
		return false
	})
	return out
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
