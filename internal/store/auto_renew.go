package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Store) SetAutoRenew(ctx context.Context, actorID, serviceID int64, enabled bool, admin bool) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var ownerID int64
	var status, cycle string
	if err = tx.QueryRow(ctx, "SELECT user_id,status,billing_cycle FROM services WHERE id=$1 FOR UPDATE", serviceID).Scan(&ownerID, &status, &cycle); err != nil {
		return err
	}
	if !admin && ownerID != actorID {
		return pgx.ErrNoRows
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
	if _, err = tx.Exec(ctx, "UPDATE services SET auto_renew=$2,auto_renew_error='',updated_at=now() WHERE id=$1", serviceID, enabled); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actorID, "service.auto_renew.updated", "service", serviceID, map[string]any{"enabled": enabled}); err != nil {
		return err
	}
	return tx.Commit(ctx)
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

// recordAutoRenewError stores the operator-visible reason a renewal did not
// complete. The legacy system had no structured error column and only wrote
// free text into activity_log; this column already exists, so use it.
func (s *Store) recordAutoRenewError(ctx context.Context, serviceID int64, message string) {
	_, _ = s.Pool.Exec(ctx, "UPDATE services SET auto_renew_error=$2,updated_at=now() WHERE id=$1", serviceID, message)
}

// ProcessAutoRenewals creates and pays due renewal invoices. Each invoice and
// payment uses a key derived from the due timestamp, so retries are harmless.
//
// The candidate query mirrors the legacy cron (app/admin/command/Cron.php
// invoice() calling Invoices::createRenew): a service that already has an
// unpaid renewal invoice is skipped, neither re-invoiced nor counted. That
// keeps a permanently failing service from occupying the window and starving
// the services behind it. The worker keeps its LIMIT because it polls in a
// loop, unlike the legacy once-a-day unbounded query.
func (s *Store) ProcessAutoRenewals(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,user_id,next_due_at FROM services
WHERE auto_renew AND NOT import_review_required AND status IN ('active','suspended') AND billing_cycle <> 'once'
  AND next_due_at IS NOT NULL AND next_due_at <= now()+interval '24 hours'
  AND NOT EXISTS (SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id
                  WHERE ii.service_id=services.id AND ii.item_type='renew' AND i.status='unpaid')
ORDER BY next_due_at,id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type dueService struct {
		id, userID int64
		due        time.Time
	}
	items := []dueService{}
	for rows.Next() {
		var item dueService
		if err = rows.Scan(&item.id, &item.userID, &item.due); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	processed := 0
	for _, item := range items {
		key := fmt.Sprintf("auto-renew-%d-%s", item.id, item.due.UTC().Format("20060102150405"))
		invoice, createErr := s.CreateAutoRenewalInvoice(ctx, item.userID, item.id, item.due, key)
		if createErr != nil {
			// Skipping is intentional for a service that already has an unpaid
			// renewal invoice; label it so operators can tell a deliberate skip
			// apart from a real failure.
			s.recordAutoRenewError(ctx, item.id, autoRenewErrorText(createErr))
			continue
		}
		_, payErr := s.PayInvoice(ctx, item.userID, invoice.InvoiceID, "auto-pay-"+key)
		var status string
		if err = s.Pool.QueryRow(ctx, "SELECT status FROM invoices WHERE id=$1", invoice.InvoiceID).Scan(&status); err != nil {
			s.recordAutoRenewError(ctx, item.id, err.Error())
			continue
		}
		if status == "paid" {
			// A paid invoice is the only durable proof of success. Replay on an
			// existing invoice does not count unless that invoice is paid: the
			// legacy cron skipped an unpaid cron renewal invoice and never
			// treated it as generated.
			processed++
			_, _ = s.Pool.Exec(ctx, "UPDATE services SET auto_renew_error='',updated_at=now() WHERE id=$1", item.id)
			continue
		}
		s.recordAutoRenewError(ctx, item.id, autoRenewSkipReason(payErr, status, invoice.Replayed))
	}
	return processed, nil
}
