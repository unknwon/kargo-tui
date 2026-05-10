package kargo

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/akuity/kargo/pkg/client/generated/system"
)

// ArgoCDAppRef points at an Argo CD Application referenced by a Stage's
// health output. The TUI uses these to render clickable links.
type ArgoCDAppRef struct {
	Name      string
	Namespace string
	Health    string
	Sync      string
}

// DiscoverArgoCDBaseURL queries the Kargo server's GetConfig endpoint for the
// URL of any configured Argo CD shard. Returns "" when nothing is configured
// so callers can degrade gracefully.
func (c *Client) DiscoverArgoCDBaseURL(ctx context.Context) string {
	resp, err := c.api.System.GetConfig(
		system.NewGetConfigParams().WithContext(ctx),
		c.authInfo,
	)
	if err != nil || resp.Payload == nil {
		return ""
	}
	shards := resp.Payload.ArgocdShards
	// Prefer a shard literally named "default" if present, otherwise the
	// alphabetically-first one. Most installs configure exactly one shard.
	if shard, ok := shards["default"]; ok && shard.URL != "" {
		return strings.TrimRight(shard.URL, "/")
	}
	names := make([]string, 0, len(shards))
	for name := range shards {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if shard := shards[name]; shard.URL != "" {
			return strings.TrimRight(shard.URL, "/")
		}
	}
	return ""
}

// parseArgoApps extracts Argo CD Application references from a Stage's
// status.health.output JSON. The output is an array of objects, each
// containing an applicationStatuses entry with name/namespace and nested
// health/sync state. We tolerate inconsistent casing ("Name" vs "name") since
// the upstream payload mixes both.
func parseArgoApps(raw []byte) []ArgoCDAppRef {
	if len(raw) == 0 {
		return nil
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	var out []ArgoCDAppRef
	seen := make(map[string]struct{})
	for _, e := range entries {
		apps, _ := e["applicationStatuses"].([]any)
		for _, a := range apps {
			m, ok := a.(map[string]any)
			if !ok {
				continue
			}
			name := pickString(m, "Name", "name")
			ns := pickString(m, "Namespace", "namespace")
			if name == "" || ns == "" {
				continue
			}
			key := ns + "/" + name
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			ref := ArgoCDAppRef{Name: name, Namespace: ns}
			if h, ok := m["health"].(map[string]any); ok {
				ref.Health = pickString(h, "Status", "status")
			}
			if sy, ok := m["sync"].(map[string]any); ok {
				ref.Sync = pickString(sy, "Status", "status")
			}
			out = append(out, ref)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}
