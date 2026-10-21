package httpapi

import (
	"net/http"
	"strconv"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) adminServiceManualCollectionQuote(w http.ResponseWriter, r *http.Request) {
	service, ok := pathID(w, r)
	if !ok {
		return
	}
	invoice, err := strconv.ParseInt(r.PathValue("invoice_id"), 10, 64)
	if err != nil || invoice < 1 {
		writeError(w, http.StatusBadRequest, "invalid_id", "账单编号无效")
		return
	}
	var in store.AdminServiceManualCollectionInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.QuoteAdminServiceManualCollection(requestFileContext(r), identityFrom(r).ID, service, invoice, in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) adminCollectServiceRenewal(w http.ResponseWriter, r *http.Request) {
	service, ok := pathID(w, r)
	if !ok {
		return
	}
	invoice, err := strconv.ParseInt(r.PathValue("invoice_id"), 10, 64)
	if err != nil || invoice < 1 {
		writeError(w, http.StatusBadRequest, "invalid_id", "账单编号无效")
		return
	}
	var in store.AdminServiceManualCollectionInput
	if !decode(w, r, &in) {
		return
	}
	key, ok := adminRenewalMutationKey(w, r)
	if !ok {
		return
	}
	out, err := s.db.CollectAdminServiceRenewal(requestFileContext(r), identityFrom(r).ID, service, invoice, key, in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	write(w, status, out)
}
