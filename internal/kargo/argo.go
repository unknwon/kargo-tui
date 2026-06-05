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

// ArgoCDShard captures one ArgoCD shard's UI base URL and the Kubernetes
// namespace where its controller runs. The shard name (the map key in
// ArgoCDShards) is whatever the Kargo admin configured. Kargo does not
// require it to be "default".
type ArgoCDShard struct {
	URL       string
	Namespace string
}

// ArgoCDShards is the discovered shard table keyed by shard name. Empty when
// the server has no shards configured or GetConfig failed.
type ArgoCDShards map[string]ArgoCDShard

// ShardLabelKey is the Stage label whose value names which Argo CD shard
// the stage's Applications live on. Matches the upstream Kargo web UI
// constant kargo.akuity.io/shard. The empty string is a valid value (it
// means "the unnamed default shard") and is keyed verbatim into
// ArgoCDShards.
const ShardLabelKey = "kargo.akuity.io/shard"

// BaseURLFor returns the UI base URL of the shard identified by
// shardKey, or "" when no such shard exists or its URL is unset.
//
// shardKey is the value of the Stage's kargo.akuity.io/shard label.
// This mirrors the upstream Kargo web UI's resolution exactly (see
// ui/src/features/project/pipelines/nodes/argocd-link.tsx) — the label
// is the only source of truth, and a missing shard means the link is
// hidden rather than guessed. The previous heuristics that tried to
// match a shard by app namespace or fall back to "default" were
// inventing a relationship the server never exposed.
func (s ArgoCDShards) BaseURLFor(shardKey string) string {
	sh, ok := s[shardKey]
	if !ok || sh.URL == "" {
		return ""
	}
	return strings.TrimRight(sh.URL, "/")
}

// DiscoverArgoCDShards queries the Kargo server's GetConfig endpoint and
// returns every configured ArgoCD shard. The error is surfaced (rather
// than swallowed into an empty map) so the TUI can show why links are
// missing instead of silently dropping them.
func (c *Client) DiscoverArgoCDShards(ctx context.Context) (ArgoCDShards, error) {
	var resp struct {
		ArgoCDShards map[string]struct {
			URL       string `json:"url"`
			Namespace string `json:"namespace"`
		} `json:"argocdShards"`
	}
	if err := c.rpc.call(ctx, "GetConfig", struct{}{}, &resp); err != nil {
		return ArgoCDShards{}, err
	}
	out := make(ArgoCDShards, len(resp.ArgoCDShards))
	for name, sh := range resp.ArgoCDShards {
		out[name] = ArgoCDShard{
			URL:       strings.TrimRight(sh.URL, "/"),
			Namespace: sh.Namespace,
		}
	}
	return out, nil
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
