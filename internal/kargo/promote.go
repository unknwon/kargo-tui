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

// PromoteDownstream promotes the given source stage's freight to every
// stage that lists it as an upstream. Returns one PromotionEntry per
// downstream stage that the server kicked off — the slice is empty when
// the source has no downstreams or no eligible freight.
func (c *Client) PromoteDownstream(ctx context.Context, project, sourceStage, freight string) ([]PromotionEntry, error) {
	if project == "" {
		project = c.project
	}
	if sourceStage == "" {
		return nil, errors.New("source stage is required")
	}
	if freight == "" {
		return nil, errors.New("freight is required")
	}
	req := &svcv1alpha1.PromoteDownstreamRequest{
		Project: project,
		Stage:   sourceStage,
		Freight: freight,
	}
	resp := &svcv1alpha1.PromoteDownstreamResponse{}
	if err := c.rpc.callProto(ctx, "PromoteDownstream", req, resp); err != nil {
		return nil, err
	}
	out := make([]PromotionEntry, 0, len(resp.Promotions))
	for _, p := range resp.Promotions {
		if p == nil {
			continue
		}
		out = append(out, flattenPromotion(p))
	}
	return out, nil
}
