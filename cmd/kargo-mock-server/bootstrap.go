package main

import (
	"math/rand/v2"
	"path/filepath"

	"github.com/cockroachdb/errors"
)

// bootstrap loads every topology YAML under fixturesDir, then runs the
// procedural volume generator against each one to populate freight,
// promotions, and events. Returns a store ready to serve RPCs.
func bootstrap(fixturesDir string, seed int64) (*store, error) {
	topologyFiles := []string{"acme-commerce.yaml", "acme-edge.yaml"}
	s := newStore()
	for i, name := range topologyFiles {
		path := filepath.Join(fixturesDir, name)
		topo, err := loadTopology(path)
		if err != nil {
			return nil, errors.Wrapf(err, "load %s", path)
		}
		// Seed each project deterministically off the global seed plus an
		// offset so a single --seed change reshuffles every project but the
		// projects don't all share the same RNG stream.
		rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)+uint64(i+1)*0x9E3779B97F4A7C15)) //nolint:gosec
		ps := buildProjectState(topo, rng)
		s.addProject(ps)
	}
	return s, nil
}
