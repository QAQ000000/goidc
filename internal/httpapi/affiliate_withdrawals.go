package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
	"strings"
)

func (s *Server) affiliateWithdrawals(w http.ResponseWriter, r *http.Request) {
	actor := identityFrom(r).ID
	if r.Method == "GET" {
		limit, offset := pagination(r)
		items, err := s.db.ListAffiliateWithdrawals(r.Context(), actor, false, limit, offset)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, items)
		return
	}
	var in store.AffiliateWithdrawalInput
	if !decode(w, r, &in) {
		return
	}
	result, replay, err := s.db.CreateAffiliateWithdrawal(requestFileContext(r), actor, strings.TrimSpace(r.Header.Get("Idempotency-Key")), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 201
	if replay {
		status = 200
	}
	write(w, status, result)
}
func (s *Server) affiliateWithdrawalDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	admin := strings.HasPrefix(r.URL.Path, "/api/v1/admin/")
	item, err := s.db.AffiliateWithdrawalDetail(r.Context(), identityFrom(r).ID, id, admin)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, item)
}
func (s *Server) adminAffiliateWithdrawals(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	items, err := s.db.ListAffiliateWithdrawals(r.Context(), identityFrom(r).ID, true, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, items)
}
func (s *Server) reviewAffiliateWithdrawal(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.AffiliateWithdrawalReview
	if !decode(w, r, &in) {
		return
	}
	result, err := s.db.ReviewAffiliateWithdrawal(requestFileContext(r), identityFrom(r).ID, id, strings.TrimSpace(r.Header.Get("Idempotency-Key")), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, result)
}
func (s *Server) payoutAffiliateWithdrawal(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.AffiliatePayoutInput
	if !decode(w, r, &in) {
		return
	}
	result, err := s.db.RegisterAffiliatePayout(requestFileContext(r), identityFrom(r).ID, id, strings.TrimSpace(r.Header.Get("Idempotency-Key")), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, result)
}
func (s *Server) affiliatePayoutTarget(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	result, err := s.db.AffiliatePayoutTarget(requestFileContext(r), identityFrom(r).ID, id, in.Reason)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, result)
}
