package httpapi

import (
	"net/http"
	"strconv"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) externalRefunds(w http.ResponseWriter, r *http.Request) {
	invoiceID, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	items, total, err := s.db.ListExternalRefunds(r.Context(), identityFrom(r).ID, invoiceID, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []store.ExternalRefund{}
	}
	write(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) createExternalRefund(w http.ResponseWriter, r *http.Request) {
	invoiceID, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.ExternalRefundInput
	if !decode(w, r, &in) {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	ctx := store.WithExternalRefundSession(r.Context(), tokenHash(cookie.Value))
	out, err := s.db.CreateExternalRefund(ctx, identityFrom(r).ID, invoiceID, in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	write(w, status, out)
}

func (s *Server) reviewExternalRefund(w http.ResponseWriter, r *http.Request) {
	invoiceID, ok := pathID(w, r)
	if !ok {
		return
	}
	refundID, err := strconv.ParseInt(r.PathValue("refund_id"), 10, 64)
	if err != nil || refundID < 1 {
		writeError(w, 400, "invalid_id", "退款编号无效")
		return
	}
	var in store.ExternalRefundReviewInput
	if !decode(w, r, &in) {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	ctx := store.WithExternalRefundSession(r.Context(), tokenHash(cookie.Value))
	out, err := s.db.ReviewExternalRefund(ctx, identityFrom(r).ID, invoiceID, refundID, r.PathValue("action") == "confirm", in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	write(w, status, out)
}
