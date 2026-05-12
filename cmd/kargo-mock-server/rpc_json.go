package main

import (
	"net/http"
	"sort"
)

// GetPublicConfig — returned shape must match the TUI's PublicConfig
// struct (internal/kargo/client.go). adminAccountEnabled:false + nil
// oidc tells the TUI auth is off, so it'll send empty bearer tokens
// happily. skipAuth bypasses the login screen entirely.
func (h *handlers) getPublicConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"adminAccountEnabled": false,
		"skipAuth":            true,
	})
}

// AdminLogin — never invoked when skipAuth is set, but stub it anyway in
// case someone hits the login command directly.
func (h *handlers) adminLogin(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"idToken": "mock-token-not-used"})
}

// GetConfig — the TUI uses this to discover the Argo CD base URL for
// clickable links. Return a single argo-cd-shard entry pointing at the
// topology-configured ArgoCDURL (or a dead-link default).
func (h *handlers) getConfig(w http.ResponseWriter, _ *http.Request) {
	url := ""
	for _, name := range h.store.projectNames() {
		p, _ := h.store.project(name)
		if p != nil && p.topology.ArgoCDURL != "" {
			url = p.topology.ArgoCDURL
			break
		}
	}
	if url == "" {
		url = "https://argocd.example.com"
	}
	writeJSON(w, map[string]any{
		"argocdShards": map[string]any{
			"default": map[string]string{"url": url},
		},
	})
}

// ListProjects — wraps each name in a metadata envelope to match the
// shape the TUI's parser expects (internal/kargo/projects.go).
func (h *handlers) listProjects(w http.ResponseWriter, _ *http.Request) {
	names := h.store.projectNames()
	projects := make([]map[string]any, len(names))
	for i, n := range names {
		projects[i] = map[string]any{
			"metadata": map[string]string{"name": n},
		}
	}
	writeJSON(w, map[string]any{"projects": projects})
}

// ListProjectEvents — returns events filtered to a project. The TUI's
// caller filters again client-side by stage name prefix.
func (h *handlers) listProjectEvents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
	}
	if err := readJSON(r, &req); err != nil {
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	p, ok := h.store.project(req.Project)
	if !ok {
		writeJSON(w, map[string]any{"events": []rawEventJSON{}})
		return
	}
	events := append([]rawEventJSON(nil), p.events...)
	sort.Slice(events, func(i, j int) bool {
		return events[i].LastTimestamp > events[j].LastTimestamp
	})
	writeJSON(w, map[string]any{"events": events})
}
