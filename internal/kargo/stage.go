package kargo

import (
	"context"
	"fmt"
	"time"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// ListStages loads all Stages in the given namespace using the user's
// kubeconfig.
func ListStages(ctx context.Context, namespace string) ([]Stage, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}

	var sl kargoapi.StageList
	if err := c.List(ctx, &sl, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list stages in %q: %w", namespace, err)
	}

	out := make([]Stage, 0, len(sl.Items))
	for _, s := range sl.Items {
		var (
			health       string
			healthIssues []string
			argoApps     []ArgoCDAppRef
		)
		if s.Status.Health != nil {
			health = string(s.Status.Health.Status)
			healthIssues = s.Status.Health.Issues
			if s.Status.Health.Output != nil {
				argoApps = parseArgoApps(s.Status.Health.Output.Raw)
			}
		}
		var (
			lastPromo     string
			lastPromoName string
			lastPromoAt   time.Time
		)
		if s.Status.LastPromotion != nil {
			lastPromoName = s.Status.LastPromotion.Name
			if s.Status.LastPromotion.Status != nil {
				lastPromo = string(s.Status.LastPromotion.Status.Phase)
			}
			if s.Status.LastPromotion.FinishedAt != nil {
				lastPromoAt = s.Status.LastPromotion.FinishedAt.Time
			}
		}

		var current []string
		if len(s.Status.FreightHistory) > 0 {
			head := s.Status.FreightHistory[0]
			if head != nil {
				for _, fr := range head.Freight {
					current = append(current, fr.Name)
				}
			}
		}

		out = append(out, Stage{
			Name:           s.Name,
			Namespace:      s.Namespace,
			Shard:          s.Spec.Shard,
			IsControlFlow:  s.IsControlFlow(),
			Health:         health,
			HealthIssues:   healthIssues,
			FreightSummary: s.Status.FreightSummary,
			LastPromo:      lastPromo,
			LastPromoName:  lastPromoName,
			LastPromoAt:    lastPromoAt,
			CurrentFreight: current,
			ArgoCDApps:     argoApps,
			Created:        s.CreationTimestamp.Time,
			Labels:         s.Labels,
		})
	}
	return out, nil
}
