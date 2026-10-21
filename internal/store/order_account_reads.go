package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// A supplier order is proven by immutable service delivery snapshots, never
// by the product's current binding. The customer amount remains order-wide.
const supplierAccountOrderProjection = `,
 coalesce((SELECT jsonb_agg(jsonb_build_object('service_id',s.id,'supplier_id',s.delivery_snapshot->>'supplier_id','supplier_name',a.name,'cost_cents',j.result->'cost_cents','currency',j.result->>'currency') ORDER BY s.id)
 FROM services s LEFT JOIN supplier_accounts a ON a.id::text=s.delivery_snapshot->>'supplier_id'
 LEFT JOIN LATERAL (SELECT result FROM provision_jobs WHERE service_id=s.id AND action='create' AND status='succeeded' AND result->>'currency'='CNY' AND result ? 'cost_cents' ORDER BY id DESC LIMIT 1) j ON true
 WHERE s.order_id=o.id AND s.user_id=o.user_id AND s.provision_mode='supplier' AND s.delivery_snapshot->>'supplier_id' ~ '^[1-9][0-9]*$'),'[]'::jsonb) AS supplier_resources`

type OrderAccountListFilter struct {
	Actor                                    int64
	Admin, Supplier, Page                    bool
	CustomerID                               *int64
	SupplierID, Status, Query, PaymentStatus string
	DateFrom, DateTo                         *time.Time
	Limit, Offset                            int
}

func (s *Store) OrderAccountList(ctx context.Context, in OrderAccountListFilter) (any, error) {
	where := "o.deleted_at IS NULL AND (($1::boolean AND admin_customer_allowed($2,o.user_id)) OR (NOT $1::boolean AND o.user_id=$2))"
	args := []any{in.Admin, in.Actor}
	if in.Supplier {
		proof := "EXISTS(SELECT 1 FROM services ss WHERE ss.order_id=o.id AND ss.user_id=o.user_id AND ss.provision_mode='supplier' AND ss.delivery_snapshot->>'supplier_id' ~ '^[1-9][0-9]*$'"
		if in.SupplierID != "" {
			args = append(args, in.SupplierID)
			proof += fmt.Sprintf(" AND ss.delivery_snapshot->>'supplier_id'=$%d", len(args))
		}
		where += " AND " + proof + ")"
	}
	if in.Admin && in.CustomerID != nil {
		args = append(args, *in.CustomerID)
		where += fmt.Sprintf(" AND o.user_id=$%d", len(args))
	}
	if in.Status == "review_pending" {
		where += " AND o.review_status='pending' AND o.deleted_at IS NULL"
	} else if in.Status != "" {
		args = append(args, in.Status)
		where += fmt.Sprintf(" AND o.status=$%d", len(args))
	}
	if in.Query != "" {
		args = append(args, "%"+in.Query+"%")
		where += fmt.Sprintf(" AND (o.order_no ILIKE $%d OR EXISTS(SELECT 1 FROM order_items oi WHERE oi.order_id=o.id AND oi.product_name ILIKE $%d) OR ($1::boolean AND (u.display_name ILIKE $%d OR u.email ILIKE $%d)))", len(args), len(args), len(args), len(args))
	}
	if in.Admin {
		if in.DateFrom != nil {
			args = append(args, *in.DateFrom)
			where += fmt.Sprintf(" AND o.created_at >= $%d", len(args))
		}
		if in.DateTo != nil {
			args = append(args, *in.DateTo)
			where += fmt.Sprintf(" AND o.created_at < $%d", len(args))
		}
		if in.PaymentStatus == "deferred" {
			where += " AND i.id IS NULL"
		} else if in.PaymentStatus != "" {
			args = append(args, in.PaymentStatus)
			where += fmt.Sprintf(" AND i.status=$%d", len(args))
		}
	}
	projection := "o.version,o.id,o.order_no,o.user_id,u.display_name AS customer_name,o.status,o.review_status,o.total_cents,o.currency,o.created_at,i.id AS invoice_id,i.status AS invoice_status"
	if in.Admin {
		projection += `,external_refund_order_hold_json(o.id) AS external_refund_order_hold,COALESCE((SELECT cp.admin_note FROM customer_profiles cp WHERE cp.customer_id=o.user_id),'') AS customer_note,o.notes,o.invoice_deferred,o.payment_provider,o.payment_channel_instance_id,COALESCE((SELECT string_agg(DISTINCT nullif(s.resource_info->>'dedicated_ip',''),'、') FROM services s WHERE s.order_id=o.id AND admin_customer_allowed($2,s.user_id)),'') AS service_ips,COALESCE((SELECT string_agg(oi.product_name,'、' ORDER BY oi.id) FROM order_items oi WHERE oi.order_id=o.id),'') AS product_names,COALESCE((SELECT string_agg(s.domain,'、' ORDER BY s.id) FROM services s WHERE s.order_id=o.id AND admin_customer_allowed($2,s.user_id)),'') AS service_domains,COALESCE((SELECT display_name FROM administrators WHERE id=u.sales_owner_id),'') AS sales_owner_name,COALESCE((SELECT pt.provider FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status='succeeded' ORDER BY pt.id DESC LIMIT 1),CASE WHEN i.status='paid' AND EXISTS(SELECT 1 FROM balance_ledger WHERE reference_type='invoice' AND reference_id=i.id AND entry_type='payment') THEN 'balance' ELSE '' END) AS payment_method`
	}
	if in.Supplier {
		projection += supplierAccountOrderProjection
	}
	from := " FROM orders o JOIN users u ON u.id=o.user_id LEFT JOIN invoices i ON i.order_id=o.id AND i.user_id=o.user_id WHERE " + where
	query := "SELECT " + projection + from
	if in.Supplier || in.Page {
		tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)
		result, err := ticketAccountPage(ctx, tx, query, "id DESC", args, in.Limit, in.Offset)
		if err != nil {
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return result, nil
	}
	args = append(args, in.Limit, in.Offset)
	rows, err := s.Pool.Query(ctx, query+fmt.Sprintf(" ORDER BY o.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	return collectMaps(rows, err)
}

// RenewalAccountList reads renewal invoice lines; renewals do not create orders.
func (s *Store) RenewalAccountList(ctx context.Context, in OrderAccountListFilter) (map[string]any, error) {
	args := []any{true, in.Actor}
	where := "(($1::boolean AND admin_customer_allowed($2,i.user_id)) OR (NOT $1::boolean AND i.user_id=$2)) AND ii.item_type='renew'"
	if in.Supplier {
		proof := "EXISTS(SELECT 1 FROM services ss WHERE ss.id=ii.service_id AND ss.user_id=i.user_id AND ss.provision_mode='supplier' AND ss.delivery_snapshot->>'supplier_id' ~ '^[1-9][0-9]*$'"
		if in.SupplierID != "" {
			args = append(args, in.SupplierID)
			proof += fmt.Sprintf(" AND ss.delivery_snapshot->>'supplier_id'=$%d", len(args))
		}
		where += " AND " + proof + ")"
		if in.DateFrom != nil {
			args = append(args, *in.DateFrom)
			where += fmt.Sprintf(" AND i.created_at >= $%d", len(args))
		}
		if in.DateTo != nil {
			args = append(args, *in.DateTo)
			where += fmt.Sprintf(" AND i.created_at < $%d", len(args))
		}
	}
	if in.CustomerID != nil {
		args = append(args, *in.CustomerID)
		where += fmt.Sprintf(" AND i.user_id=$%d", len(args))
	}
	if in.Status != "" {
		args = append(args, in.Status)
		where += fmt.Sprintf(" AND CASE WHEN i.status='unpaid' THEN 'pending' WHEN i.status='cancelled' THEN 'cancelled' ELSE i.status END=$%d", len(args))
	}
	if in.Query != "" {
		args = append(args, "%"+in.Query+"%")
		where += fmt.Sprintf(" AND (i.invoice_no ILIKE $%d OR ii.description ILIKE $%d OR ($1::boolean AND (u.display_name ILIKE $%d OR u.email ILIKE $%d)))", len(args), len(args), len(args), len(args))
	}
	projection := "ii.id,i.invoice_no AS order_no,i.user_id,u.display_name AS customer_name,CASE WHEN i.status='unpaid' THEN 'pending' WHEN i.status='cancelled' THEN 'cancelled' ELSE i.status END AS status,'approved'::text AS review_status,ii.amount_cents AS total_cents,'CNY'::text AS currency,i.created_at,i.id AS invoice_id,i.status AS invoice_status,i.due_at,ii.description AS product_name,ii.service_id,s.domain AS service_domain,COALESCE((SELECT pt.provider FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status='succeeded' ORDER BY pt.id DESC LIMIT 1),CASE WHEN i.status='paid' AND EXISTS(SELECT 1 FROM balance_ledger WHERE reference_type='invoice' AND reference_id=i.id AND entry_type='payment') THEN 'balance' ELSE '' END) AS payment_method"
	if in.Supplier {
		projection += ",s.resource_info->>'dedicated_ip' AS service_ips,s.delivery_snapshot->>'supplier_id' AS supplier_id,(SELECT name FROM supplier_accounts a WHERE a.id::text=s.delivery_snapshot->>'supplier_id') AS supplier_name,(SELECT (j.result->>'cost_cents')::bigint FROM provision_jobs j WHERE j.idempotency_key='supplier-renew-'||i.id::text AND j.service_id=s.id AND j.status='succeeded' AND j.result->>'currency'='CNY') AS cost_cents,(SELECT j.status FROM provision_jobs j WHERE j.idempotency_key='supplier-renew-'||i.id::text AND j.service_id=s.id) AS upstream_renewal_status,'local_renewal_invoice'::text AS record_kind"
	}

	query := "SELECT " + projection + " FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id JOIN users u ON u.id=i.user_id LEFT JOIN services s ON s.id=ii.service_id AND s.user_id=i.user_id WHERE " + where
	return customerAccountReadPage(ctx, s.Pool, query, "id DESC", args, in.Limit, in.Offset)
}
