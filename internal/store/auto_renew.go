package store

import (
	"context"
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
			return conflict("unsupported_supplier_renewal", "该上游适配器暂未开放自动续费")
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

// ProcessAutoRenewals creates and pays due renewal invoices. Each invoice and
// payment uses a key derived from the due timestamp, so retries are harmless.
func (s *Store) ProcessAutoRenewals(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,user_id,next_due_at FROM services
WHERE auto_renew AND NOT import_review_required AND status IN ('active','suspended') AND billing_cycle <> 'once'
  AND next_due_at IS NOT NULL AND next_due_at <= now()+interval '24 hours'
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
			continue
		}
		_, payErr := s.PayInvoice(ctx, item.userID, invoice.InvoiceID, "auto-pay-"+key)
		if payErr == nil || invoice.Replayed {
			processed++
			_, _ = s.Pool.Exec(ctx, "UPDATE services SET auto_renew_error='',updated_at=now() WHERE id=$1", item.id)
			continue
		}
		_, _ = s.Pool.Exec(ctx, "UPDATE services SET auto_renew_error=$2,updated_at=now() WHERE id=$1", item.id, payErr.Error())
	}
	return processed, nil
}
