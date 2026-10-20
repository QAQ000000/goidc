package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/codex/zjmfgo/internal/scheduler"
	"github.com/jackc/pgx/v5"
)

func (s *Store) SetAutoRenew(ctx context.Context, actorID, serviceID int64, enabled bool, admin bool) error {
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		var ownerID int64
		var status, cycle string
		var scopedOwnerID int64
		var scopeErr error
		if admin {
			if err = tx.QueryRow(ctx, "SELECT user_id FROM services WHERE id=$1", serviceID).Scan(&scopedOwnerID); err != nil {
				return err
			}
			// Match other service mutations: customer assignment before service.
			// Credential/role locks are acquired after the service wait so a
			// revocation committed during that wait is still observed.
			scopeErr = LockAdminCustomer(ctx, tx, actorID, scopedOwnerID)
			if scopeErr != nil && !errors.Is(scopeErr, pgx.ErrNoRows) {
				return scopeErr
			}
		}
		if err = tx.QueryRow(ctx, "SELECT user_id,status,billing_cycle FROM services WHERE id=$1 FOR UPDATE", serviceID).Scan(&ownerID, &status, &cycle); err != nil {
			return err
		}
		if !admin && ownerID != actorID {
			return pgx.ErrNoRows
		}
		if admin {
			// The service lock and customer-assignment lock can both wait. The
			// middleware's role/scope snapshot cannot authorize a later write.
			if err = RequireAdministratorPermission(ctx, tx, actorID, "services.manage"); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return conflict("unauthorized", "登录已失效，请重新登录")
				}
				return err
			}
			// Account mutations (password/staff/API-key management) lock the
			// administrator before its credentials. Keep that same order here.
			if err = lockStorageCredential(ctx, tx, actorID); err != nil {
				return err
			}
			// Holding the current administrator and role now prevents another
			// scope or permission change until this mutation commits.
			if scopeErr != nil {
				return scopeErr
			}
			if ownerID != scopedOwnerID {
				return conflict("service_owner_changed", "服务客户已变更，请刷新后重试")
			}
			if err = LockAdminCustomer(ctx, tx, actorID, ownerID); err != nil {
				return err
			}
		}
		if err = requireNoLocalRenewalHold(ctx, tx, serviceID); err != nil {
			return err
		}
		if enabled {
			var supplierMode bool
			if err = tx.QueryRow(ctx, "SELECT provision_mode='supplier' FROM services WHERE id=$1", serviceID).Scan(&supplierMode); err != nil {
				return err
			}
			if supplierMode {
				if _, _, err = s.requireSupplierRenewal(ctx, tx, serviceID); err != nil {
					return err
				}
			}

			if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
				return err
			}
		}
		if cycle == "once" {
			return conflict("nonrenewable_service", "一次性服务不可自动续费")
		}
		if status != "active" && status != "suspended" {
			return conflict("invalid_auto_renew_state", "只有已启用或已暂停的服务可以设置自动续费")
		}
		if _, bound := ctx.Value(storageCredentialKey{}).(storageCredential); admin && bound {
			if err = ticketFileCredentialCurrent(ctx, tx, actorID); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE services SET auto_renew=$2,auto_renew_error='',updated_at=now() WHERE id=$1", serviceID, enabled); err != nil {
			return err
		}
		if err = Audit(ctx, tx, actorID, "service.auto_renew.updated", "service", serviceID, map[string]any{"enabled": enabled}); err != nil {
			return err
		}
		return nil
	})
}

// autoRenewPendingInvoice is the operator-facing reason for a service that is
// deliberately skipped because an unpaid renewal invoice already exists.
const autoRenewPendingInvoice = "已有待支付续费账单"

// autoRenewErrorText renders auto_renew_error for a failed candidate. A pending
// renewal invoice is a skip, not a failure, so it gets an explicit label.
func autoRenewErrorText(err error) string {
	var c *Conflict
	if errors.As(err, &c) && c.Code == "renewal_invoice_pending" {
		return autoRenewPendingInvoice
	}
	return err.Error()
}

// autoRenewSkipReason explains why a candidate that got an invoice did not end
// up as a paid renewal. The durable invoice status is the authority; the
// Replayed flag alone never means success, because replay hit on an existing
// invoice may well still be unpaid (the legacy system skipped exactly that
// case instead of reporting a generated invoice).
func autoRenewSkipReason(payErr error, status string, replayed bool) string {
	if status == "unpaid" {
		if replayed || payErr == nil {
			return autoRenewPendingInvoice
		}
		return payErr.Error()
	}
	return fmt.Sprintf("续费账单状态为 %s，未完成自动续费", status)
}

// RunDailyAutoRenewals runs one complete pass per database calendar day. The
// transaction advisory lock serializes workers and is released on connection
// loss, allowing an interrupted pass to be scanned again. Committed unpaid
// invoices remain for manual payment; interrupted or failed collection is not
// retried automatically.
func (s *Store) RunDailyAutoRenewals(ctx context.Context, log *slog.Logger) (int, error) {
	return withTx(ctx, s, func(tx pgx.Tx) (int, error) {
		var err error
		var claimed, completed bool
		if err = tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended('automatic-renewals-daily',0))").Scan(&claimed); err != nil || !claimed {
			return 0, err
		}
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM scheduler_runs WHERE task_key=$1 AND status='succeeded' AND is_full_pass AND started_at>=date_trunc('day',now()))", string(scheduler.AutomaticRenewals)).Scan(&completed); err != nil || completed {
			return 0, err
		}
		count, err := s.runScheduled(ctx, log, string(scheduler.AutomaticRenewals), true, func(c context.Context) (int, error) {
			return s.ProcessAutoRenewals(c, 100)
		})
		if err != nil {
			return count, err
		}
		return count, nil
	})
}

type autoRenewCandidate struct {
	id, userID int64
	generation int64
	due        time.Time
}

// ProcessAutoRenewals traverses all candidates like the legacy daily cron.
// pageSize bounds memory and query size, not the total number attempted. An ID
// cursor moves past failures even if no invoice could be created. A fixed upper
// ID and cutoff keep concurrent inserts and due-date updates from extending the
// pass indefinitely. Existing unpaid invoices are preserved and skipped.
func (s *Store) ProcessAutoRenewals(ctx context.Context, pageSize int) (int, error) {
	if pageSize < 1 || pageSize > 100 {
		pageSize = 100
	}
	var upperID int64
	var cutoff time.Time
	if err := s.Pool.QueryRow(ctx, "SELECT coalesce(max(id),0),now()+interval '24 hours' FROM services").Scan(&upperID, &cutoff); err != nil {
		return 0, err
	}
	var cursor int64
	processed := 0
	var persistenceErr error
	for cursor < upperID {
		rows, err := s.Pool.Query(ctx, `SELECT id,user_id,next_due_at,renewal_generation FROM services
WHERE auto_renew AND NOT import_review_required AND status IN ('active','suspended') AND billing_cycle <> 'once'
  AND NOT local_renewal_service_blocked(id)
  AND next_due_at IS NOT NULL AND next_due_at <= $2 AND id>$3 AND id<=$4
  AND NOT EXISTS (SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id
                  WHERE ii.service_id=services.id AND ii.item_type='renew' AND i.status='unpaid')
ORDER BY id LIMIT $1`, pageSize, cutoff, cursor, upperID)
		if err != nil {
			return processed, errors.Join(persistenceErr, err)
		}
		items := []autoRenewCandidate{}
		for rows.Next() {
			var item autoRenewCandidate
			if err = rows.Scan(&item.id, &item.userID, &item.due, &item.generation); err != nil {
				rows.Close()
				return processed, errors.Join(persistenceErr, err)
			}
			items = append(items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return processed, errors.Join(persistenceErr, err)
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return processed, errors.Join(persistenceErr, err)
			}
			WorkerRuntimeProgress(ctx)
			cursor = item.id
			attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
			paid, message := s.attemptAutoRenewal(attempt, item)
			cancel()
			if paid {
				processed++
			}
			if _, err = s.Pool.Exec(ctx, "UPDATE services SET auto_renew_error=$2,updated_at=now() WHERE id=$1 AND next_due_at=$3 AND renewal_generation=$4 AND auto_renew AND NOT local_renewal_service_blocked(id)", item.id, message, item.due, item.generation); err != nil {
				persistenceErr = errors.Join(persistenceErr, err)
			}
		}
	}
	return processed, persistenceErr
}

func (s *Store) attemptAutoRenewal(ctx context.Context, item autoRenewCandidate) (bool, string) {
	key := fmt.Sprintf("auto-renew-%d-%s", item.id, item.due.UTC().Format("20060102150405"))
	if item.generation > 0 {
		key += fmt.Sprintf("-g%d", item.generation)
	}
	invoice, err := s.CreateAutoRenewalInvoice(ctx, item.userID, item.id, item.due, key, item.generation)
	if err != nil {
		return false, autoRenewErrorText(err)
	}
	_, payErr := s.PayAutoRenewalInvoice(ctx, item.userID, item.id, invoice.InvoiceID, item.due, item.generation, "auto-pay-"+key)
	var status string
	if err = s.Pool.QueryRow(ctx, "SELECT status FROM invoices WHERE id=$1", invoice.InvoiceID).Scan(&status); err != nil {
		return false, err.Error()
	}
	if status == "paid" {
		return true, ""
	}
	return false, autoRenewSkipReason(payErr, status, invoice.Replayed)
}
