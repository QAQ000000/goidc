package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
)

func (s *Server) orderSettings(w http.ResponseWriter, r *http.Request) {
	out, err := s.db.OrderSettings(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
func (s *Server) updateOrderSettings(w http.ResponseWriter, r *http.Request) {
	var in store.OrderSettingsInput
	if !decode(w, r, &in) {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	out, err := s.db.UpdateOrderSettings(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value), r.Header.Get("Idempotency-Key"), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
