package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	uid := identityFrom(r).ID
	var balance int64
	var phone string
	var phoneVerified, emailVerified bool
	if err := s.db.Pool.QueryRow(r.Context(), "SELECT balance_cents,phone,phone_verified,email_verified FROM users WHERE id=$1", uid).Scan(&balance, &phone, &phoneVerified, &emailVerified); err != nil {
		s.fail(w, r, err)
		return
	}
	limit, offset := pagination(r)
	rows, err := s.db.Pool.Query(r.Context(), `SELECT id,entry_type,delta_cents,balance_after,reference_type,reference_id,note,created_at
FROM balance_ledger WHERE user_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3`, uid, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	write(w, 200, map[string]any{"balance_cents": balance, "ledger": items, "phone": phone, "phone_verified": phoneVerified, "email_verified": emailVerified})
}

func (s *Server) payInvoice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	var expected []int64
	if input.ExpectedVersion != 0 {
		expected = []int64{input.ExpectedVersion}
	}
	result, err := s.db.PayInvoice(r.Context(), identityFrom(r).ID, id, r.Header.Get("Idempotency-Key"), expected...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if !result.Replayed {
		status = http.StatusCreated
	}
	write(w, status, result)
}

func (s *Server) adminTopUp(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		AmountCents int64  `json:"amount_cents"`
		Note        string `json:"note"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Note = strings.TrimSpace(input.Note)
	if len(input.Note) > 240 {
		writeError(w, 400, "invalid_topup", "备注过长")
		return
	}
	balance, replayed, err := s.db.TopUp(r.Context(), identityFrom(r).ID, id, input.AmountCents, r.Header.Get("Idempotency-Key"), input.Note)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	write(w, status, map[string]any{"user_id": id, "balance_cents": balance, "replayed": replayed})
}

func (s *Server) adminRefundInvoice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct{}
	if !decode(w, r, &input) {
		return
	}
	cookie, _ := r.Cookie("zjmfgo_session")
	ctx := r.Context()
	if cookie != nil {
		ctx = store.WithCreditRefundSession(ctx, tokenHash(cookie.Value))
	}
	result, err := s.db.RefundInvoice(ctx, identityFrom(r).ID, id, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	write(w, status, result)
}
