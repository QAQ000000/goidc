package httpapi

import (
	"net/http"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) adminServiceManagement(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := s.db.AdminServiceManagement(requestFileContext(r), identityFrom(r).ID, id)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) adminServicePricingQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion  int64  `json:"expected_version"`
		Mode             string `json:"mode"`
		FixedAmountCents *int64 `json:"fixed_amount_cents"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.AdminServicePricingQuote(requestFileContext(r), identityFrom(r).ID, id, store.AdminServicePricingInput{
		ExpectedVersion: in.ExpectedVersion, Mode: in.Mode, FixedAmountCents: in.FixedAmountCents,
	})
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) adminSetServicePricing(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.AdminServicePricingInput
	if !decode(w, r, &in) {
		return
	}
	key, ok := adminRenewalMutationKey(w, r)
	if !ok {
		return
	}
	out, err := s.db.AdminSetServicePricing(requestFileContext(r), identityFrom(r).ID, id, in, key)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) adminFixedRenewalQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64  `json:"expected_version"`
		AmountCents     *int64 `json:"amount_cents"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.AmountCents == nil {
		writeError(w, http.StatusBadRequest, "invalid_fixed_renewal", "请明确本次续费应付金额")
		return
	}
	out, err := s.db.AdminFixedRenewalQuote(requestFileContext(r), identityFrom(r).ID, id, in.ExpectedVersion, *in.AmountCents)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) adminCreateFixedRenewal(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.AdminServiceFixedRenewalInput
	if !decode(w, r, &in) {
		return
	}
	key, ok := adminRenewalMutationKey(w, r)
	if !ok {
		return
	}
	out, err := s.db.AdminCreateFixedRenewalInvoice(requestFileContext(r), identityFrom(r).ID, id, in, key)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	write(w, status, out)
}

func (s *Server) adminServiceTermQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64      `json:"expected_version"`
		Kind            string     `json:"kind"`
		DueAt           *time.Time `json:"next_due_at"`
		Reason          string     `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.AdminServiceTermQuote(requestFileContext(r), identityFrom(r).ID, id, store.AdminServiceTermInput{
		ExpectedVersion: in.ExpectedVersion, Kind: in.Kind, DueAt: in.DueAt, Reason: in.Reason,
	})
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) adminAdjustServiceTerm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.AdminServiceTermInput
	if !decode(w, r, &in) {
		return
	}
	key, ok := adminRenewalMutationKey(w, r)
	if !ok {
		return
	}
	out, err := s.db.AdminAdjustServiceTerm(requestFileContext(r), identityFrom(r).ID, id, in, key)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}
