package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Assignment takes the exclusive advisory lock; business mutations hold a shared
// lock without changing their existing finance/resource row-lock order.
func LockAdminCustomer(ctx context.Context, tx pgx.Tx, actorID, customerID int64) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(hashtextextended('customer-sales:'||$1::bigint::text,0))", customerID); err != nil {
		return err
	}
	var id int64
	return tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$2 AND role='customer' AND admin_customer_owner_allowed($1,sales_owner_id)", actorID, customerID).Scan(&id)
}

func LockAdminResourceCustomer(ctx context.Context, tx pgx.Tx, actorID int64, resource string, id int64) error {
	var query string
	switch resource {
	case "order":
		query = "SELECT user_id FROM orders WHERE id=$1"
	case "invoice":
		query = "SELECT user_id FROM invoices WHERE id=$1"
	case "service":
		query = "SELECT user_id FROM services WHERE id=$1"
	case "cancellation":
		query = "SELECT user_id FROM service_cancellation_requests WHERE id=$1"
	default:
		return conflict("invalid_resource", "客户资源类型无效")
	}
	var customerID int64
	if err := tx.QueryRow(ctx, query, id).Scan(&customerID); err != nil {
		return err
	}
	return LockAdminCustomer(ctx, tx, actorID, customerID)
}
