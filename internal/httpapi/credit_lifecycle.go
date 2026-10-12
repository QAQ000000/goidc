package httpapi

import (
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

func (s *Server) payCreditInvoice(w http.ResponseWriter, r *http.Request) {
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
	cookie, cookieErr := r.Cookie("zjmfgo_session")
	if cookieErr != nil {
		writeError(w, 401, "unauthorized", "会话失效")
		return
	}
	out, err := s.db.PayInvoiceCredit(r.Context(), identityFrom(r).ID, id, r.Header.Get("Idempotency-Key"), in.ExpectedVersion, tokenHash(cookie.Value))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	code := 201
	if out.Replayed {
		code = 200
	}
	write(w, code, out)
}
func creditFilters(w http.ResponseWriter, r *http.Request) (*int64, *time.Time, *time.Time, string, bool) {
	var customer *int64
	var from, to *time.Time
	if raw := r.URL.Query().Get("customer_id"); raw != "" {
		id, err := parsePositiveID(raw)
		if err != nil {
			writeError(w, 400, "invalid_customer", "客户ID无效")
			return nil, nil, nil, "", false
		}
		customer = &id
	}
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	for _, field := range []string{"date_from", "date_to"} {
		raw := r.URL.Query().Get(field)
		if raw == "" {
			continue
		}
		v, err := time.ParseInLocation("2006-01-02", raw, zone)
		if err != nil {
			writeError(w, 400, "invalid_date", "日期无效")
			return nil, nil, nil, "", false
		}
		if field == "date_from" {
			from = &v
		} else {
			v = v.AddDate(0, 0, 1)
			to = &v
		}
	}
	if from != nil && to != nil && !from.Before(*to) {
		writeError(w, 400, "invalid_date_range", "日期范围无效")
		return nil, nil, nil, "", false
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "outstanding" && status != "repaid" && status != "refunded" {
		writeError(w, 400, "invalid_credit_status", "信用状态无效")
		return nil, nil, nil, "", false
	}
	return customer, from, to, status, true
}
func (s *Server) creditConsumptions(w http.ResponseWriter, r *http.Request) {
	customer, from, to, status, ok := creditFilters(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	s.adminListPage(w, r, `SELECT c.invoice_id AS id,c.user_id,u.display_name AS customer_name,i.invoice_no,c.amount_cents,c.remaining_cents,c.status,c.version,c.created_at FROM credit_invoice_payments c JOIN users u ON u.id=c.user_id JOIN invoices i ON i.id=c.invoice_id WHERE admin_customer_allowed($1,c.user_id) AND (u.display_name ILIKE $2 OR i.invoice_no ILIKE $2 OR i.id::text ILIKE $2) AND ($3::bigint IS NULL OR c.user_id=$3) AND ($4::timestamptz IS NULL OR c.created_at>=$4) AND ($5::timestamptz IS NULL OR c.created_at<$5) AND ($6='' OR c.status=$6)`, "created_at DESC,id DESC", limit, offset, identityFrom(r).ID, "%"+r.URL.Query().Get("q")+"%", customer, from, to, status)
}
func (s *Server) creditRepayments(w http.ResponseWriter, r *http.Request) {
	customer, from, to, status, ok := creditFilters(w, r)
	if !ok {
		return
	}
	if status != "" {
		writeError(w, 400, "invalid_repayment_status", "还款记录仅包含已确认到账记录")
		return
	}
	limit, offset := pagination(r)
	s.adminListPage(w, r, `SELECT p.id,p.invoice_id,p.user_id,u.display_name AS customer_name,i.invoice_no,p.amount_cents,p.receipt,p.received_at,p.note,p.created_at FROM credit_repayments p JOIN users u ON u.id=p.user_id JOIN invoices i ON i.id=p.invoice_id WHERE admin_customer_allowed($1,p.user_id) AND (u.display_name ILIKE $2 OR i.invoice_no ILIKE $2 OR p.receipt ILIKE $2) AND ($3::bigint IS NULL OR p.user_id=$3) AND ($4::timestamptz IS NULL OR p.received_at>=$4) AND ($5::timestamptz IS NULL OR p.received_at<$5)`, "received_at DESC,id DESC", limit, offset, identityFrom(r).ID, "%"+r.URL.Query().Get("q")+"%", customer, from, to)
}
func (s *Server) creditRepay(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Amount          int64     `json:"amount_cents"`
		ExpectedVersion int64     `json:"expected_version"`
		Receipt         string    `json:"receipt"`
		ReceivedAt      time.Time `json:"received_at"`
		Note            string    `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.ReceivedAt = in.ReceivedAt.Truncate(time.Microsecond)
	key := r.Header.Get("Idempotency-Key")
	in.Receipt = strings.TrimSpace(in.Receipt)
	if in.Amount < 1 || in.ExpectedVersion < 1 || len(in.Receipt) < 3 || len(in.Receipt) > 240 || len(in.Note) > 240 || len(key) < 8 || len(key) > 128 || in.ReceivedAt.IsZero() || in.ReceivedAt.After(time.Now()) {
		writeError(w, 400, "invalid_repayment", "请输入正数金额、版本、到账时间和真实到账凭证")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	actor := identityFrom(r).ID
	var uid int64
	if err = tx.QueryRow(r.Context(), "SELECT user_id FROM invoices WHERE id=$1", id).Scan(&uid); err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.lockCreditManager(w, r, tx, uid) {
		return
	}
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("credit-repay:%d:%s", actor, key)); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "SELECT id FROM invoices WHERE id=$1 FOR UPDATE", id); err != nil {
		s.fail(w, r, err)
		return
	}
	var previousID, previousAmount, previousVersion int64
	var previousReceipt, previousNote string
	var previousTime time.Time
	err = tx.QueryRow(r.Context(), "SELECT invoice_id,amount_cents,expected_version,receipt,received_at,note FROM credit_repayments WHERE actor_id=$1 AND idempotency_key=$2", actor, key).Scan(&previousID, &previousAmount, &previousVersion, &previousReceipt, &previousTime, &previousNote)
	if err == nil {
		if previousID != id || previousAmount != in.Amount || previousVersion != in.ExpectedVersion || previousReceipt != in.Receipt || !previousTime.Equal(in.ReceivedAt) || previousNote != in.Note {
			writeError(w, 409, "idempotency_conflict", "幂等键对应其他还款")
			return
		}
		write(w, 200, map[string]any{"replayed": true})
		return
	}
	if err != pgx.ErrNoRows {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "credit-receipt:"+in.Receipt); err != nil {
		s.fail(w, r, err)
		return
	}
	var receiptExists bool
	if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM credit_repayments WHERE receipt=$1)", in.Receipt).Scan(&receiptExists); err != nil {
		s.fail(w, r, err)
		return
	}
	if receiptExists {
		writeError(w, 409, "receipt_already_recorded", "到账凭证已登记，不能重复入账")
		return
	}
	var remaining, version int64
	var status string
	if err = tx.QueryRow(r.Context(), "SELECT remaining_cents,version,status FROM credit_invoice_payments WHERE invoice_id=$1 FOR UPDATE", id).Scan(&remaining, &version, &status); err != nil {
		s.fail(w, r, err)
		return
	}
	if version != in.ExpectedVersion || status != "outstanding" || in.Amount > remaining {
		writeError(w, 409, "stale_credit_payment", "消费已更新或还款金额超过剩余债务")
		return
	}
	remaining -= in.Amount
	if remaining == 0 {
		status = "repaid"
	}
	if _, err = tx.Exec(r.Context(), "UPDATE customer_credit_accounts SET used_cents=used_cents-$2,version=version+1,updated_at=now() WHERE user_id=$1", uid, in.Amount); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE credit_invoice_payments SET remaining_cents=$2,status=$3,version=version+1 WHERE invoice_id=$1", id, remaining, status); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,note,idempotency_key,expected_version,version_after,remaining_after) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)", id, uid, actor, in.Amount, in.Receipt, in.ReceivedAt, in.Note, key, version, version+1, remaining); err != nil {
		s.fail(w, r, err)
		return
	}
	if remaining == 0 {
		if err = s.db.QueueAffiliateCommissions(r.Context(), tx, id, 0); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err = store.Audit(r.Context(), tx, actor, "credit.repayment_confirmed", "invoice", id, in); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 201, map[string]any{"remaining_cents": remaining, "version": version + 1})
}
func (s *Server) creditFreeze(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Frozen          bool   `json:"frozen"`
		ExpectedVersion int64  `json:"expected_version"`
		Note            string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if in.ExpectedVersion < 1 || len(key) < 8 || len(key) > 128 || len(in.Note) > 240 {
		writeError(w, 400, "invalid_credit_freeze", "版本或幂等键无效")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if !s.lockCreditManager(w, r, tx, id) {
		return
	}
	actor := identityFrom(r).ID
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("credit-freeze:%d:%s", actor, key)); err != nil {
		s.fail(w, r, err)
		return
	}
	var oldID, oldExpected, oldVersion int64
	var oldFrozen bool
	var oldNote string
	err = tx.QueryRow(r.Context(), "SELECT user_id,frozen,expected_version,version_after,note FROM credit_account_operations WHERE actor_id=$1 AND idempotency_key=$2", actor, key).Scan(&oldID, &oldFrozen, &oldExpected, &oldVersion, &oldNote)
	if err == nil {
		if oldID != id || oldFrozen != in.Frozen || oldExpected != in.ExpectedVersion || oldNote != in.Note {
			writeError(w, 409, "idempotency_conflict", "幂等键对应其他操作")
			return
		}
		write(w, 200, map[string]any{"version": oldVersion, "replayed": true})
		return
	}
	if err != pgx.ErrNoRows {
		s.fail(w, r, err)
		return
	}
	var version int64
	if err = tx.QueryRow(r.Context(), "SELECT version FROM customer_credit_accounts WHERE user_id=$1 FOR UPDATE", id).Scan(&version); err != nil {
		s.fail(w, r, err)
		return
	}
	if version != in.ExpectedVersion {
		writeError(w, 409, "stale_credit", "信用账户已更新")
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE customer_credit_accounts SET frozen=$2,version=version+1,updated_at=now() WHERE user_id=$1", id, in.Frozen); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "INSERT INTO credit_account_operations(actor_id,idempotency_key,user_id,frozen,expected_version,version_after,note) VALUES($1,$2,$3,$4,$5,$6,$7)", actor, key, id, in.Frozen, version, version+1, in.Note); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, actor, "credit.freeze_changed", "customer_credit_account", id, in); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 201, map[string]any{"version": version + 1})
}
