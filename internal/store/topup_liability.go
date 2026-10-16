package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type topupLiabilitySessionKey struct{}

func WithTopupLiabilitySession(ctx context.Context, hash string) context.Context {
	return context.WithValue(ctx, topupLiabilitySessionKey{}, hash)
}

type TopupLiabilitySettlement struct {
	PaymentID   int64     `json:"payment_id"`
	Status      string    `json:"status"`
	AmountCents int64     `json:"amount_cents"`
	LedgerID    *int64    `json:"ledger_id"`
	FailureCode string    `json:"failure_code"`
	CreatedAt   time.Time `json:"created_at"`
}
type TopupLiabilityResolution struct {
	ID          int64     `json:"id"`
	InvoiceID   int64     `json:"invoice_id"`
	PaymentID   int64     `json:"payment_id"`
	UserID      int64     `json:"user_id"`
	AmountCents int64     `json:"amount_cents"`
	LedgerID    int64     `json:"ledger_id"`
	ActorID     int64     `json:"actor_id"`
	Note        string    `json:"note"`
	CreatedAt   time.Time `json:"created_at"`
}
type TopupLiability struct {
	InvoiceID   int64                     `json:"invoice_id"`
	UserID      int64                     `json:"user_id"`
	AmountCents int64                     `json:"amount_cents"`
	Version     int64                     `json:"version"`
	Status      string                    `json:"status"`
	Settlement  *TopupLiabilitySettlement `json:"settlement"`
	Resolution  *TopupLiabilityResolution `json:"resolution"`
	Replayed    bool                      `json:"replayed"`
}
type ResolveTopupLiabilityInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	Note            string `json:"note"`
}

func authorizeTopupLiability(ctx context.Context, tx pgx.Tx, actor int64, write bool) error {
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, `SELECT role,admin_role_id FROM users WHERE id=$1 AND status='active' FOR SHARE`, actor).Scan(&role, &roleID); err != nil {
		return conflict("permission_denied", "员工会话或权限无效")
	}
	if role != "super_admin" {
		if role != "admin" || roleID == nil {
			return conflict("permission_denied", "没有充值责任处理权限")
		}
		var allowed bool
		if err := tx.QueryRow(ctx, `SELECT active AND 'invoices.view'=ANY(permissions) AND (NOT $2 OR 'balance.topup.resolve'=ANY(permissions)) FROM admin_roles WHERE id=$1 FOR SHARE`, *roleID, write).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return conflict("permission_denied", "没有充值责任处理权限")
		}
	}
	if write {
		var id int64
		if err := tx.QueryRow(ctx, `SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE`, ctx.Value(topupLiabilitySessionKey{}), actor).Scan(&id); err != nil {
			return conflict("permission_denied", "补账需要有效员工浏览器会话")
		}
	}
	return nil
}
func readTopupLiability(ctx context.Context, tx pgx.Tx, invoice int64) (TopupLiability, error) {
	var out TopupLiability
	var settlement, resolution []byte
	err := tx.QueryRow(ctx, `SELECT t.invoice_id,t.user_id,t.amount_cents,i.version,t.status,
 (SELECT jsonb_build_object('payment_id',s.payment_id,'status',s.status,'amount_cents',s.amount_cents,'ledger_id',s.ledger_id,'failure_code',s.failure_code,'created_at',s.created_at) FROM balance_topup_settlements s WHERE s.topup_id=t.id),
 (SELECT jsonb_build_object('id',r.id,'invoice_id',r.invoice_id,'payment_id',r.payment_id,'user_id',r.user_id,'amount_cents',r.amount_cents,'ledger_id',r.ledger_id,'actor_id',r.actor_id,'note',r.note,'created_at',r.created_at) FROM balance_topup_liability_resolutions r WHERE r.topup_id=t.id)
 FROM balance_topups t JOIN invoices i ON i.id=t.invoice_id AND i.user_id=t.user_id WHERE i.id=$1 AND i.invoice_kind='topup'`, invoice).Scan(&out.InvoiceID, &out.UserID, &out.AmountCents, &out.Version, &out.Status, &settlement, &resolution)
	if err != nil {
		return out, err
	}
	if settlement != nil {
		err = json.Unmarshal(settlement, &out.Settlement)
		if err != nil {
			return out, err
		}
	}
	if resolution != nil {
		err = json.Unmarshal(resolution, &out.Resolution)
	}
	return out, err
}
func (s *Store) GetTopupLiability(ctx context.Context, actor, invoice int64) (TopupLiability, error) {
	var out TopupLiability
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = LockAdminResourceCustomer(ctx, tx, actor, "invoice", invoice); err != nil {
		return out, err
	}
	if err = authorizeTopupLiability(ctx, tx, actor, false); err != nil {
		return out, err
	}
	// Authorization locks the actor row. Recheck scope with a fresh statement
	// snapshot after any concurrent scope revocation has finished committing.
	// Keep the initial sales advisory lock before the actor lock to preserve
	// the established sales-assignment lock order.
	if err = LockAdminResourceCustomer(ctx, tx, actor, "invoice", invoice); err != nil {
		return out, err
	}
	out, err = readTopupLiability(ctx, tx, invoice)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) ResolveTopupLiability(ctx context.Context, actor, invoice int64, in ResolveTopupLiabilityInput, key string) (TopupLiability, error) {
	var out TopupLiability
	if in.ExpectedVersion < 1 || strings.TrimSpace(in.Note) == "" || !contentTextValid(in.Note, 1000, true) || !utf8.ValidString(key) || utf8.RuneCountInString(key) < 8 || utf8.RuneCountInString(key) > 128 {
		return out, conflict("invalid_topup_resolution", "版本、说明或幂等键无效")
	}
	raw, _ := json.Marshal([]any{invoice, in})
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = LockAdminResourceCustomer(ctx, tx, actor, "invoice", invoice); err != nil {
		return out, err
	}
	if err = authorizeTopupLiability(ctx, tx, actor, true); err != nil {
		return out, err
	}
	// Authorization locks the actor row. Recheck scope with a fresh statement
	// snapshot after any concurrent scope revocation has finished committing.
	// Keep the initial sales advisory lock before the actor lock to preserve
	// the established sales-assignment lock order.
	if err = LockAdminResourceCustomer(ctx, tx, actor, "invoice", invoice); err != nil {
		return out, err
	}
	var uid int64
	if err = tx.QueryRow(ctx, `SELECT user_id FROM invoices WHERE id=$1`, invoice).Scan(&uid); err != nil {
		return out, err
	}
	if err = LockFinancialCustomer(ctx, tx, uid); err != nil {
		return out, err
	}
	if err = lockOperationKey(ctx, tx, "topup-liability", fmt.Sprintf("%d:%s", actor, key)); err != nil {
		return out, err
	}
	var savedHash string
	var saved []byte
	err = tx.QueryRow(ctx, `SELECT request_hash,result FROM balance_topup_liability_resolutions WHERE actor_id=$1 AND idempotency_key=$2`, actor, key).Scan(&savedHash, &saved)
	if err == nil {
		if savedHash != hash {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同补账请求")
		}
		if err = json.Unmarshal(saved, &out); err != nil {
			return out, err
		}
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	// Classification reads do not lock: retain customer -> payment -> invoice -> topup order.
	var payment int64
	if err = tx.QueryRow(ctx, `SELECT payment_id FROM balance_topup_settlements WHERE invoice_id=$1`, invoice).Scan(&payment); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, conflict("topup_not_pending", "充值尚无待补账收款事实")
		}
		return out, err
	}
	var paymentInvoice, paymentAmount, paymentUID int64
	var paymentStatus string
	if err = tx.QueryRow(ctx, `SELECT invoice_id,amount_cents,user_id,status FROM payment_transactions WHERE id=$1 FOR UPDATE`, payment).Scan(&paymentInvoice, &paymentAmount, &paymentUID, &paymentStatus); err != nil {
		return out, err
	}
	var kind, invoiceStatus string
	var invoiceAmount, version int64
	if err = tx.QueryRow(ctx, `SELECT invoice_kind,status,amount_cents,version FROM invoices WHERE id=$1 AND user_id=$2 FOR UPDATE`, invoice, uid).Scan(&kind, &invoiceStatus, &invoiceAmount, &version); err != nil {
		return out, err
	}
	var topupID, amount, topupUID int64
	var state string
	if err = tx.QueryRow(ctx, `SELECT id,user_id,amount_cents,status FROM balance_topups WHERE invoice_id=$1 FOR UPDATE`, invoice).Scan(&topupID, &topupUID, &amount, &state); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, conflict("topup_not_pending", "该账单不是待补账充值")
		}
		return out, err
	}
	var settledInvoice, settledPayment, settledUID, settledAmount int64
	var settledStatus string
	var ledger *int64
	if err = tx.QueryRow(ctx, `SELECT invoice_id,payment_id,user_id,amount_cents,status,ledger_id FROM balance_topup_settlements WHERE topup_id=$1`, topupID).Scan(&settledInvoice, &settledPayment, &settledUID, &settledAmount, &settledStatus, &ledger); err != nil {
		return out, err
	}
	var already bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM balance_topup_liability_resolutions WHERE topup_id=$1) OR EXISTS(SELECT 1 FROM balance_ledger WHERE reference_type='gateway_topup' AND reference_id=$2 AND entry_type='topup')`, topupID, invoice).Scan(&already); err != nil {
		return out, err
	}
	if already || state != "pending_liability" || settledStatus != "pending_liability" || ledger != nil || kind != "topup" || invoiceStatus != "paid" || paymentStatus != "succeeded" || paymentInvoice != invoice || paymentUID != uid || settledInvoice != invoice || settledPayment != payment || topupUID != uid || settledUID != uid || amount != settledAmount || amount != invoiceAmount || amount != paymentAmount {
		return out, conflict("topup_not_pending", "充值责任状态或收款关联异常，请核对")
	}
	if version != in.ExpectedVersion {
		return out, conflict("version_conflict", "账单版本已变化，请重新核对")
	}
	var balance int64
	if err = tx.QueryRow(ctx, `SELECT balance_cents FROM users WHERE id=$1`, uid).Scan(&balance); err != nil {
		return out, err
	}
	if amount > 9223372036854775807-balance {
		return out, conflict("balance_overflow", "余额无法安全入账，请先核对")
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET balance_cents=balance_cents+$2,updated_at=now() WHERE id=$1`, uid, amount); err != nil {
		return out, err
	}
	var ledgerID int64
	if err = tx.QueryRow(ctx, `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id,idempotency_key,note) VALUES($1,'topup',$2,$3,'gateway_topup',$4,$5,'在线充值待补账处理') RETURNING id`, uid, amount, balance+amount, invoice, fmt.Sprintf("gateway-topup-%d", invoice)).Scan(&ledgerID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE balance_topups SET status='credited',updated_at=clock_timestamp() WHERE id=$1`, topupID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE invoices SET updated_at=now() WHERE id=$1`, invoice); err != nil {
		return out, err
	}
	var resolution TopupLiabilityResolution
	// The immutable row is inserted only once; compute its receipt before insertion.
	out, err = readTopupLiability(ctx, tx, invoice)
	if err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('balance_topup_liability_resolutions','id')),clock_timestamp()`).Scan(&resolution.ID, &resolution.CreatedAt); err != nil {
		return out, err
	}
	resolution.InvoiceID = invoice
	resolution.PaymentID = payment
	resolution.UserID = uid
	resolution.AmountCents = amount
	resolution.LedgerID = ledgerID
	resolution.ActorID = actor
	resolution.Note = in.Note
	out.Resolution = &resolution
	receipt, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO balance_topup_liability_resolutions(id,topup_id,invoice_id,payment_id,user_id,amount_cents,ledger_id,actor_id,note,created_at,idempotency_key,request_hash,result) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, resolution.ID, topupID, invoice, payment, uid, amount, ledgerID, actor, in.Note, resolution.CreatedAt, key, hash, receipt); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, "balance.topup.liability_resolved", "invoice", invoice, map[string]any{"resolution_id": resolution.ID, "ledger_id": ledgerID, "amount_cents": amount, "note": in.Note}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
