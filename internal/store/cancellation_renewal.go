package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The caller owns the service row. Cancellation approval uses the same row,
// so its decision cannot race a new renewal or an invoice payment admission.
func requireServiceCancellationOpen(ctx context.Context, tx pgx.Tx, serviceID int64) error {
	var approved bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM service_cancellation_requests WHERE service_id=$1 AND status='approved')`, serviceID).Scan(&approved); err != nil {
		return err
	}
	if approved {
		return conflict("service_cancellation_approved", "服务取消已获批准，不能继续续费或付款，请联系管理员")
	}
	return nil
}

// Invoice writers already own the invoice. Do not wait for the service while
// holding finance locks: other service operations can need those same locks.
func requireRenewalInvoiceCancellationOpen(ctx context.Context, tx pgx.Tx, invoiceID int64) error {
	rows, err := tx.Query(ctx, `SELECT s.id FROM services s WHERE EXISTS(
SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=$1 AND ii.service_id=s.id AND ii.item_type='renew')
ORDER BY s.id FOR UPDATE OF s NOWAIT`, invoiceID)
	if err != nil {
		return cancellationServiceLockError(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return cancellationServiceLockError(err)
	}
	for _, id := range ids {
		if err = requireServiceCancellationOpen(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

func cancellationServiceLockError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "55P03" {
		return conflict("service_action_pending", "服务正在处理其他操作，请刷新后重试")
	}
	return err
}

// Called under the service lock. Intent creation and settlement also take that
// lock; committed intent state therefore suffices without reversing invoice locks.
func requireCancellationPaymentsResolved(ctx context.Context, tx pgx.Tx, serviceID int64) error {
	var unresolved bool
	if err := tx.QueryRow(ctx, `SELECT cancellation_renewal_payment_unresolved($1)`, serviceID).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved {
		return conflict("invoice_payment_pending", "续费账单存在未核实的在线支付，请先确认支付结果再批准取消")
	}
	return nil
}
