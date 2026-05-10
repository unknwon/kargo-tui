package kargo

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/akuity/kargo/pkg/client/generated/core"
	"github.com/akuity/kargo/pkg/client/generated/models"
)

// Freight is a flattened, UI-friendly view of a kargoapi.Freight.
type Freight struct {
	Name        string
	Alias       string
	Namespace   string
	Warehouse   string
	Created     time.Time
	VerifiedIn  int
	ApprovedFor int

	VerifiedStages []string
	ApprovedStages []string
	CurrentlyIn    []string
	Commits        []FreightCommit
	Images         []FreightImage
	Charts         []FreightChart
	Labels         map[string]string
}

// FreightCommit is a single git commit pinned by a Freight.
type FreightCommit struct {
	RepoURL string
	ID      string // full SHA
	Branch  string
	Tag     string
	Message string
	Author  string
}

// FreightImage is a single OCI image reference pinned by a Freight.
type FreightImage struct {
	RepoURL string
	Tag     string
	Digest  string // sha256:…
}

// FreightChart is a single Helm chart reference pinned by a Freight.
type FreightChart struct {
	RepoURL string
	Name    string
	Version string
}

// ListFreight loads all Freight in the given project sorted newest-first.
// QueryFreightsRest with no group_by returns a flat list of freight under a
// single empty-string group key.
func (c *Client) ListFreight(ctx context.Context, project string) ([]Freight, error) {
	if project == "" {
		project = c.project
	}
	params := core.NewQueryFreightsRestParams().WithContext(ctx)
	params.Project = project
	resp, err := c.api.Core.QueryFreightsRest(params, c.authInfo)
	if err != nil {
		return nil, fmt.Errorf("list freight in %q: %w", project, err)
	}
	if resp.Payload == nil {
		return nil, nil
	}

	// Payload is unstructured (`any`); the server returns
	// {"groups": {"": {"freight": [...]}}}. Round-trip through JSON to land
	// in the typed FreightList model.
	raw, err := json.Marshal(resp.Payload)
	if err != nil {
		return nil, fmt.Errorf("marshal freight payload: %w", err)
	}
	var envelope struct {
		Groups map[string]struct {
			Freight []*models.Freight `json:"freight"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode freight payload: %w", err)
	}

	out := make([]Freight, 0)
	seen := make(map[string]struct{})
	for _, group := range envelope.Groups {
		for _, f := range group.Freight {
			if f == nil || f.Metadata == nil {
				continue
			}
			if _, dup := seen[f.Metadata.Name]; dup {
				continue
			}
			seen[f.Metadata.Name] = struct{}{}

			warehouse := ""
			if f.Origin.Name != nil {
				warehouse = *f.Origin.Name
			}
			if warehouse == "" {
				warehouse = f.Metadata.Labels["kargo.akuity.io/warehouse"]
			}

			commits := make([]FreightCommit, 0, len(f.Commits))
			for _, cm := range f.Commits {
				if cm == nil {
					continue
				}
				commits = append(commits, FreightCommit{
					RepoURL: cm.RepoURL,
					ID:      cm.ID,
					Branch:  cm.Branch,
					Tag:     cm.Tag,
					Message: cm.Message,
					Author:  cm.Author,
				})
			}
			images := make([]FreightImage, 0, len(f.Images))
			for _, i := range f.Images {
				if i == nil {
					continue
				}
				images = append(images, FreightImage{
					RepoURL: i.RepoURL,
					Tag:     i.Tag,
					Digest:  i.Digest,
				})
			}
			charts := make([]FreightChart, 0, len(f.Charts))
			for _, ch := range f.Charts {
				if ch == nil {
					continue
				}
				charts = append(charts, FreightChart{
					RepoURL: ch.RepoURL,
					Name:    ch.Name,
					Version: ch.Version,
				})
			}

			out = append(out, Freight{
				Name:           f.Metadata.Name,
				Alias:          f.Alias,
				Namespace:      f.Metadata.Namespace,
				Warehouse:      warehouse,
				Created:        parseTime(f.Metadata.CreationTimestamp),
				VerifiedIn:     len(f.Status.FreightStatus.VerifiedIn),
				ApprovedFor:    len(f.Status.FreightStatus.ApprovedFor),
				VerifiedStages: mapKeys(f.Status.FreightStatus.VerifiedIn),
				ApprovedStages: mapKeys(f.Status.FreightStatus.ApprovedFor),
				CurrentlyIn:    mapKeys(f.Status.FreightStatus.CurrentlyIn),
				Commits:        commits,
				Images:         images,
				Charts:         charts,
				Labels:         f.Metadata.Labels,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Created.After(out[j].Created)
	})
	return out, nil
}
