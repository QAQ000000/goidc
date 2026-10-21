package httpapi

import (
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) adminServiceTransferQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.AdminServiceTransferInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.QuoteAdminServiceTransfer(requestFileContext(r), identityFrom(r).ID, id, in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) adminServiceTransfer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.AdminServiceTransferInput
	if !decode(w, r, &in) {
		return
	}
	key, ok := adminRenewalMutationKey(w, r)
	if !ok {
		return
	}
	out, err := s.db.TransferAdminService(requestFileContext(r), identityFrom(r).ID, id, key, in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	write(w, status, out)
}
