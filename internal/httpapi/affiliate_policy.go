package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
)

func (s *Server) adminAffiliatePolicy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	out, err := s.db.AffiliatePolicy(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}
func (s *Server) updateAdminAffiliatePolicy(w http.ResponseWriter, r *http.Request) {
	var in store.AffiliatePolicyInput
	if !decode(w, r, &in) {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	out, err := s.db.UpdateAffiliatePolicy(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value), r.Header.Get("Idempotency-Key"), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}
