package kargo

import (
	"context"
	"fmt"
	"sort"

	"github.com/akuity/kargo/pkg/client/generated/core"
)

// ListProjects returns the names of Kargo projects accessible to the
// authenticated user, sorted alphabetically.
func (c *Client) ListProjects(ctx context.Context) ([]string, error) {
	resp, err := c.api.Core.ListProjects(
		core.NewListProjectsParams().WithContext(ctx),
		c.authInfo,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	if resp.Payload == nil {
		return nil, nil
	}
	out := make([]string, 0, len(resp.Payload.Items))
	for _, p := range resp.Payload.Items {
		if p == nil || p.Metadata == nil {
			continue
		}
		out = append(out, p.Metadata.Name)
	}
	sort.Strings(out)
	return out, nil
}
