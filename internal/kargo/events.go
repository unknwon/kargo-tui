package kargo

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/akuity/kargo/pkg/client/generated/core"
	"github.com/akuity/kargo/pkg/client/generated/events"
	"github.com/akuity/kargo/pkg/client/generated/models"
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
func (c *Client) ListPromotionsForStage(ctx context.Context, project, stage string) ([]PromotionEntry, error) {
	if project == "" {
		project = c.project
	}
	params := core.NewListPromotionsParams().WithContext(ctx)
	params.Project = project
	params.Stage = stringPtr(stage)
	resp, err := c.api.Core.ListPromotions(params, c.authInfo)
	if err != nil {
		return nil, fmt.Errorf("list promotions: %w", err)
	}
	if resp.Payload == nil {
		return nil, nil
	}
	items := resp.Payload.Items
	out := make([]PromotionEntry, 0, len(items))
	for _, p := range items {
		if p == nil || p.Metadata == nil {
			continue
		}
		entry := PromotionEntry{
			Name:    p.Metadata.Name,
			Phase:   p.Status.PromotionStatus.Phase,
			Message: p.Status.PromotionStatus.Message,
			Created: parseTime(p.Metadata.CreationTimestamp),
		}
		if p.Spec.PromotionSpec.Stage != nil {
			entry.Stage = *p.Spec.PromotionSpec.Stage
		}
		if p.Spec.PromotionSpec.Freight != nil {
			entry.Freight = *p.Spec.PromotionSpec.Freight
		}
		entry.StartedAt = parseTime(p.Status.PromotionStatus.StartedAt)
		entry.FinishedAt = parseTime(p.Status.PromotionStatus.FinishedAt)
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
func (c *Client) ListEventsForStage(ctx context.Context, project, stage string) ([]EventEntry, error) {
	if project == "" {
		project = c.project
	}
	params := events.NewListProjectEventsParams().WithContext(ctx)
	params.Project = project
	resp, err := c.api.Events.ListProjectEvents(params, c.authInfo)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	if resp.Payload == nil {
		return nil, nil
	}
	items := resp.Payload.Items
	out := make([]EventEntry, 0, len(items))
	stageLower := strings.ToLower(stage)
	for _, e := range items {
		if e == nil {
			continue
		}
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
			Count:   int32(e.Count),
			Last:    eventLastTime(e),
			Source:  e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Last.After(out[j].Last)
	})
	return out, nil
}

// eventLastTime returns the most authoritative timestamp on a Kubernetes
// event delivered through the Kargo API.
func eventLastTime(e *models.V1Event) time.Time {
	if t := parseTime(e.LastTimestamp); !t.IsZero() {
		return t
	}
	if e.Metadata.CreationTimestamp != "" {
		return parseTime(e.Metadata.CreationTimestamp)
	}
	return parseTime(e.FirstTimestamp)
}

// parseTime parses an RFC3339 timestamp produced by the Kargo API. Returns
// the zero time on empty input or parse failure.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
