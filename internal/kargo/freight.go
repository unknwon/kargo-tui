package kargo

import (
	"context"
	"sort"
	"time"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"

	svcv1alpha1 "unknwon.dev/kargo-tui/internal/kargoapi/svc"
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
	RepoURL     string
	Tag         string
	Digest      string // sha256:…
	Annotations map[string]string
}

// FreightChart is a single Helm chart reference pinned by a Freight.
type FreightChart struct {
	RepoURL string
	Name    string
	Version string
}

// ListFreight loads all Freight in the given project sorted newest-first.
// QueryFreight with no group_by groups every result under a single
// empty-string key, giving us a flat list.
func (c *Client) ListFreight(ctx context.Context, project string) ([]Freight, error) {
	if project == "" {
		project = c.project
	}
	req := &svcv1alpha1.QueryFreightRequest{Project: project}
	resp := &svcv1alpha1.QueryFreightResponse{}
	if err := c.rpc.callProto(ctx, "QueryFreight", req, resp); err != nil {
		return nil, err
	}

	out := make([]Freight, 0)
	seen := make(map[string]struct{})
	for _, group := range resp.Groups {
		if group == nil {
			continue
		}
		for _, f := range group.Freight {
			if f == nil || f.Name == "" {
				continue
			}
			if _, dup := seen[f.Name]; dup {
				continue
			}
			seen[f.Name] = struct{}{}
			out = append(out, flattenFreight(f))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func flattenFreight(f *kargoapi.Freight) Freight {
	warehouse := f.Origin.Name
	if warehouse == "" {
		warehouse = f.Labels["kargo.akuity.io/warehouse"]
	}

	commits := make([]FreightCommit, 0, len(f.Commits))
	for _, cm := range f.Commits {
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
		images = append(images, FreightImage{
			RepoURL:     i.RepoURL,
			Tag:         i.Tag,
			Digest:      i.Digest,
			Annotations: i.Annotations,
		})
	}
	charts := make([]FreightChart, 0, len(f.Charts))
	for _, ch := range f.Charts {
		charts = append(charts, FreightChart{
			RepoURL: ch.RepoURL,
			Name:    ch.Name,
			Version: ch.Version,
		})
	}

	return Freight{
		Name:           f.Name,
		Alias:          f.Alias,
		Namespace:      f.Namespace,
		Warehouse:      warehouse,
		Created:        f.CreationTimestamp.Time,
		VerifiedIn:     len(f.Status.VerifiedIn),
		ApprovedFor:    len(f.Status.ApprovedFor),
		VerifiedStages: stageMapKeys(f.Status.VerifiedIn),
		ApprovedStages: stageMapKeys(f.Status.ApprovedFor),
		CurrentlyIn:    stageMapKeys(f.Status.CurrentlyIn),
		Commits:        commits,
		Images:         images,
		Charts:         charts,
		Labels:         f.Labels,
	}
}

// stageMapKeys returns the sorted keys of a map keyed by stage name.
// Generic helper that abstracts over the various status-map value types.
func stageMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
