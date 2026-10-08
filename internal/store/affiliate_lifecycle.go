package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// Financial callers keep their invoice/order/service locks first. All affiliate
// bucket writes then serialize on the account. No external I/O is performed.
func lockAffiliateAccount(ctx context.Context, tx pgx.Tx, id int64) error {
	var locked int64
	return tx.QueryRow(ctx, "SELECT id FROM affiliate_accounts WHERE id=$1 FOR UPDATE", id).Scan(&locked)
}
func affiliateAudit(ctx context.Context, tx pgx.Tx, account, actor int64, action, object string, id int64, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO affiliate_audit_events(account_id,actor_id,event_type,object_type,object_id,payload) VALUES($1,$2,$3,$4,$5,$6)", nullableInt64(account), nullableInt64(actor), action, object, fmt.Sprint(id), raw)
	if err != nil {
		return err
	}
	return Audit(ctx, tx, actor, action, object, id, payload)
}
func affiliateEvent(ctx context.Context, tx pgx.Tx, entry, actor, withdrawal, run int64, event string, delta int64, before, reason, key string) error {
	_, err := tx.Exec(ctx, "INSERT INTO affiliate_commission_events(entry_id,event_type,delta_cents,before_status,after_status,reason,idempotency_key,actor_id,withdrawal_id,settlement_run_id,bucket_snapshot) SELECT id,$2,$3,$4,status,$5,$6,$7,$8,$9,jsonb_build_object('pending',pending_cents,'available',available_cents,'reserved',reserved_cents,'paid',paid_cents,'reversed',reversed_cents) FROM affiliate_commission_entries WHERE id=$1", entry, event, delta, before, reason, key, nullableInt64(actor), nullableInt64(withdrawal), nullableInt64(run))
	return err
}
func (s *Store) ReverseAffiliateCommissions(ctx context.Context, tx pgx.Tx, invoice, actor int64, reason string) error {
	rows, err := tx.Query(ctx, "SELECT DISTINCT account_id FROM affiliate_commission_entries WHERE invoice_id=$1 ORDER BY account_id", invoice)
	if err != nil {
		return err
	}
	accounts, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for _, account := range accounts {
		if err = lockAffiliateAccount(ctx, tx, account); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, "SELECT id,status,pending_cents+available_cents,reserved_cents+paid_cents FROM affiliate_commission_entries WHERE invoice_id=$1 AND account_id=$2 AND reversal_at IS NULL ORDER BY id FOR UPDATE", invoice, account)
		if err != nil {
			return err
		}
		type item struct {
			id              int64
			status          string
			free, committed int64
		}
		var items []item
		for rows.Next() {
			var i item
			if err = rows.Scan(&i.id, &i.status, &i.free, &i.committed); err != nil {
				rows.Close()
				return err
			}
			items = append(items, i)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, i := range items {
			if _, err = tx.Exec(ctx, "UPDATE affiliate_commission_entries SET reversed_cents=reversed_cents+pending_cents+available_cents,pending_cents=0,available_cents=0,reversal_at=now() WHERE id=$1", i.id); err != nil {
				return err
			}
			if i.committed > 0 {
				if _, err = tx.Exec(ctx, "INSERT INTO affiliate_clawbacks(entry_id,withdrawal_id,amount_cents,outstanding_cents) SELECT entry_id,withdrawal_id,amount_cents,amount_cents FROM affiliate_withdrawal_allocations WHERE entry_id=$1 AND status IN ('reserved','paid') ON CONFLICT DO NOTHING", i.id); err != nil {
					return err
				}
			}
			event := "reversed"
			if i.committed > 0 {
				event = "clawback"
			}
			if err = affiliateEvent(ctx, tx, i.id, actor, 0, 0, event, -(i.free + i.committed), i.status, reason, fmt.Sprintf("affiliate:reverse:%d", i.id)); err != nil {
				return err
			}
			if err = affiliateAudit(ctx, tx, account, actor, "affiliate.commission."+event, "affiliate_commission", i.id, map[string]any{"invoice_id": invoice, "reversed_cents": i.free, "clawback_cents": i.committed, "reason": reason}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Each minute is a leased batch. Per-entry short transactions permit a failed
// entry to retry without rolling back commissions already released.
func (s *Store) SettleAffiliateCommissions(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	owner, err := newAffiliateCode()
	if err != nil {
		return 0, err
	}
	var run int64
	err = s.Pool.QueryRow(ctx, "INSERT INTO affiliate_settlement_runs(run_key,kind,window_start,window_end,lease_owner,lease_until) VALUES('commission-release:'||to_char(date_trunc('minute',now()),'YYYYMMDDHH24MI'),'commission_release',date_trunc('minute',now()),now(),$1,now()+interval '2 minutes') ON CONFLICT(run_key) DO UPDATE SET status='running',lease_owner=EXCLUDED.lease_owner,lease_until=EXCLUDED.lease_until,finished_at=NULL WHERE affiliate_settlement_runs.status='failed' OR (affiliate_settlement_runs.status='running' AND affiliate_settlement_runs.lease_until<now()) RETURNING id", owner).Scan(&run)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count, failures := 0, 0
	var cursor int64
	for {
		rows, err := s.Pool.Query(ctx, "SELECT id FROM affiliate_commission_entries WHERE id>$1 AND pending_cents>0 AND available_at<=now() AND reversal_at IS NULL ORDER BY id LIMIT $2", cursor, limit)
		if err != nil {
			return count, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return count, err
		}
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			cursor = id
			released, e := s.releaseAffiliateEntry(ctx, id, run, owner)
			if e != nil {
				if ctx.Err() != nil {
					return count, ctx.Err()
				}
				failures++
				tag, err := s.Pool.Exec(ctx, `WITH owned AS (UPDATE affiliate_settlement_runs SET error_count=error_count+1,lease_until=clock_timestamp()+interval '2 minutes' WHERE id=$1 AND lease_owner=$3 AND status='running' AND lease_until>clock_timestamp() RETURNING id) INSERT INTO affiliate_settlement_errors(run_id,entry_id,error) SELECT id,$2,'commission release failed' FROM owned ON CONFLICT(run_id,entry_id) DO UPDATE SET error=EXCLUDED.error`, run, id, owner)
				if err != nil {
					return count, err
				}
				if tag.RowsAffected() == 0 {
					return count, conflict("settlement_lease_lost", "佣金结算租约失效")
				}
			} else if released {
				count++
			}
		}
		if len(ids) < limit {
			break
		}
	}
	tag, err := s.Pool.Exec(ctx, "UPDATE affiliate_settlement_runs SET status=CASE WHEN $2>0 THEN 'failed' ELSE 'succeeded' END,finished_at=now(),lease_until=now(),error=CASE WHEN $2>0 THEN 'commission release failed' ELSE '' END WHERE id=$1 AND lease_owner=$3 AND status='running'", run, failures, owner)
	if err != nil {
		return count, err
	}
	if tag.RowsAffected() == 0 {
		return count, conflict("settlement_lease_lost", "佣金结算租约失效")
	}
	if failures > 0 {
		return count, fmt.Errorf("affiliate settlement: %d entries failed", failures)
	}
	return count, nil
}

func (s *Store) releaseAffiliateEntry(ctx context.Context, id, run int64, owner string) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var runID int64
	if err = tx.QueryRow(ctx, "UPDATE affiliate_settlement_runs SET lease_until=clock_timestamp()+interval '2 minutes' WHERE id=$1 AND lease_owner=$2 AND status='running' AND lease_until>clock_timestamp() RETURNING id", run, owner).Scan(&runID); err != nil {
		return false, err
	}
	var invoice, account int64
	if err = tx.QueryRow(ctx, "SELECT invoice_id,account_id FROM affiliate_commission_entries WHERE id=$1", id).Scan(&invoice, &account); err != nil {
		return false, err
	}
	var invoiceStatus string
	var order *int64
	if err = tx.QueryRow(ctx, "SELECT status,order_id FROM invoices WHERE id=$1 FOR UPDATE", invoice).Scan(&invoiceStatus, &order); err != nil {
		return false, err
	}
	if order != nil {
		var orderStatus, review string
		if err = tx.QueryRow(ctx, "SELECT status,review_status FROM orders WHERE id=$1 FOR UPDATE", *order).Scan(&orderStatus, &review); err != nil {
			return false, err
		}
		if orderStatus == "cancelled" || orderStatus == "refunded" || review != "approved" {
			return false, tx.Commit(ctx)
		}
	}
	rows, err := tx.Query(ctx, "SELECT s.id,s.status,s.import_review_required FROM services s WHERE s.order_id=$1 OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=$2 AND ii.service_id=s.id) ORDER BY s.id FOR UPDATE", order, invoice)
	if err != nil {
		return false, err
	}
	blocked := false
	for rows.Next() {
		var sid int64
		var status string
		var review bool
		if err = rows.Scan(&sid, &status, &review); err != nil {
			rows.Close()
			return false, err
		}
		if review || status != "active" {
			blocked = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	var held bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM provision_jobs j JOIN services s ON s.id=j.service_id WHERE (s.order_id=$1 OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=$2 AND ii.service_id=s.id)) AND j.status IN ('pending','running','waiting_manual','waiting_verification')) OR EXISTS(SELECT 1 FROM invoices WHERE id=$2 AND import_review_required) OR EXISTS(SELECT 1 FROM service_cancellation_requests c JOIN services s ON s.id=c.service_id WHERE (s.order_id=$1 OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=$2 AND ii.service_id=s.id)) AND c.status IN ('pending','approved'))", order, invoice).Scan(&held); err != nil {
		return false, err
	}
	if blocked || held || invoiceStatus != "paid" {
		return false, tx.Commit(ctx)
	}
	if err = lockAffiliateAccount(ctx, tx, account); err != nil {
		return false, err
	}
	var amount int64
	var before string
	if err = tx.QueryRow(ctx, "SELECT pending_cents,status FROM affiliate_commission_entries WHERE id=$1 AND available_at<=now() AND reversal_at IS NULL FOR UPDATE", id).Scan(&amount, &before); err == pgx.ErrNoRows {
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if amount == 0 {
		return false, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, "UPDATE affiliate_commission_entries SET available_cents=available_cents+pending_cents,pending_cents=0 WHERE id=$1", id); err != nil {
		return false, err
	}
	if err = affiliateEvent(ctx, tx, id, 0, 0, run, "released", amount, before, "hold period elapsed", fmt.Sprintf("affiliate:release:%d", id)); err != nil {
		return false, err
	}
	if err = offsetAffiliateClawbacks(ctx, tx, account, id, run); err != nil {
		return false, err
	}
	if err = affiliateAudit(ctx, tx, account, 0, "affiliate.commission.released", "affiliate_commission", id, map[string]any{"amount_cents": amount, "run_id": run}); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "UPDATE affiliate_settlement_runs SET processed_count=processed_count+1 WHERE id=$1 AND lease_owner=$2", run, owner); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
func offsetAffiliateClawbacks(ctx context.Context, tx pgx.Tx, account, entry, run int64) error {
	var available int64
	if err := tx.QueryRow(ctx, "SELECT available_cents FROM affiliate_commission_entries WHERE id=$1", entry).Scan(&available); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT c.id,c.outstanding_cents FROM affiliate_clawbacks c JOIN affiliate_commission_entries e ON e.id=c.entry_id JOIN affiliate_withdrawal_allocations a ON a.entry_id=c.entry_id AND a.withdrawal_id=c.withdrawal_id WHERE e.account_id=$1 AND a.status='paid' AND c.outstanding_cents>0 ORDER BY c.id FOR UPDATE OF c", account)
	if err != nil {
		return err
	}
	type debt struct{ id, amount int64 }
	var debts []debt
	for rows.Next() {
		var d debt
		if err = rows.Scan(&d.id, &d.amount); err != nil {
			rows.Close()
			return err
		}
		debts = append(debts, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, d := range debts {
		n := min(available, d.amount)
		if n == 0 {
			break
		}
		if _, err = tx.Exec(ctx, "UPDATE affiliate_clawbacks SET outstanding_cents=outstanding_cents-$2 WHERE id=$1", d.id, n); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE affiliate_commission_entries SET available_cents=available_cents-$2,reversed_cents=reversed_cents+$2 WHERE id=$1", entry, n); err != nil {
			return err
		}
		if err = affiliateEvent(ctx, tx, entry, 0, 0, run, "adjusted", -n, "available", "clawback offset", fmt.Sprintf("affiliate:offset:%d:%d", entry, d.id)); err != nil {
			return err
		}
		if err = affiliateAudit(ctx, tx, account, 0, "affiliate.clawback.offset", "affiliate_commission", entry, map[string]any{"clawback_id": d.id, "amount_cents": n, "run_id": run}); err != nil {
			return err
		}
		available -= n
	}
	return nil
}
