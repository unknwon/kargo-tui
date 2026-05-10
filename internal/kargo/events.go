package kargo

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"

	svcv1alpha1 "unknwon.dev/kargo-tui/internal/kargoapi/svc"
)

// PromotionEntry is a flattened view of a kargoapi.Promotion record.
type PromotionEntry struct {
	Name        string
	Stage       string
	Freight     string
	Phase       string
	Message     string
	Created     time.Time
	StartedAt   time.Time
	FinishedAt  time.Time
	CurrentStep int32
	Steps       []PromotionStep
}

// PromotionStep is one entry from Promotion.Status.StepExecutionMetadata —
// the per-step status reported by the Kargo controller while a promotion
// runs through its template's ordered step list.
type PromotionStep struct {
	Alias      string
	Status     string
	Message    string
	ErrorCount int32
	StartedAt  time.Time
	FinishedAt time.Time
}

// ulidTimeFromName extracts the timestamp encoded in a ULID embedded in a
// Kargo promotion name. Kargo names promotions "<stage>.<ulid>.<freight>"
// (the ULID is the unique-per-promotion segment). A ULID's first 10
// Crockford-base32 chars encode the millisecond Unix timestamp of when it
// was minted — i.e. when the promotion record was created. Returns the
// zero time when no segment looks like a ULID.
//
// Kept around even after the proto-binary transport switch as a fallback
// when the server *still* hands back a zero creation timestamp for some
// reason (older Kargo versions, etc.).
func ulidTimeFromName(name string) time.Time {
	for _, seg := range strings.Split(name, ".") {
		if len(seg) != 26 {
			continue
		}
		ms, ok := decodeULIDTime(seg[:10])
		if !ok {
			continue
		}
		return time.UnixMilli(int64(ms)).UTC()
	}
	return time.Time{}
}

func decodeULIDTime(s string) (uint64, bool) {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	if len(s) != 10 {
		return 0, false
	}
	var ms uint64
	for i := 0; i < 10; i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		idx := strings.IndexByte(alphabet, c)
		if idx < 0 {
			return 0, false
		}
		ms = ms<<5 | uint64(idx)
	}
	return ms, true
}

// ListPromotionsForStage returns the most recent Promotions targeting the
// given stage, newest first.
func (c *Client) ListPromotionsForStage(ctx context.Context, project, stage string) ([]PromotionEntry, error) {
	if project == "" {
		project = c.project
	}
	stagePtr := &stage
	if stage == "" {
		stagePtr = nil
	}
	req := &svcv1alpha1.ListPromotionsRequest{Project: project, Stage: stagePtr}
	resp := &svcv1alpha1.ListPromotionsResponse{}
	if err := c.rpc.callProto(ctx, "ListPromotions", req, resp); err != nil {
		return nil, err
	}
	out := make([]PromotionEntry, 0, len(resp.Promotions))
	for _, p := range resp.Promotions {
		if p == nil {
			continue
		}
		out = append(out, flattenPromotion(p))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Created.After(out[j].Created)
	})
	return out, nil
}

func flattenPromotion(p *kargoapi.Promotion) PromotionEntry {
	steps := make([]PromotionStep, 0, len(p.Status.StepExecutionMetadata))
	for _, s := range p.Status.StepExecutionMetadata {
		steps = append(steps, PromotionStep{
			Alias:      s.Alias,
			Status:     string(s.Status),
			Message:    s.Message,
			ErrorCount: int32(s.ErrorCount),
			StartedAt:  metaTime(s.StartedAt),
			FinishedAt: metaTime(s.FinishedAt),
		})
	}
	created := p.CreationTimestamp.Time
	if created.IsZero() {
		// Defensive fallback: if a future Kargo bump regresses on
		// timestamps again, we still get a real moment from the ULID.
		created = ulidTimeFromName(p.Name)
	}
	started := metaTime(p.Status.StartedAt)
	if started.IsZero() {
		started = created
	}
	return PromotionEntry{
		Name:        p.Name,
		Stage:       p.Spec.Stage,
		Freight:     p.Spec.Freight,
		Phase:       string(p.Status.Phase),
		Message:     p.Status.Message,
		Created:     created,
		StartedAt:   started,
		FinishedAt:  metaTime(p.Status.FinishedAt),
		CurrentStep: int32(p.Status.CurrentStep),
		Steps:       steps,
	}
}

// metaTime extracts a plain time.Time from a metav1.Time value or pointer.
// Returns zero when the input is nil, the zero metav1.Time, or doesn't
// expose UTC(). Safe to call with a nil *metav1.Time because that type's
// IsZero method handles a nil receiver.
func metaTime(t interface{ IsZero() bool }) time.Time {
	if t == nil || t.IsZero() {
		return time.Time{}
	}
	if v, ok := t.(interface{ UTC() time.Time }); ok {
		return v.UTC()
	}
	return time.Time{}
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
//
// Events stay on the JSON transport instead of binary proto: the corev1.Event
// type lives in k8s.io/api/core/v1 which is gogo-proto v1 and not vendor-
// patchable to v2 the way Kargo's own messages are. Since the wire payload
// for events is mostly nulls anyway under the current Kargo server, the
// transport choice doesn't change what we can show. ULID-from-name remains
// the meaningful timestamp source for promotion-attached events.
func (c *Client) ListEventsForStage(ctx context.Context, project, stage string) ([]EventEntry, error) {
	if project == "" {
		project = c.project
	}
	req := struct {
		Project string `json:"project"`
	}{Project: project}
	var resp struct {
		Events []rawEvent `json:"events"`
	}
	if err := c.rpc.call(ctx, "ListProjectEvents", req, &resp); err != nil {
		return nil, err
	}
	out := make([]EventEntry, 0, len(resp.Events))
	stageLower := strings.ToLower(stage)
	for _, e := range resp.Events {
		obj := strings.ToLower(e.InvolvedObject.Name)
		if obj != stageLower && !strings.HasPrefix(obj, stageLower+".") {
			continue
		}
		out = append(out, EventEntry{
			Type:    e.Type,
			Reason:  e.Reason,
			Message: e.Message,
			Count:   e.Count,
			Last:    eventLastTime(&e),
			Source:  e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Last.After(out[j].Last)
	})
	return out, nil
}

// rawEvent mirrors the corev1.Event JSON shape for the JSON event path.
type rawEvent struct {
	Type           string         `json:"type"`
	Reason         string         `json:"reason"`
	Message        string         `json:"message"`
	Count          int32          `json:"count"`
	FirstTimestamp jsonTimeString `json:"firstTimestamp"`
	LastTimestamp  jsonTimeString `json:"lastTimestamp"`
	Metadata       struct {
		CreationTimestamp jsonTimeString `json:"creationTimestamp"`
	} `json:"metadata"`
	InvolvedObject struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"involvedObject"`
}

// jsonTimeString decodes an RFC3339 timestamp string, tolerating empty
// objects and nulls. We don't bother handling the {seconds,nanos} shape
// since we only use this for JSON-path events and the JSON encoder never
// uses that form.
type jsonTimeString struct{ time.Time }

func (t *jsonTimeString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" || string(data) == "{}" {
		t.Time = time.Time{}
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		if s == "" {
			t.Time = time.Time{}
			return nil
		}
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Time = time.Time{}
			return nil
		}
		t.Time = parsed
		return nil
	}
	t.Time = time.Time{}
	return nil
}

func eventLastTime(e *rawEvent) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.Metadata.CreationTimestamp.IsZero() {
		return e.Metadata.CreationTimestamp.Time
	}
	if !e.FirstTimestamp.IsZero() {
		return e.FirstTimestamp.Time
	}
	return ulidTimeFromName(e.InvolvedObject.Name)
}
