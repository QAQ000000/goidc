package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
)

func (s *Server) serviceCDROM(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.authLimit(w, r, "service-cdrom", 30) {
		return
	}
	if r.Method == http.MethodGet {
		result, err := s.db.ServiceCDROM(r.Context(), identityFrom(r).ID, id, adminRoute(r))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.auth(func(w http.ResponseWriter, r *http.Request) { write(w, 200, result) }, adminRoute(r))(w, r)
		return
	}
	var in struct {
		Operation    string `json:"operation"`
		ImageID      string `json:"image_id"`
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
	result, err := s.db.CDROMServiceAction(ctx, identityFrom(r).ID, id, in.Operation, in.ImageID, r.Header.Get("Idempotency-Key"), in.Acknowledged, adminRoute(r))
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
