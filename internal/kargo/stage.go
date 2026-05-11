package kargo

import (
	"context"
	"sort"
	"time"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"

	svcv1alpha1 "unknwon.dev/kargo-tui/internal/kargoapi/svc"
)

// Stage is a flattened, UI-friendly view of a kargoapi.Stage.
type Stage struct {
	Name           string
	Namespace      string
	Shard          string
	IsControlFlow  bool
	Health         string // Healthy / Unhealthy / Progressing / Unknown / NotApplicable / ""
	HealthIssues   []string
	FreightSummary string
	LastPromo      string // Succeeded / Failed / Running / ...
	LastPromoName  string
	LastPromoAt    time.Time
	CurrentFreight []string // freight names currently deployed
	ArgoCDApps     []ArgoCDAppRef
	Created        time.Time
	Labels         map[string]string
	// Upstreams is the deduplicated set of upstream stage names referenced
	// by Spec.RequestedFreight[].Sources.Stages — used to build the tree
	// view's parent/child relationships.
	Upstreams []string
	// DirectWarehouses lists every Warehouse this stage pulls freight
	// directly from (Spec.RequestedFreight[].Sources.Direct == true).
	// Used by the promote picker to surface warehouse-origin freight on
	// stages with no upstream stages.
	DirectWarehouses []string
}

// ListStages loads all Stages in the given project via the Kargo API server.
// An empty project falls back to the client's default. Results are sorted
// newest-first with name as tiebreaker so refreshes don't reshuffle stages
// that share a creation timestamp.
//
// Uses Connect-RPC binary protobuf rather than JSON because the Kargo
// server's proto-JSON encoder elides every metav1.Time value to `{}` —
// see internal/kargoapi/README.md for the full story.
func (c *Client) ListStages(ctx context.Context, project string) ([]Stage, error) {
	if project == "" {
		project = c.project
	}
	req := &svcv1alpha1.ListStagesRequest{Project: project}
	resp := &svcv1alpha1.ListStagesResponse{}
	if err := c.rpc.callProto(ctx, "ListStages", req, resp); err != nil {
		return nil, err
	}

	out := make([]Stage, 0, len(resp.Stages))
	for _, s := range resp.Stages {
		if s == nil {
			continue
		}
		stage := flattenStage(s)
		out = append(out, stage)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// flattenStage projects a single kargoapi.Stage into the TUI's Stage view.
func flattenStage(s *kargoapi.Stage) Stage {
	var (
		health       string
		healthIssues []string
		argoApps     []ArgoCDAppRef
	)
	if s.Status.Health != nil {
		health = string(s.Status.Health.Status)
		healthIssues = s.Status.Health.Issues
		// Output is a *apiextensionsv1.JSON — a nil pointer when the
		// stage's health checks produced no opaque output (common for
		// control-flow stages and any stage whose health probe hasn't
		// run yet). Guard before reading Raw.
		if s.Status.Health.Output != nil && len(s.Status.Health.Output.Raw) > 0 {
			argoApps = parseArgoApps(s.Status.Health.Output.Raw)
		}
	}

	var (
		lastPromo     string
		lastPromoName string
		lastPromoAt   time.Time
	)
	if lp := s.Status.LastPromotion; lp != nil {
		lastPromoName = lp.Name
		if !lp.FinishedAt.IsZero() {
			lastPromoAt = lp.FinishedAt.Time
		}
		if lp.Status != nil {
			lastPromo = string(lp.Status.Phase)
			if lastPromoAt.IsZero() && !lp.Status.FinishedAt.IsZero() {
				lastPromoAt = lp.Status.FinishedAt.Time
			}
		}
	}

	// Resolve currently-deployed freight names from the most recent
	// freight-history entry.
	var current []string
	if len(s.Status.FreightHistory) > 0 {
		for _, ref := range s.Status.FreightHistory[0].Freight {
			if ref.Name != "" {
				current = append(current, ref.Name)
			}
		}
	}
	if len(current) == 0 && s.Status.LastPromotion != nil && s.Status.LastPromotion.Freight != nil {
		if n := s.Status.LastPromotion.Freight.Name; n != "" {
			current = append(current, n)
		}
	}

	upstreams := upstreamStages(s)
	directWarehouses := directWarehouses(s)

	return Stage{
		Name:             s.Name,
		Namespace:        s.Namespace,
		Shard:            s.Spec.Shard,
		IsControlFlow:    s.IsControlFlow(),
		Health:           health,
		HealthIssues:     healthIssues,
		FreightSummary:   s.Status.FreightSummary,
		LastPromo:        lastPromo,
		LastPromoName:    lastPromoName,
		LastPromoAt:      lastPromoAt,
		CurrentFreight:   current,
		ArgoCDApps:       argoApps,
		Created:          s.CreationTimestamp.Time,
		Labels:           s.Labels,
		Upstreams:        upstreams,
		DirectWarehouses: directWarehouses,
	}
}

// directWarehouses collects the deduplicated warehouse names this stage
// pulls freight directly from (RequestedFreight entries with
// Sources.Direct == true).
func directWarehouses(s *kargoapi.Stage) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, req := range s.Spec.RequestedFreight {
		if !req.Sources.Direct {
			continue
		}
		name := req.Origin.Name
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// upstreamStages collects the deduplicated upstream stage names from every
// FreightRequest on the stage spec. A stage with no upstream Stages entries
// (e.g. one that pulls Direct from a Warehouse) returns an empty slice.
func upstreamStages(s *kargoapi.Stage) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, req := range s.Spec.RequestedFreight {
		for _, name := range req.Sources.Stages {
			if name == "" {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
