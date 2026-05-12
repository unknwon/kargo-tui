package main

import (
	"fmt"
	"time"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// runPromote kicks off a single Pending → Running → Succeeded promotion
// against one stage. The lifecycle runs in a goroutine so the HTTP
// handler returns immediately with the Pending promotion (mirroring real
// Kargo). Each phase transition broadcasts a stage update.
func (h *handlers) runPromote(p *projectState, stageName, freightName, actor string) (*kargoapi.Promotion, error) {
	h.store.mu.Lock()
	stage, ok := p.stages[stageName]
	if !ok {
		h.store.mu.Unlock()
		return nil, fmt.Errorf("stage %q not found", stageName)
	}
	freight, ok := p.freight[freightName]
	if !ok {
		h.store.mu.Unlock()
		return nil, fmt.Errorf("freight %q not found", freightName)
	}

	now := time.Now().UTC()
	promo := newPromotion(p.name, stage, freight, now)
	p.promotions = append([]*kargoapi.Promotion{promo}, p.promotions...)
	// Reflect the running promo on the stage immediately.
	stage.Status.CurrentPromotion = &kargoapi.PromotionReference{
		Name: promo.Name,
		Freight: &kargoapi.FreightReference{
			Name:   freight.Name,
			Origin: freight.Origin,
		},
	}
	p.events = append(p.events, newEvent("Promotion", promo.Name, "PromotionCreated",
		fmt.Sprintf("Promotion to stage %s started by %s", stageName, actor), now))
	stageCopy := *stage
	h.store.mu.Unlock()
	p.broadcastStage(&stageCopy)

	go h.advancePromotion(p, promo.Name)
	return promo, nil
}

// advancePromotion walks one promotion through Pending → Running →
// Succeeded with realistic delays, broadcasting a stage update at each
// transition. Always succeeds (per current product decision).
func (h *handlers) advancePromotion(p *projectState, promoName string) {
	speed := h.speed
	if speed <= 0 {
		speed = 1.0
	}
	step := func(d time.Duration) time.Duration { return time.Duration(float64(d) / speed) }

	// Pending → Running
	time.Sleep(step(1500 * time.Millisecond))
	h.transitionPromotion(p, promoName, kargoapi.PromotionPhaseRunning)

	// Running → Succeeded
	time.Sleep(step(2500 * time.Millisecond))
	h.transitionPromotion(p, promoName, kargoapi.PromotionPhaseSucceeded)
}

func (h *handlers) transitionPromotion(p *projectState, promoName string, phase kargoapi.PromotionPhase) {
	h.store.mu.Lock()
	var promo *kargoapi.Promotion
	for _, pr := range p.promotions {
		if pr.Name == promoName {
			promo = pr
			break
		}
	}
	if promo == nil {
		h.store.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	promo.Status.Phase = phase
	if phase == kargoapi.PromotionPhaseRunning && promo.Status.StartedAt == nil {
		t := metav1.NewTime(now)
		promo.Status.StartedAt = &t
	}
	stage, ok := p.stages[promo.Spec.Stage]
	if !ok {
		h.store.mu.Unlock()
		return
	}
	freight := p.freight[promo.Spec.Freight]
	switch phase {
	case kargoapi.PromotionPhaseRunning:
		stage.Status.CurrentPromotion = &kargoapi.PromotionReference{
			Name:    promo.Name,
			Freight: refForFreight(freight),
			Status:  &kargoapi.PromotionStatus{Phase: kargoapi.PromotionPhaseRunning},
		}
	case kargoapi.PromotionPhaseSucceeded:
		t := metav1.NewTime(now)
		promo.Status.FinishedAt = &t
		stage.Status.CurrentPromotion = nil
		stage.Status.LastPromotion = &kargoapi.PromotionReference{
			Name:       promo.Name,
			Freight:    refForFreight(freight),
			Status:     &kargoapi.PromotionStatus{Phase: kargoapi.PromotionPhaseSucceeded, FinishedAt: &t},
			FinishedAt: &t,
		}
		// Roll the freight into the stage's FreightHistory.
		if freight != nil {
			ref := *refForFreight(freight)
			coll := &kargoapi.FreightCollection{}
			coll.UpdateOrPush(ref)
			stage.Status.FreightHistory.Record(coll)
			stage.Status.FreightSummary = ref.Name
			if stage.Status.Health == nil {
				stage.Status.Health = &kargoapi.Health{}
			}
			stage.Status.Health.Status = kargoapi.HealthStateHealthy
			stage.Status.Health.Issues = nil

			// Mark freight verified in this stage.
			freight.Status.AddVerifiedStage(stage.Name, now)
			freight.Status.AddCurrentStage(stage.Name, now)
		}
		p.events = append(p.events, newEvent("Promotion", promo.Name, "PromotionSucceeded",
			fmt.Sprintf("Promotion to stage %s succeeded", stage.Name), now))
	}
	stageCopy := *stage
	h.store.mu.Unlock()
	p.broadcastStage(&stageCopy)
}

// runApprove flips the approved-for bit on a freight. Instant in real
// Kargo too.
func (h *handlers) runApprove(p *projectState, freightName, stageName string) error {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	freight, ok := p.freight[freightName]
	if !ok {
		return fmt.Errorf("freight %q not found", freightName)
	}
	if _, ok := p.stages[stageName]; !ok {
		return fmt.Errorf("stage %q not found", stageName)
	}
	freight.Status.AddApprovedStage(stageName, time.Now().UTC())
	p.events = append(p.events, newEvent("Freight", freightName, "FreightApproved",
		fmt.Sprintf("Freight approved for stage %s", stageName), time.Now().UTC()))
	return nil
}

// runPromoteDownstream walks the topology BFS from sourceStage and kicks
// off one promotion per downstream stage, staggered so the graph
// cascades visibly. This is the wow demo.
func (h *handlers) runPromoteDownstream(p *projectState, sourceStage, freightName string) []*kargoapi.Promotion {
	h.store.mu.RLock()
	downstreams := p.topology.downstreamsOf(sourceStage)
	h.store.mu.RUnlock()

	var promos []*kargoapi.Promotion
	for i, ds := range downstreams {
		// Stagger initial kickoff so the cascade is visible. Delay
		// scales inversely with speed.
		delay := time.Duration(i) * 800 * time.Millisecond
		if h.speed > 0 {
			delay = time.Duration(float64(delay) / h.speed)
		}
		dsName := ds
		go func() {
			if delay > 0 {
				time.Sleep(delay)
			}
			_, _ = h.runPromote(p, dsName, freightName, "downstream")
		}()
	}
	// Return Pending placeholders so the TUI gets a non-empty response.
	now := time.Now().UTC()
	h.store.mu.RLock()
	for _, ds := range downstreams {
		stage := p.stages[ds]
		freight := p.freight[freightName]
		if stage == nil || freight == nil {
			continue
		}
		promos = append(promos, newPromotion(p.name, stage, freight, now))
	}
	h.store.mu.RUnlock()
	return promos
}

func refForFreight(f *kargoapi.Freight) *kargoapi.FreightReference {
	if f == nil {
		return &kargoapi.FreightReference{}
	}
	return &kargoapi.FreightReference{
		Name:    f.Name,
		Origin:  f.Origin,
		Commits: f.Commits,
		Images:  f.Images,
		Charts:  f.Charts,
	}
}
