package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
)

// Apply cache policy before authentication, including rejected requests.
func (s *Server) topupLiabilityHTTP(next http.HandlerFunc, mutation bool) http.HandlerFunc {
	authenticated := s.auth(next, true)
	if mutation {
		authenticated = s.sessionAuth(next, true)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		authenticated(w, r)
	}
}

func (s *Server) topupLiability(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := s.db.GetTopupLiability(r.Context(), identityFrom(r).ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
func (s *Server) resolveTopupLiability(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.ResolveTopupLiabilityInput
	if !decode(w, r, &in) {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	out, err := s.db.ResolveTopupLiability(store.WithTopupLiabilitySession(r.Context(), tokenHash(cookie.Value)), identityFrom(r).ID, id, in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 201
	if out.Replayed {
		status = 200
	}
	write(w, status, out)
}
