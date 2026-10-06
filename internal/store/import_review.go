package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Call while holding the invoice row lock, before any financial mutation.
func requireInvoiceReviewed(ctx context.Context, tx pgx.Tx, invoiceID int64) error {
	var required bool
	if err := tx.QueryRow(ctx, "SELECT import_review_required FROM invoices WHERE id=$1", invoiceID).Scan(&required); err != nil {
		return err
	}
	if required {
		return conflict("import_review_required", "历史账单尚未完成迁移复核")
	}
	return nil
}

// Call while holding the service row lock, before billing or resource changes.
func requireServiceReviewed(ctx context.Context, tx pgx.Tx, serviceID int64) error {
	var required bool
	if err := tx.QueryRow(ctx, "SELECT import_review_required FROM services WHERE id=$1", serviceID).Scan(&required); err != nil {
		return err
	}
	if required {
		return conflict("import_review_required", "历史服务尚未完成迁移复核")
	}
	return nil
}
