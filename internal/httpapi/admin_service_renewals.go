package httpapi

import (
	"net/http"
	"strconv"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) adminRenewalQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.AdminRenewalQuote(requestFileContext(r), identityFrom(r).ID, id, in.ExpectedVersion)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}

func adminRenewalMutationKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "请只提供一个幂等键")
		return "", false
	}
	return mutationKey(w, r, "", true)
}

func (s *Server) adminCreateRenewal(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.AdminServiceRenewalInput
	if !decode(w, r, &in) {
		return
	}
	key, ok := adminRenewalMutationKey(w, r)
	if !ok {
		return
	}
	out, err := s.db.AdminCreateRenewalInvoice(requestFileContext(r), identityFrom(r).ID, id, in, key)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	write(w, status, out)
}

func (s *Server) adminPayRenewal(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	invoice, err := strconv.ParseInt(r.PathValue("invoice_id"), 10, 64)
	if err != nil || invoice < 1 {
		writeError(w, http.StatusBadRequest, "invalid_id", "账单编号无效")
		return
	}
	var in struct {
		ExpectedVersion int64  `json:"expected_version"`
		AmountCents     *int64 `json:"amount_cents"`
		Reason          string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.AmountCents == nil {
		writeError(w, http.StatusBadRequest, "invalid_renewal_payment", "请明确确认账单金额")
		return
	}
	key, ok := adminRenewalMutationKey(w, r)
	if !ok {
		return
	}
	out, err := s.db.AdminPayRenewalInvoice(requestFileContext(r), identityFrom(r).ID, id, invoice, store.AdminRenewalPaymentInput{
		ExpectedVersion: in.ExpectedVersion, AmountCents: *in.AmountCents, Reason: in.Reason,
	}, key)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	write(w, status, out)
}
