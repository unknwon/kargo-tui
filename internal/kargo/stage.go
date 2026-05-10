package kargo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/akuity/kargo/pkg/client/generated/core"
	"github.com/akuity/kargo/pkg/client/generated/models"
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
}

// ListStages loads all Stages in the given project via the Kargo API server.
// An empty project falls back to the client's default.
func (c *Client) ListStages(ctx context.Context, project string) ([]Stage, error) {
	if project == "" {
		project = c.project
	}
	params := core.NewListStagesParams().WithContext(ctx)
	params.Project = project
	resp, err := c.api.Core.ListStages(params, c.authInfo)
	if err != nil {
		return nil, fmt.Errorf("list stages in %q: %w", project, err)
	}
	if resp.Payload == nil {
		return nil, nil
	}

	items := resp.Payload.Items
	out := make([]Stage, 0, len(items))
	for _, s := range items {
		if s == nil || s.Metadata == nil {
			continue
		}

		health := s.Status.Health.Status
		healthIssues := s.Status.Health.Issues
		var argoApps []ArgoCDAppRef
		if s.Status.Health.Output != nil {
			if raw, err := json.Marshal(s.Status.Health.Output); err == nil {
				argoApps = parseArgoApps(raw)
			}
		}

		lastPromo := s.Status.LastPromotion.Status.Phase
		lastPromoName := s.Status.LastPromotion.Name
		lastPromoAt := parseTime(s.Status.LastPromotion.FinishedAt)

		out = append(out, Stage{
			Name:           s.Metadata.Name,
			Namespace:      s.Metadata.Namespace,
			Shard:          s.Spec.Shard,
			IsControlFlow:  isControlFlow(s),
			Health:         health,
			HealthIssues:   healthIssues,
			FreightSummary: s.Status.FreightSummary,
			LastPromo:      lastPromo,
			LastPromoName:  lastPromoName,
			LastPromoAt:    lastPromoAt,
			CurrentFreight: currentFreightNames(s.Status.FreightHistory),
			ArgoCDApps:     argoApps,
			Created:        parseTime(s.Metadata.CreationTimestamp),
			Labels:         s.Metadata.Labels,
		})
	}
	return out, nil
}

// isControlFlow returns true when the stage has no PromotionTemplate steps —
// the same definition as kargoapi.Stage.IsControlFlow().
func isControlFlow(s *models.Stage) bool {
	tmpl := s.Spec.PromotionTemplate.PromotionTemplate
	if tmpl.Spec == nil {
		return true
	}
	return len(tmpl.Spec.Steps) == 0
}

// currentFreightNames extracts the names of freight in the most recent entry
// of the FreightHistory list.
func currentFreightNames(history []*models.FreightCollection) []string {
	if len(history) == 0 || history[0] == nil {
		return nil
	}
	out := make([]string, 0, len(history[0].Items))
	for _, ref := range history[0].Items {
		if ref.Name == "" {
			continue
		}
		out = append(out, ref.Name)
	}
	return out
}
