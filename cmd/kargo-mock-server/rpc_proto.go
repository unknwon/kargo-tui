package main

import (
	"net/http"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	svcv1alpha1 "unknwon.dev/kargo-tui/internal/kargoapi/svc"
)

func (h *handlers) listStages(w http.ResponseWriter, r *http.Request) {
	req := &svcv1alpha1.ListStagesRequest{}
	if err := readProto(r, req); err != nil {
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	p, ok := h.store.project(req.Project)
	if !ok {
		writeProto(w, &svcv1alpha1.ListStagesResponse{})
		return
	}
	h.store.mu.RLock()
	stages := p.listStages()
	h.store.mu.RUnlock()
	resp := &svcv1alpha1.ListStagesResponse{Stages: stages}
	writeProto(w, resp)
}

func (h *handlers) queryFreight(w http.ResponseWriter, r *http.Request) {
	req := &svcv1alpha1.QueryFreightRequest{}
	if err := readProto(r, req); err != nil {
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	p, ok := h.store.project(req.Project)
	if !ok {
		writeProto(w, &svcv1alpha1.QueryFreightResponse{})
		return
	}
	h.store.mu.RLock()
	freight := p.listFreight()
	h.store.mu.RUnlock()
	// The TUI's ListFreight flattens every group's freight into one list,
	// dedup by name. Returning a single empty-key group works fine.
	fl := make([]*kargoapi.Freight, len(freight))
	copy(fl, freight)
	resp := &svcv1alpha1.QueryFreightResponse{
		Groups: map[string]*svcv1alpha1.FreightList{
			"": {Freight: fl},
		},
	}
	writeProto(w, resp)
}

func (h *handlers) listPromotions(w http.ResponseWriter, r *http.Request) {
	req := &svcv1alpha1.ListPromotionsRequest{}
	if err := readProto(r, req); err != nil {
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	p, ok := h.store.project(req.Project)
	if !ok {
		writeProto(w, &svcv1alpha1.ListPromotionsResponse{})
		return
	}
	stage := ""
	if req.Stage != nil {
		stage = *req.Stage
	}
	h.store.mu.RLock()
	promos := p.listPromotions(stage)
	h.store.mu.RUnlock()
	writeProto(w, &svcv1alpha1.ListPromotionsResponse{Promotions: promos})
}

func (h *handlers) promoteToStage(w http.ResponseWriter, r *http.Request) {
	req := &svcv1alpha1.PromoteToStageRequest{}
	if err := readProto(r, req); err != nil {
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	p, ok := h.store.project(req.Project)
	if !ok {
		writeConnectError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	promo, err := h.runPromote(p, req.Stage, req.Freight, "user")
	if err != nil {
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	writeProto(w, &svcv1alpha1.PromoteToStageResponse{Promotion: promo})
}

func (h *handlers) approveFreight(w http.ResponseWriter, r *http.Request) {
	req := &svcv1alpha1.ApproveFreightRequest{}
	if err := readProto(r, req); err != nil {
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	p, ok := h.store.project(req.Project)
	if !ok {
		writeConnectError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	if err := h.runApprove(p, req.Name, req.Stage); err != nil {
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	writeProto(w, &svcv1alpha1.ApproveFreightResponse{})
}

func (h *handlers) promoteDownstream(w http.ResponseWriter, r *http.Request) {
	req := &svcv1alpha1.PromoteDownstreamRequest{}
	if err := readProto(r, req); err != nil {
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	p, ok := h.store.project(req.Project)
	if !ok {
		writeConnectError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	promos := h.runPromoteDownstream(p, req.Stage, req.Freight)
	writeProto(w, &svcv1alpha1.PromoteDownstreamResponse{Promotions: promos})
}
