package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type topupSessionKey struct{}

func WithTopupSession(ctx context.Context, hash string) context.Context {
	return context.WithValue(ctx, topupSessionKey{}, hash)
}
func requireTopupSession(ctx context.Context, tx pgx.Tx, uid int64) error {
	var id int64
	if err := tx.QueryRow(ctx, `SELECT u.id FROM users u JOIN sessions s ON s.user_id=u.id WHERE u.id=$1 AND u.role='customer' AND u.status='active' AND s.token_hash=$2 AND s.expires_at>clock_timestamp() FOR SHARE OF u,s`, uid, ctx.Value(topupSessionKey{})).Scan(&id); err != nil {
		return conflict("permission_denied", "充值需要有效客户浏览器会话")
	}
	return nil
}

type TopupOptions struct {
	Enabled                bool   `json:"enabled"`
	MinTopupCents          int64  `json:"min_topup_cents"`
	MaxTopupCents          int64  `json:"max_topup_cents"`
	MaxBalanceCents        int64  `json:"max_balance_cents"`
	BalanceCents           int64  `json:"balance_cents"`
	ReservedCents          int64  `json:"reserved_cents"`
	AvailableCapacityCents *int64 `json:"available_capacity_cents"`
	RequireActiveOrder     bool   `json:"require_active_order"`
	HasActiveOrder         bool   `json:"has_active_order"`
	Version                int64  `json:"version"`
}

func readTopupOptions(ctx context.Context, tx pgx.Tx, uid int64) (TopupOptions, error) {
	var o TopupOptions
	err := tx.QueryRow(ctx, `SELECT f.topup_enabled,f.min_topup_cents,f.max_topup_cents,f.max_balance_cents,f.require_active_order,f.version,u.balance_cents,
 (SELECT coalesce(sum(amount_cents),0)::bigint FROM balance_topups WHERE user_id=u.id AND status IN ('accepted','pending_liability')),
 EXISTS(SELECT 1 FROM orders WHERE user_id=u.id AND status IN ('paid','active') AND deleted_at IS NULL)
 FROM finance_settings f JOIN users u ON u.id=$1 AND u.role='customer' WHERE f.id=1 FOR SHARE OF f`, uid).Scan(&o.Enabled, &o.MinTopupCents, &o.MaxTopupCents, &o.MaxBalanceCents, &o.RequireActiveOrder, &o.Version, &o.BalanceCents, &o.ReservedCents, &o.HasActiveOrder)
	if err == nil && o.MaxBalanceCents > 0 {
		n := topupCapacity(o.MaxBalanceCents, o.BalanceCents, o.ReservedCents)
		o.AvailableCapacityCents = &n
	}
	return o, err
}

// Subtract only after ordering checks: a lowered ceiling plus a near-MaxInt64
// balance and outstanding liabilities must never wrap into positive capacity.
func topupCapacity(limit, balance, reserved int64) int64 {
	if balance >= limit {
		return 0
	}
	remaining := limit - balance
	if reserved >= remaining {
		return 0
	}
	return remaining - reserved
}

func (s *Store) OnlineTopupOptions(ctx context.Context, uid int64) (TopupOptions, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return TopupOptions{}, err
	}
	defer tx.Rollback(ctx)
	if err = requireTopupSession(ctx, tx, uid); err != nil {
		return TopupOptions{}, err
	}
	o, err := readTopupOptions(ctx, tx, uid)
	if err != nil {
		return o, err
	}
	return o, tx.Commit(ctx)
}
func validateTopupOptions(o TopupOptions, amount, expected int64) error {
	if expected != o.Version {
		return conflict("version_conflict", "充值设置已更新，请刷新")
	}
	if !o.Enabled {
		return conflict("topup_disabled", "充值功能已关闭")
	}
	if amount < o.MinTopupCents {
		return conflict("topup_below_minimum", "充值金额低于最小值")
	}
	if amount > o.MaxTopupCents {
		return conflict("topup_above_maximum", "充值金额超过最大值")
	}
	if o.RequireActiveOrder && !o.HasActiveOrder {
		return conflict("topup_active_order_required", "充值前必须存在已支付或已激活订单")
	}
	if o.AvailableCapacityCents != nil && amount > *o.AvailableCapacityCents {
		return conflict("topup_balance_limit", "余额及待到账充值超过余额上限")
	}
	return nil
}

type OnlineTopupInput struct {
	AmountCents     int64 `json:"amount_cents"`
	ExpectedVersion int64 `json:"expected_version"`
}
type OnlineTopupResult struct {
	InvoiceID       int64  `json:"invoice_id"`
	InvoiceNo       string `json:"invoice_no"`
	AmountCents     int64  `json:"amount_cents"`
	Status          string `json:"status"`
	Version         int64  `json:"version"`
	SettingsVersion int64  `json:"settings_version"`
	Replayed        bool   `json:"replayed"`
}

func (s *Store) CreateOnlineTopup(ctx context.Context, uid int64, in OnlineTopupInput, key string) (OnlineTopupResult, error) {
	var out OnlineTopupResult
	if in.AmountCents < 1 || in.AmountCents > 10000000000 || in.ExpectedVersion < 1 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_topup", "充值金额、版本或幂等键无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = LockFinancialCustomer(ctx, tx, uid); err != nil {
		return out, err
	}
	if err = requireTopupSession(ctx, tx, uid); err != nil {
		return out, err
	}
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	var old string
	err = tx.QueryRow(ctx, `SELECT t.invoice_id,i.invoice_no,t.amount_cents,t.status,i.version,t.settings_version,t.request_hash FROM balance_topups t JOIN invoices i ON i.id=t.invoice_id WHERE t.user_id=$1 AND t.idempotency_key=$2`, uid, key).Scan(&out.InvoiceID, &out.InvoiceNo, &out.AmountCents, &out.Status, &out.Version, &out.SettingsVersion, &old)
	if err == nil {
		if old != hash {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同充值")
		}
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	o, err := readTopupOptions(ctx, tx, uid)
	if err != nil {
		return out, err
	}
	if err = validateTopupOptions(o, in.AmountCents, in.ExpectedVersion); err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,amount_cents,due_at) VALUES('T-'||replace(gen_random_uuid()::text,'-',''),$1,'topup',$2,now()+interval '1 day') RETURNING id,invoice_no,version`, uid, in.AmountCents).Scan(&out.InvoiceID, &out.InvoiceNo, &out.Version)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents) VALUES($1,'账户余额充值','topup',$2)`, out.InvoiceID, in.AmountCents); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO balance_topups(invoice_id,user_id,amount_cents,settings_version,idempotency_key,request_hash) VALUES($1,$2,$3,$4,$5,$6)`, out.InvoiceID, uid, in.AmountCents, o.Version, key, hash); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, uid, "balance.topup.created", "invoice", out.InvoiceID, map[string]any{"amount_cents": in.AmountCents}); err != nil {
		return out, err
	}
	out.AmountCents = in.AmountCents
	out.Status = "created"
	out.SettingsVersion = o.Version
	return out, tx.Commit(ctx)
}

// All topup paths take the customer row before payment and invoice rows. The
// classification read takes no locks and cannot introduce a reverse wait edge.
func lockOnlineTopupCustomer(ctx context.Context, tx pgx.Tx, invoice int64) (bool, error) {
	var uid int64
	var kind string
	if err := tx.QueryRow(ctx, "SELECT user_id,invoice_kind FROM invoices WHERE id=$1", invoice).Scan(&uid, &kind); err != nil {
		return false, err
	}
	if kind != "topup" {
		return false, nil
	}
	return true, LockFinancialCustomer(ctx, tx, uid)
}
func acceptOnlineTopup(ctx context.Context, tx pgx.Tx, uid, invoice int64) error {
	var amount, version int64
	var state string
	if err := requireTopupSession(ctx, tx, uid); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, "SELECT amount_cents,settings_version,status FROM balance_topups WHERE invoice_id=$1 AND user_id=$2 FOR UPDATE", invoice, uid).Scan(&amount, &version, &state); err != nil {
		return err
	}
	if state == "accepted" {
		var safelyClosed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=$1 AND status='closed') AND NOT EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=$1 AND status<>'closed') AND NOT EXISTS(SELECT 1 FROM payment_close_anomalies WHERE invoice_id=$1 AND payment_close_anomaly_unresolved(id)) AND NOT EXISTS(SELECT 1 FROM balance_topup_settlements WHERE invoice_id=$1)`, invoice).Scan(&safelyClosed); err != nil {
			return err
		}
		// Reuse the original accepted reservation and immutable policy snapshot;
		// never reserve the same topup twice or silently accept current policies.
		if safelyClosed {
			return nil
		}
	}
	if state != "created" {
		return conflict("topup_not_payable", "充值已接纳或已结束，请继续原支付单")
	}
	o, err := readTopupOptions(ctx, tx, uid)
	if err != nil {
		return err
	}
	if err = validateTopupOptions(o, amount, version); err != nil {
		return err
	}
	snapshot, _ := json.Marshal(o)
	_, err = tx.Exec(ctx, "UPDATE balance_topups SET status='accepted',accepted_at=clock_timestamp(),accepted_settings=$2,updated_at=clock_timestamp() WHERE invoice_id=$1", invoice, snapshot)
	return err
}

// Keep the merchant receipt even if account posting unexpectedly fails. A
// savepoint removes every partial balance write before recording the liability.
func settleOnlineTopup(ctx context.Context, tx pgx.Tx, invoice, payment int64) error {
	var id, uid, amount int64
	var state string
	if err := tx.QueryRow(ctx, "SELECT id,user_id,amount_cents,status FROM balance_topups WHERE invoice_id=$1 FOR UPDATE", invoice).Scan(&id, &uid, &amount, &state); err != nil {
		return err
	}
	if state != "accepted" {
		return conflict("topup_state_conflict", "充值接纳状态异常，需核对")
	}
	var balance int64
	if err := tx.QueryRow(ctx, "SELECT balance_cents FROM users WHERE id=$1", uid).Scan(&balance); err != nil {
		return err
	}
	status, failure := "credited", ""
	var ledger *int64
	if amount > 9223372036854775807-balance {
		status = "pending_liability"
		failure = "balance_overflow"
	} else {
		nested, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		_, err = nested.Exec(ctx, "UPDATE users SET balance_cents=balance_cents+$2,updated_at=now() WHERE id=$1", uid, amount)
		var lid int64
		if err == nil {
			err = nested.QueryRow(ctx, `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id,idempotency_key,note) VALUES($1,'topup',$2,$3,'gateway_topup',$4,$5,'在线充值到账') RETURNING id`, uid, amount, balance+amount, invoice, fmt.Sprintf("gateway-topup-%d", invoice)).Scan(&lid)
		}
		if err != nil {
			if rollbackErr := nested.Rollback(ctx); rollbackErr != nil {
				return rollbackErr
			}
			status = "pending_liability"
			failure = "balance_posting_failed"
		} else {
			if err = nested.Commit(ctx); err != nil {
				return err
			}
			ledger = &lid
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO balance_topup_settlements(topup_id,invoice_id,payment_id,user_id,amount_cents,status,ledger_id,failure_code) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, invoice, payment, uid, amount, status, ledger, failure); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE balance_topups SET status=$2,updated_at=clock_timestamp() WHERE id=$1", id, status); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1", invoice); err != nil {
		return err
	}
	return Audit(ctx, tx, uid, "balance.topup."+status, "invoice", invoice, map[string]any{"payment_id": payment, "amount_cents": amount, "failure_code": failure})
}

type OnlineTopupCancelInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	Note            string `json:"note"`
}

func (s *Store) CancelOnlineTopup(ctx context.Context, uid, invoice int64, in OnlineTopupCancelInput, key string) (OnlineTopupResult, error) {
	var out OnlineTopupResult
	if in.ExpectedVersion < 1 || len(key) < 8 || len(key) > 128 || !contentTextValid(in.Note, 1000, true) {
		return out, conflict("invalid_topup", "充值版本或幂等键无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = LockFinancialCustomer(ctx, tx, uid); err != nil {
		return out, err
	}
	if err = requireTopupSession(ctx, tx, uid); err != nil {
		return out, err
	}
	raw, _ := json.Marshal([]any{invoice, in})
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if replayed, err := ReplayOperation(ctx, tx, uid, "balance-topup", key, hash, &out); err != nil {
		return out, err
	} else if replayed {
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	err = tx.QueryRow(ctx, `SELECT t.invoice_id,i.invoice_no,t.amount_cents,t.status,i.version,t.settings_version FROM balance_topups t JOIN invoices i ON i.id=t.invoice_id WHERE t.user_id=$1 AND t.invoice_id=$2 FOR UPDATE OF i,t`, uid, invoice).Scan(&out.InvoiceID, &out.InvoiceNo, &out.AmountCents, &out.Status, &out.Version, &out.SettingsVersion)
	if err != nil {
		return out, err
	}
	if out.Status != "created" && out.Status != "accepted" {
		return out, conflict("topup_payment_protected", "已接纳或已结束的充值不能取消；请继续原支付或联系财务核对")
	}
	if out.Version != in.ExpectedVersion {
		return out, conflict("version_conflict", "充值账单版本已变化")
	}
	var hasPayment bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=$1 AND status<>'closed') OR EXISTS(SELECT 1 FROM payment_close_anomalies WHERE invoice_id=$1 AND payment_close_anomaly_unresolved(id))", invoice).Scan(&hasPayment); err != nil {
		return out, err
	}
	if hasPayment {
		return out, conflict("topup_payment_protected", "充值已有支付记录，不能直接取消")
	}
	if err = tx.QueryRow(ctx, "UPDATE invoices SET status='cancelled' WHERE id=$1 RETURNING version", invoice).Scan(&out.Version); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, "UPDATE balance_topups SET status='cancelled',updated_at=clock_timestamp() WHERE invoice_id=$1", invoice); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, uid, "balance.topup.cancelled", "invoice", invoice, map[string]any{"note": in.Note}); err != nil {
		return out, err
	}
	out.Status = "cancelled"
	if err = SaveOperation(ctx, tx, uid, "balance-topup", key, hash, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
