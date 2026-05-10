package kargo

import (
	"context"
	"fmt"
	"sort"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ListProjects returns the names of Kargo projects in the cluster. It first
// tries the cluster-scoped Project CRD (the canonical source). If that fails
// (e.g. RBAC denies it), it falls back to namespaces labeled with
// "kargo.akuity.io/project". The two are unioned and de-duplicated, so a
// missing label doesn't hide a Project that exists.
func ListProjects(ctx context.Context) ([]string, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})

	var projList kargoapi.ProjectList
	projErr := c.List(ctx, &projList)
	if projErr == nil {
		for _, p := range projList.Items {
			seen[p.Name] = struct{}{}
		}
	}

	var nsList corev1.NamespaceList
	nsErr := c.List(ctx, &nsList, client.HasLabels{"kargo.akuity.io/project"})
	if nsErr == nil {
		for _, n := range nsList.Items {
			seen[n.Name] = struct{}{}
		}
	}

	if projErr != nil && nsErr != nil {
		return nil, fmt.Errorf("list projects/namespaces: %w (and %v)", projErr, nsErr)
	}

	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}
