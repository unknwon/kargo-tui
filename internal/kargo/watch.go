package kargo

import (
	"context"
	"sort"

	"google.golang.org/protobuf/proto"

	svcv1alpha1 "unknwon.dev/kargo-tui/internal/kargoapi/svc"
)

// StageEvent is one change notification from WatchStages, flattened for
// the TUI.
type StageEvent struct {
	Type  string // ADDED / MODIFIED / DELETED
	Stage Stage
}

// WatchStages opens a server-streaming WatchStages RPC and pumps events
// into the returned channel. Both channels close when the stream ends
// (clean close, error, or ctx cancellation). Any error encountered is
// posted to errCh once before close — callers typically restart the
// watch or fall back to ListStages polling.
func (c *Client) WatchStages(ctx context.Context, project string) (<-chan StageEvent, <-chan error) {
	if project == "" {
		project = c.project
	}
	out := make(chan StageEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errCh)
		req := &svcv1alpha1.WatchStagesRequest{Project: project}
		err := c.rpc.callServerStream(
			ctx,
			"WatchStages",
			req,
			func() proto.Message { return &svcv1alpha1.WatchStagesResponse{} },
			func(m proto.Message) error {
				resp := m.(*svcv1alpha1.WatchStagesResponse)
				if resp.Stage == nil {
					return nil
				}
				select {
				case out <- StageEvent{Type: resp.Type, Stage: flattenStage(resp.Stage)}:
				case <-ctx.Done():
					return ctx.Err()
				}
				return nil
			},
		)
		if err != nil && ctx.Err() == nil {
			errCh <- err
		}
	}()
	return out, errCh
}

// MergeStageEvent applies a single StageEvent to an existing []Stage
// and returns a new slice with the change folded in. Result is sorted
// the same way ListStages sorts so the UI doesn't reshuffle on
// per-event updates.
func MergeStageEvent(cur []Stage, ev StageEvent) []Stage {
	switch ev.Type {
	case "DELETED":
		out := make([]Stage, 0, len(cur))
		for _, s := range cur {
			if s.Name != ev.Stage.Name {
				out = append(out, s)
			}
		}
		return out
	default: // ADDED / MODIFIED / ""
		out := make([]Stage, 0, len(cur)+1)
		replaced := false
		for _, s := range cur {
			if s.Name == ev.Stage.Name {
				out = append(out, ev.Stage)
				replaced = true
			} else {
				out = append(out, s)
			}
		}
		if !replaced {
			out = append(out, ev.Stage)
		}
		sort.Slice(out, func(i, j int) bool {
			if !out[i].Created.Equal(out[j].Created) {
				return out[i].Created.After(out[j].Created)
			}
			return out[i].Name < out[j].Name
		})
		return out
	}
}
