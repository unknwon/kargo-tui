package kargo

import (
	"context"
	"sort"

	"github.com/cockroachdb/errors"
)

// ListProjects returns the names of Kargo projects accessible to the
// authenticated user, sorted alphabetically.
func (c *Client) ListProjects(ctx context.Context) ([]string, error) {
	var resp struct {
		Projects []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"projects"`
	}
	if err := c.rpc.call(ctx, "ListProjects", struct{}{}, &resp); err != nil {
		return nil, errors.Wrap(err, "list projects")
	}
	out := make([]string, 0, len(resp.Projects))
	for _, p := range resp.Projects {
		if p.Metadata.Name == "" {
			continue
		}
		out = append(out, p.Metadata.Name)
	}
	sort.Strings(out)
	return out, nil
}
