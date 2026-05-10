package kargo

import (
	"context"
	"fmt"
	"sort"
	"time"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// ListFreight loads all Freight in the given namespace using the user's
// kubeconfig, sorted newest-first by creation time.
func ListFreight(ctx context.Context, namespace string) ([]Freight, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}

	var fl kargoapi.FreightList
	if err := c.List(ctx, &fl, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list freight in %q: %w", namespace, err)
	}

	out := make([]Freight, 0, len(fl.Items))
	for _, f := range fl.Items {
		warehouse := f.Origin.Name
		if warehouse == "" {
			warehouse = f.Labels["kargo.akuity.io/warehouse"]
		}

		commits := make([]FreightCommit, 0, len(f.Commits))
		for _, c := range f.Commits {
			commits = append(commits, FreightCommit{
				RepoURL: c.RepoURL,
				ID:      c.ID,
				Branch:  c.Branch,
				Tag:     c.Tag,
				Message: c.Message,
				Author:  c.Author,
			})
		}
		images := make([]FreightImage, 0, len(f.Images))
		for _, i := range f.Images {
			images = append(images, FreightImage{
				RepoURL: i.RepoURL,
				Tag:     i.Tag,
				Digest:  i.Digest,
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

		out = append(out, Freight{
			Name:           f.Name,
			Alias:          f.Alias,
			Namespace:      f.Namespace,
			Warehouse:      warehouse,
			Created:        f.CreationTimestamp.Time,
			VerifiedIn:     len(f.Status.VerifiedIn),
			ApprovedFor:    len(f.Status.ApprovedFor),
			VerifiedStages: mapKeys(f.Status.VerifiedIn),
			ApprovedStages: mapKeys(f.Status.ApprovedFor),
			CurrentlyIn:    mapKeys(f.Status.CurrentlyIn),
			Commits:        commits,
			Images:         images,
			Charts:         charts,
			Labels:         f.Labels,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Created.After(out[j].Created)
	})
	return out, nil
}
