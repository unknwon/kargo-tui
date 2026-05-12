package main

import (
	"net/http"
)

// servicePath is the common URL prefix for every Kargo Connect-RPC method.
// Mirrors internal/kargo/connectjson.go.
const servicePath = "/akuity.io.kargo.service.v1alpha1.KargoService/"

// registerRoutes wires every Kargo RPC the TUI knows how to call onto the
// given mux. Unimplemented or unknown methods get a 501.
func registerRoutes(mux *http.ServeMux, s *store, speed float64) {
	h := &handlers{store: s, speed: speed}

	// JSON unary
	mux.HandleFunc(servicePath+"GetPublicConfig", h.getPublicConfig)
	mux.HandleFunc(servicePath+"AdminLogin", h.adminLogin)
	mux.HandleFunc(servicePath+"GetConfig", h.getConfig)
	mux.HandleFunc(servicePath+"ListProjects", h.listProjects)
	mux.HandleFunc(servicePath+"ListProjectEvents", h.listProjectEvents)

	// Proto unary
	mux.HandleFunc(servicePath+"ListStages", h.listStages)
	mux.HandleFunc(servicePath+"QueryFreight", h.queryFreight)
	mux.HandleFunc(servicePath+"ListPromotions", h.listPromotions)
	mux.HandleFunc(servicePath+"PromoteToStage", h.promoteToStage)
	mux.HandleFunc(servicePath+"ApproveFreight", h.approveFreight)
	mux.HandleFunc(servicePath+"PromoteDownstream", h.promoteDownstream)

	// Proto server-streaming
	mux.HandleFunc(servicePath+"WatchStages", h.watchStages)

	// Catch-all so unknown RPCs surface as Connect errors, not 404 HTML.
	mux.HandleFunc(servicePath, h.notImplemented)
}

// handlers carries the dependencies every RPC handler reads from.
type handlers struct {
	store *store
	speed float64
}
