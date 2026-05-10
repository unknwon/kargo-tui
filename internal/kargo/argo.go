package kargo

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ArgoCDAppRef points at an Argo CD Application referenced by a Stage's
// health output. The TUI uses these to render clickable links.
type ArgoCDAppRef struct {
	Name      string
	Namespace string
	Health    string
	Sync      string
}

// DiscoverArgoCDBaseURL returns the configured Argo CD UI URL by reading the
// argocd-cm ConfigMap; falls back to scanning Ingresses in the argocd
// namespace for the argocd-server service. Returns "" when nothing is found
// so callers can degrade gracefully.
func DiscoverArgoCDBaseURL(ctx context.Context) string {
	c, err := newClient()
	if err != nil {
		return ""
	}
	for _, ns := range []string{"argocd", "argo-cd"} {
		var cm corev1.ConfigMap
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "argocd-cm"}, &cm); err == nil {
			if u := strings.TrimRight(cm.Data["url"], "/"); u != "" {
				return u
			}
		}
		var ings networkingv1.IngressList
		if err := c.List(ctx, &ings, client.InNamespace(ns)); err == nil {
			for _, ing := range ings.Items {
				for _, rule := range ing.Spec.Rules {
					if rule.Host != "" && referencesArgoCDServer(ing) {
						scheme := "https"
						if len(ing.Spec.TLS) == 0 {
							scheme = "http"
						}
						return scheme + "://" + rule.Host
					}
				}
			}
		}
	}
	return ""
}

// referencesArgoCDServer returns true if any of the Ingress's rules backs an
// argocd-server Service. Used as a heuristic for which Ingress to trust.
func referencesArgoCDServer(ing networkingv1.Ingress) bool {
	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, p := range rule.HTTP.Paths {
			if p.Backend.Service != nil && strings.Contains(p.Backend.Service.Name, "argocd-server") {
				return true
			}
		}
	}
	return false
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
