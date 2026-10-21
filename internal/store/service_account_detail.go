package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ServiceAccountDetail keeps the service projection and its linked records in
// the Store, using one connection for all reads.
func (s *Store) ServiceAccountDetail(ctx context.Context, actor, id int64, admin bool) (map[string]any, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if admin {
		if err = LockAdminResourceCustomer(ctx, tx, actor, "service", id); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT `+serviceAccountColumns+`,u.display_name AS customer_name`+serviceAccountGroupColumns+serviceAccountJoins+`
WHERE s.id=$1 AND (($2::boolean AND admin_customer_allowed($3,s.user_id)) OR (NOT $2::boolean AND s.user_id=$3))`, id, admin, actor)
	if err != nil {
		return nil, err
	}
	service, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if service["provision_mode"] == "supplier" {
		caps, capErr := s.ServiceSupplierCapabilitiesTx(ctx, tx, id, admin)
		if capErr != nil {
			return nil, capErr
		}
		service["supplier_capabilities"] = caps
	}
	if admin {
		metadata, queryErr := tx.Query(ctx, `SELECT p.name AS product_name,
 o.order_no,oi.price_cents AS first_price_cents,oi.setup_fee_cents,i.id AS invoice_id,i.invoice_no,i.status AS invoice_status,i.paid_at,
 a.name AS supplier_name,b.upstream_product_id,b.provider AS supplier_provider,
 a.running_job_id AS supplier_running_job_id,j.service_id AS supplier_blocking_service_id,j.action AS supplier_blocking_action
 FROM services s JOIN products p ON p.id=s.product_id JOIN orders o ON o.id=s.order_id
 JOIN order_items oi ON oi.id=s.order_item_id LEFT JOIN invoices i ON i.order_id=s.order_id
 LEFT JOIN supplier_product_bindings b ON b.product_id=s.product_id AND s.provision_mode='supplier'
 LEFT JOIN supplier_accounts a ON a.id=b.supplier_id LEFT JOIN provision_jobs j ON j.id=a.running_job_id WHERE s.id=$1`, id)
		if queryErr != nil {
			return nil, queryErr
		}
		details, queryErr := pgx.CollectExactlyOneRow(metadata, pgx.RowToMap)
		if queryErr != nil {
			return nil, queryErr
		}
		for key, value := range details {
			service[key] = value
		}
	}
	jobs, err := tx.Query(ctx, `SELECT id,action,status,attempts,available_at,locked_at,completed_at,last_error,
 result-'manual_lifecycle_verification' AS result,request_payload,created_at,
 (request_payload->>'manual_lifecycle'='true' AND action IN ('suspend','resume','terminate')) IS TRUE AS manual_lifecycle
FROM provision_jobs WHERE service_id=$1 ORDER BY id DESC LIMIT 50`, id)
	if err != nil {
		return nil, err
	}
	jobRows, err := pgx.CollectRows(jobs, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if jobRows == nil {
		jobRows = []map[string]any{}
	}
	service["jobs"] = jobRows
	cancellation, err := serviceAccountCancellation(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	service["cancellation"] = cancellation
	changes, err := tx.Query(ctx, `SELECT id,status,change_type,target_product_id,target_product_name,billing_cycle,new_billing_cycle,amount_cents,credit_cents,old_recurring_cents,new_recurring_cents,due_at,new_snapshot AS config_snapshot,invoice_id,job_id,created_at,completed_at FROM service_configuration_changes WHERE service_id=$1 AND invoice_id IS NOT NULL ORDER BY id DESC LIMIT 20`, id)
	if err != nil {
		return nil, err
	}
	changeRows, err := pgx.CollectRows(changes, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if changeRows == nil {
		changeRows = []map[string]any{}
	}
	service["configuration_changes"] = changeRows

	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return service, nil
}

func serviceAccountCancellation(ctx context.Context, tx pgx.Tx, service int64) (map[string]any, error) {
	rows, err := tx.Query(ctx, `SELECT id,service_id,user_id,type,reason,status,resolution_note,created_at,resolved_at,resolved_by
FROM service_cancellation_requests WHERE service_id=$1 ORDER BY id DESC LIMIT 1`, service)
	if err != nil {
		return nil, err
	}
	item, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return item, err
}
