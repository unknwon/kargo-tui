package main

import (
	"context"
	"math/rand/v2"
	"time"
)

// startMotion spins up one background goroutine per project that
// continuously promotes random freight through random stages. This is
// what makes a screen recording feel alive without any user input.
func startMotion(ctx context.Context, s *store, speed float64) {
	if speed <= 0 {
		speed = 1.0
	}
	h := &handlers{store: s, speed: speed}
	for _, name := range s.projectNames() {
		p, _ := s.project(name)
		if p == nil {
			continue
		}
		go runMotion(ctx, h, p, speed)
	}
}

func runMotion(ctx context.Context, h *handlers, p *projectState, speed float64) {
	// Seed per-project so projects don't all promote in lockstep.
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), uint64(len(p.name)))) //nolint:gosec
	for {
		// Promote cadence: 4-10 seconds, scaled by speed.
		base := time.Duration(4000+rng.IntN(6000)) * time.Millisecond
		wait := time.Duration(float64(base) / speed)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		h.store.mu.RLock()
		var stageNames []string
		for name := range p.stages {
			stageNames = append(stageNames, name)
		}
		var freightNames []string
		for name := range p.freight {
			freightNames = append(freightNames, name)
		}
		h.store.mu.RUnlock()
		if len(stageNames) == 0 || len(freightNames) == 0 {
			continue
		}
		stage := stageNames[rng.IntN(len(stageNames))]
		freight := freightNames[rng.IntN(len(freightNames))]
		_, _ = h.runPromote(p, stage, freight, "auto")
	}
}
