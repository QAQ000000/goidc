package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
)

func (s *Server) adminCustomerTopupOptions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	out, err := s.db.AdminCustomerTopupOptions(requestFileContext(r), identityFrom(r).ID, id)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, out)
}
func (s *Server) adminCreateCustomerTopup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.AdminCustomerTopupInput
	if !decode(w, r, &in) {
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	out, err := s.db.AdminCreateCustomerTopup(requestFileContext(r), identityFrom(r).ID, id, in, key)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	status := http.StatusCreated
	if out["replayed"] == true {
		status = http.StatusOK
	}
	write(w, status, out)
}
