package httpapi

import (
	"fmt"
	"net/http"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) serviceNAT(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.authLimit(w, r, fmt.Sprintf("service-nat:%d", identityFrom(r).ID), 30) {
		return
	}
	kind := r.PathValue("kind")
	if r.Method == http.MethodGet {
		result, err := s.db.ServiceNAT(r.Context(), identityFrom(r).ID, id, kind, adminRoute(r))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, http.StatusOK, result)
		return
	}
	var in struct {
		Operation    string `json:"operation"`
		PointID      string `json:"point_id"`
		Name         string `json:"name"`
		Type         string `json:"type"`
		Domain       string `json:"domain"`
		ExteriorPort int    `json:"exterior_port"`
		InteriorPort int    `json:"interior_port"`
		Acknowledged bool   `json:"risk_acknowledged"`
	}
	if !decode(w, r, &in) {
		return
	}
	credential := bearerCredential(r)
	apiKey := credential != ""
	if !apiKey {
		cookie, err := r.Cookie("zjmfgo_session")
		if err != nil {
			writeError(w, 401, "unauthorized", "请先登录")
			return
		}
		credential = cookie.Value
	}
	ctx := store.WithStorageCredential(r.Context(), tokenHash(credential), apiKey)
	request := provision.NATRequest{Kind: kind, Operation: in.Operation, PointID: in.PointID, Name: in.Name, Type: in.Type, Domain: in.Domain, ExteriorPort: in.ExteriorPort, InteriorPort: in.InteriorPort}
	result, err := s.db.NATServiceAction(ctx, identityFrom(r).ID, id, request, r.Header.Get("Idempotency-Key"), in.Acknowledged, adminRoute(r))
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
