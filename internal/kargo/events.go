package kargo

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PromotionEntry is a flattened view of a kargoapi.Promotion record.
type PromotionEntry struct {
	Name       string
	Stage      string
	Freight    string
	Phase      string
	Message    string
	Created    time.Time
	StartedAt  time.Time
	FinishedAt time.Time
}

// ListPromotionsForStage returns the most recent Promotions targeting the
// given stage, newest first.
func ListPromotionsForStage(ctx context.Context, namespace, stage string) ([]PromotionEntry, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	var pl kargoapi.PromotionList
	if err := c.List(ctx, &pl, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list promotions: %w", err)
	}
	out := make([]PromotionEntry, 0, len(pl.Items))
	for _, p := range pl.Items {
		if p.Spec.Stage != stage {
			continue
		}
		entry := PromotionEntry{
			Name:    p.Name,
			Stage:   p.Spec.Stage,
			Freight: p.Spec.Freight,
			Phase:   string(p.Status.Phase),
			Message: p.Status.Message,
			Created: p.CreationTimestamp.Time,
		}
		if p.Status.StartedAt != nil {
			entry.StartedAt = p.Status.StartedAt.Time
		}
		if p.Status.FinishedAt != nil {
			entry.FinishedAt = p.Status.FinishedAt.Time
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Created.After(out[j].Created)
	})
	return out, nil
}

// EventEntry is a flattened view of a corev1.Event scoped to a Kargo
// resource.
type EventEntry struct {
	Type    string
	Reason  string
	Message string
	Count   int32
	Last    time.Time
	Source  string
}

// ListEventsForStage returns recent Kubernetes events touching the given
// stage (or its promotions), newest first.
func ListEventsForStage(ctx context.Context, namespace, stage string) ([]EventEntry, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	var el corev1.EventList
	if err := c.List(ctx, &el, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	out := make([]EventEntry, 0, len(el.Items))
	stageLower := strings.ToLower(stage)
	for _, e := range el.Items {
		obj := strings.ToLower(e.InvolvedObject.Name)
		// Match events on the Stage itself, or Promotions whose name is
		// prefixed with the stage name (Kargo's promotion naming convention).
		if obj != stageLower && !strings.HasPrefix(obj, stageLower+".") {
			continue
		}
		out = append(out, EventEntry{
			Type:    e.Type,
			Reason:  e.Reason,
			Message: e.Message,
			Count:   e.Count,
			Last:    eventTime(e),
			Source:  e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Last.After(out[j].Last)
	})
	return out, nil
}

// eventTime returns the most authoritative timestamp on a Kubernetes Event.
func eventTime(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}
