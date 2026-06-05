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

// BaseURLFor returns the UI base URL that hosts the given app, or "" when
// no shard has a usable URL. Resolution order:
//  1. Exact namespace match: any shard whose controller runs in app.Namespace
//     (works for the common case where one Argo controller manages apps in
//     its own namespace).
//  2. The shard keyed "default" (Kargo's conventional primary shard).
//  3. The lowest-keyed shard with a URL (deterministic fallback so the link
//     stays stable across refreshes).
//
// A wrong-shard URL still lands the user on a real Argo CD instance that
// likely fronts the same backing cluster, which is more useful than a
// missing link. The previous "namespace match or single-shard only" rule
// silently hid the link whenever a multi-shard server's shard namespaces
// didn't line up with the app's, and that's the case the user hit.
func (s ArgoCDShards) BaseURLFor(app ArgoCDAppRef) string {
	for _, sh := range s {
		if sh.Namespace != "" && sh.Namespace == app.Namespace && sh.URL != "" {
			return strings.TrimRight(sh.URL, "/")
		}
	}
	if sh, ok := s["default"]; ok && sh.URL != "" {
		return strings.TrimRight(sh.URL, "/")
	}
	keys := make([]string, 0, len(s))
	for k, sh := range s {
		if sh.URL != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return strings.TrimRight(s[keys[0]].URL, "/")
}

// DiscoverArgoCDShards queries the Kargo server's GetConfig endpoint and
// returns every configured ArgoCD shard. Returns an empty map when nothing
// is configured or the RPC fails, so callers can degrade gracefully.
func (c *Client) DiscoverArgoCDShards(ctx context.Context) ArgoCDShards {
	var resp struct {
		ArgoCDShards map[string]struct {
			URL       string `json:"url"`
			Namespace string `json:"namespace"`
		} `json:"argocdShards"`
	}
	if err := c.rpc.call(ctx, "GetConfig", struct{}{}, &resp); err != nil {
		return ArgoCDShards{}
	}
	out := make(ArgoCDShards, len(resp.ArgoCDShards))
	for name, sh := range resp.ArgoCDShards {
		out[name] = ArgoCDShard{
			URL:       strings.TrimRight(sh.URL, "/"),
			Namespace: sh.Namespace,
		}
	}
	return out
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
