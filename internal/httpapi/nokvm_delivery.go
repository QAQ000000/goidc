package httpapi

import (
	"net/http"
	"sort"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) provisionProfiles(w http.ResponseWriter, r *http.Request) {
	profiles := []map[string]string{}
	for _, adapter := range s.db.Provisioners {
		profiles = append(profiles, adapter.Summary())
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i]["id"] < profiles[j]["id"] })
	write(w, 200, profiles)
}
func (s *Server) verifyDelivery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input store.DeliveryVerification
	if !decode(w, r, &input) {
		return
	}
	result, err := s.db.VerifyNOKVMJob(r.Context(), identityFrom(r).ID, id, input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, result)
}
func (s *Server) serviceCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.authLimit(w, r, "service-credentials", 30) {
		return
	}
	result, err := s.db.ServiceCredentials(requestFileContext(r), identityFrom(r).ID, id, adminRoute(r))
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, 200, result)
}

func (s *Server) serviceVNC(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.authLimit(w, r, "service-vnc", 30) {
		return
	}
	u, err := s.db.ServiceVNC(requestFileContext(r), identityFrom(r).ID, id, adminRoute(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]string{"url": u})
}

func (s *Server) reinstallTemplates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	result, err := s.db.ServiceTemplates(requestFileContext(r), identityFrom(r).ID, id, adminRoute(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, result)
}

func (s *Server) reinstallService(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		TemplateID   int64 `json:"template_id"`
		Acknowledged bool  `json:"data_loss_acknowledged"`
	}
	if !decode(w, r, &in) {
		return
	}
	result, err := s.db.ReinstallService(requestFileContext(r), identityFrom(r).ID, id, in.TemplateID, in.Acknowledged, r.Header.Get("Idempotency-Key"), adminRoute(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	write(w, status, result)
}
