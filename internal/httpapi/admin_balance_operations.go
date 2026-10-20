package httpapi

import "net/http"

// Wrap authentication as well, so rejected requests never cache a financial
// operation result. The idempotency key belongs in the header, never a URL.
func privateBalanceOperation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		next(w, r)
	}
}

func (s *Server) adminBalanceOperation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || len(keys[0]) < 8 || len(keys[0]) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "请提供长度为 8 到 128 的幂等键")
		return
	}
	out, err := s.db.AdminBalanceOperation(requestFileContext(r), identityFrom(r).ID, id, keys[0])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}
