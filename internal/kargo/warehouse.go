package kargo

import (
	"context"

	"github.com/cockroachdb/errors"

	svcv1alpha1 "unknwon.dev/kargo-tui/internal/kargoapi/svc"
)

// ListWarehouseNames returns the names of every Warehouse in the given
// project. Used by the F shortcut to fan out a RefreshWarehouse call
// across every warehouse so the user can force the controller to
// re-run subscriptions without waiting for the next poll interval.
func (c *Client) ListWarehouseNames(ctx context.Context, project string) ([]string, error) {
	if project == "" {
		project = c.project
	}
	req := &svcv1alpha1.ListWarehousesRequest{Project: project}
	resp := &svcv1alpha1.ListWarehousesResponse{}
	if err := c.rpc.callProto(ctx, "ListWarehouses", req, resp); err != nil {
		return nil, errors.Wrap(err, "list warehouses")
	}
	out := make([]string, 0, len(resp.Warehouses))
	for _, w := range resp.Warehouses {
		if w == nil || w.Name == "" {
			continue
		}
		out = append(out, w.Name)
	}
	return out, nil
}

// RefreshWarehouse asks the Kargo server to immediately reconcile the
// named Warehouse. The server-side implementation bumps the
// kargo.akuity.io/refresh annotation, which the controller watches.
// Reconciliation is asynchronous: new Freight may not appear until the
// next QueryFreight call after the controller finishes its run.
func (c *Client) RefreshWarehouse(ctx context.Context, project, name string) error {
	if project == "" {
		project = c.project
	}
	if name == "" {
		return errors.New("warehouse name is required")
	}
	req := &svcv1alpha1.RefreshResourceRequest{
		Project:      project,
		Name:         name,
		ResourceType: "Warehouse",
	}
	resp := &svcv1alpha1.RefreshResourceResponse{}
	if err := c.rpc.callProto(ctx, "RefreshResource", req, resp); err != nil {
		return errors.Wrap(err, "refresh warehouse")
	}
	return nil
}
