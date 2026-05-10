package kargo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
)

// ArgoCDAppRef points at an Argo CD Application referenced by a Stage's
// health output. The TUI uses these to render clickable links.
type ArgoCDAppRef struct {
	Name      string
	Namespace string
	Health    string
	Sync      string
}

// DiscoverArgoCDBaseURL queries the Kargo server's GetConfig endpoint for
// the URL of any configured Argo CD shard. Returns "" when nothing is
// configured so callers can degrade gracefully.
func (c *Client) DiscoverArgoCDBaseURL(ctx context.Context) string {
	var resp struct {
		ArgoCDShards map[string]struct {
			URL       string `json:"url"`
			Namespace string `json:"namespace"`
		} `json:"argocdShards"`
	}
	if err := c.rpc.call(ctx, "GetConfig", struct{}{}, &resp); err != nil {
		return ""
	}
	if shard, ok := resp.ArgoCDShards["default"]; ok && shard.URL != "" {
		return strings.TrimRight(shard.URL, "/")
	}
	names := make([]string, 0, len(resp.ArgoCDShards))
	for name := range resp.ArgoCDShards {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if shard := resp.ArgoCDShards[name]; shard.URL != "" {
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
//
// Wire shape: under Connect-RPC over JSON the proto `bytes` field comes
// through as {"raw": "<base64>"} (the protojson canonical form for bytes
// fields nested in a map<string, RawExtension>-style value). The OpenAPI
// transport used to inline the JSON; we accept both for safety.
func parseArgoApps(raw []byte) []ArgoCDAppRef {
	if len(raw) == 0 {
		return nil
	}
	// {"raw": "<base64>"} → decode the wrapped bytes and parse those.
	if raw[0] == '{' {
		var wrap struct {
			Raw string `json:"raw"`
		}
		if err := json.Unmarshal(raw, &wrap); err == nil && wrap.Raw != "" {
			if decoded, err := base64.StdEncoding.DecodeString(wrap.Raw); err == nil {
				raw = decoded
			}
		}
	}
	// Bare quoted base64 (older shape). Fall through to plain-JSON parse on
	// failure so a non-base64 quoted payload still works.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
				raw = decoded
			} else {
				raw = []byte(s)
			}
		}
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
