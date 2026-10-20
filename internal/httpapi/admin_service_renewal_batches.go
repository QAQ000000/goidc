package httpapi

import (
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) adminRenewalBatchQuote(w http.ResponseWriter, r *http.Request) {
	var in store.AdminRenewalBatchQuoteInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.AdminRenewalBatchQuote(requestFileContext(r), identityFrom(r).ID, in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) adminCreateRenewalBatch(w http.ResponseWriter, r *http.Request) {
	var in store.AdminRenewalBatchInput
	if !decode(w, r, &in) {
		return
	}
	key, ok := adminRenewalMutationKey(w, r)
	if !ok {
		return
	}
	out, err := s.db.AdminCreateRenewalBatch(requestFileContext(r), identityFrom(r).ID, in, key)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	write(w, status, out)
}

func (s *Server) adminRenewalBatch(w http.ResponseWriter, r *http.Request) {
	out, err := s.db.AdminRenewalBatch(requestFileContext(r), identityFrom(r).ID, r.PathValue("batch_id"))
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) adminExecuteRenewalBatch(w http.ResponseWriter, r *http.Request) {
	var in store.AdminRenewalBatchExecutionInput
	if !decode(w, r, &in) {
		return
	}
	key, ok := adminRenewalMutationKey(w, r)
	if !ok {
		return
	}
	out, err := s.db.AdminExecuteRenewalBatch(requestFileContext(r), identityFrom(r).ID, r.PathValue("batch_id"), in, key)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}
