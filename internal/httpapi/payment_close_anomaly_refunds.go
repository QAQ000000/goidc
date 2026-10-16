package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) paymentAnomalyGroups(w http.ResponseWriter, r *http.Request) {
	invoice, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	items, total, err := s.db.ListPaymentAnomalyGroups(r.Context(), identityFrom(r).ID, invoice, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) paymentAnomalySummary(w http.ResponseWriter, r *http.Request) {
	invoice, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := s.db.PaymentAnomalySummary(r.Context(), identityFrom(r).ID, invoice)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
func (s *Server) paymentAnomalyEntries(w http.ResponseWriter, r *http.Request) {
	invoice, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	items, total, err := s.db.PaymentAnomalyEntries(r.Context(), identityFrom(r).ID, invoice, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) createPaymentAnomalyRefund(w http.ResponseWriter, r *http.Request) {
	invoice, ok := pathID(w, r)
	if !ok {
		return
	}
	anchor, err := strconv.ParseInt(r.PathValue("anchor_id"), 10, 64)
	if err != nil || anchor < 1 {
		writeError(w, 400, "invalid_id", "异常组编号无效")
		return
	}
	var in store.PaymentAnomalyRefundInput
	if !decode(w, r, &in) {
		return
	}
	ctx := store.WithExternalRefundSession(r.Context(), closePaymentSessionHash(r))
	out, err := s.db.CreatePaymentAnomalyRefund(ctx, identityFrom(r).ID, invoice, anchor, in, r.Header.Get("Idempotency-Key"))
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
func (s *Server) reviewPaymentAnomalyRefund(w http.ResponseWriter, r *http.Request) {
	invoice, ok := pathID(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("refund_id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "invalid_id", "异常退款编号无效")
		return
	}
	var in store.PaymentAnomalyReviewInput
	if !decode(w, r, &in) {
		return
	}
	ctx := store.WithExternalRefundSession(r.Context(), closePaymentSessionHash(r))
	out, err := s.db.ReviewPaymentAnomalyRefund(ctx, identityFrom(r).ID, invoice, id, strings.HasSuffix(r.Pattern, "/confirm"), in, r.Header.Get("Idempotency-Key"))
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
