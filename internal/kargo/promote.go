package kargo

import (
	"context"
	"errors"

	svcv1alpha1 "unknwon.dev/kargo-tui/internal/kargoapi/svc"
)

// PromoteToStage creates a Promotion record promoting the given freight to
// the given stage. Returns the created Promotion flattened to a
// PromotionEntry so the caller can show its phase/name in the status line.
func (c *Client) PromoteToStage(ctx context.Context, project, stage, freight string) (*PromotionEntry, error) {
	if project == "" {
		project = c.project
	}
	if stage == "" {
		return nil, errors.New("stage is required")
	}
	if freight == "" {
		return nil, errors.New("freight is required")
	}
	req := &svcv1alpha1.PromoteToStageRequest{
		Project: project,
		Stage:   stage,
		Freight: freight,
	}
	resp := &svcv1alpha1.PromoteToStageResponse{}
	if err := c.rpc.callProto(ctx, "PromoteToStage", req, resp); err != nil {
		return nil, err
	}
	if resp.Promotion == nil {
		return nil, nil
	}
	entry := flattenPromotion(resp.Promotion)
	return &entry, nil
}
