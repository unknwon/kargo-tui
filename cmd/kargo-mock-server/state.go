package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
)

// store is the in-memory backing for every RPC. One projectState per
// project. All mutations go through methods on store so the WatchStages
// broadcaster gets notified consistently.
type store struct {
	mu       sync.RWMutex
	projects map[string]*projectState
	order    []string // stable iteration order matching topology load order
}

// projectState holds one project's stages, freight, promotions, events,
// and the per-project subscriber set for WatchStages.
type projectState struct {
	name       string
	topology   *topology
	stages     map[string]*kargoapi.Stage   // keyed by stage name
	freight    map[string]*kargoapi.Freight // keyed by freight name
	promotions []*kargoapi.Promotion        // newest-first; mutate under store.mu
	events     []rawEventJSON               // JSON-shape for the JSON ListProjectEvents path

	subsMu      sync.Mutex
	subscribers map[*streamSubscriber]struct{}
}

func newStore() *store {
	return &store{projects: make(map[string]*projectState)}
}

func (s *store) addProject(p *projectState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects[p.name] = p
	s.order = append(s.order, p.name)
}

func (s *store) project(name string) (*projectState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[name]
	return p, ok
}

func (s *store) projectNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.order))
	copy(out, s.order)
	return out
}

func (s *store) projectSummary() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	parts := make([]string, 0, len(s.order))
	for _, name := range s.order {
		p := s.projects[name]
		parts = append(parts, fmt.Sprintf("%s (%d stages, %d freight, %d promotions)",
			name, len(p.stages), len(p.freight), len(p.promotions)))
	}
	return strings.Join(parts, ", ")
}

// listStages returns a snapshot of the project's stages sorted newest-first.
func (p *projectState) listStages() []*kargoapi.Stage {
	out := make([]*kargoapi.Stage, 0, len(p.stages))
	for _, s := range p.stages {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].CreationTimestamp.Time, out[j].CreationTimestamp.Time
		if !a.Equal(b) {
			return a.After(b)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// listFreight returns a snapshot of the project's freight sorted newest-first.
func (p *projectState) listFreight() []*kargoapi.Freight {
	out := make([]*kargoapi.Freight, 0, len(p.freight))
	for _, f := range p.freight {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].CreationTimestamp.Time, out[j].CreationTimestamp.Time
		if !a.Equal(b) {
			return a.After(b)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// listPromotions returns a snapshot of the project's promotions filtered to
// the given stage (empty stage = all). Sorted newest-first.
func (p *projectState) listPromotions(stage string) []*kargoapi.Promotion {
	out := make([]*kargoapi.Promotion, 0, len(p.promotions))
	for _, pr := range p.promotions {
		if stage != "" && pr.Spec.Stage != stage {
			continue
		}
		out = append(out, pr)
	}
	return out
}
