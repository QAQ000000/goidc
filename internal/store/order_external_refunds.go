package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

type ExternalRefundServiceVersion struct {
	ID      int64 `json:"id"`
	Version int64 `json:"version"`
}
type ExternalRefundOrderService struct {
	ID                int64  `json:"id"`
	Version           int64  `json:"version"`
	Status            string `json:"status"`
	ProductID         int64  `json:"product_id"`
	OrderItemID       int64  `json:"order_item_id"`
	UnitNumber        int    `json:"unit_number"`
	ProvisionMode     string `json:"provision_mode"`
	InventoryReleased bool   `json:"inventory_released"`
}
type ExternalRefundOrderReversal struct {
	Status             string     `json:"status"`
	OrderID            int64      `json:"order_id"`
	InvoiceVersion     int64      `json:"invoice_version"`
	OrderVersion       int64      `json:"order_version"`
	ServiceCount       int64      `json:"service_count"`
	StockUnitsReleased int64      `json:"stock_units_released"`
	CreatedAt          time.Time  `json:"created_at"`
	AppliedAt          *time.Time `json:"applied_at"`
	ReleasedAt         *time.Time `json:"released_at"`
}
type ExternalRefundOrderContext struct {
	InvoiceID      int64                        `json:"invoice_id"`
	InvoiceVersion int64                        `json:"invoice_version"`
	Eligible       bool                         `json:"eligible"`
	ReasonCode     string                       `json:"reason_code"`
	Reason         string                       `json:"reason"`
	OrderID        int64                        `json:"order_id"`
	OrderVersion   int64                        `json:"order_version"`
	Services       []ExternalRefundOrderService `json:"services"`
	OrderReversal  *ExternalRefundOrderReversal `json:"order_reversal"`
}

func orderRefundConflict(code string) error {
	messages := map[string]string{
		"external_refund_order_not_eligible": "仅支持未开始交付的纯外部全额新购订单",
		"external_refund_delivery_started":   "订单存在交付或变更历史，不能冲正",
		"external_refund_affiliate_linked":   "订单已有推广佣金或业绩关联，不能冲正",
		"external_refund_order_hold":         "订单已有外部退款交付冻结或已冲正",
	}
	message := messages[code]
	if message == "" {
		message = "订单退款状态不一致，需人工核对"
	}
	return conflict(code, message)
}

// Caller already owns invoice. All order mutation paths use invoice -> order;
// children are acquired in a fixed order. Writers holding an existing child
// never wait for its order, so delivery and snapshot races cannot invert locks.
func lockExternalRefundOrder(ctx context.Context, tx pgx.Tx, invoice int64) (int64, error) {
	var order int64
	if err := tx.QueryRow(ctx, `SELECT o.id FROM orders o JOIN invoices i ON i.order_id=o.id WHERE i.id=$1 FOR UPDATE OF o`, invoice).Scan(&order); err != nil {
		return 0, err
	}
	for _, q := range []string{`SELECT id FROM order_items WHERE order_id=$1 ORDER BY id FOR UPDATE`, `SELECT id FROM services WHERE order_id=$1 ORDER BY id FOR UPDATE`} {
		rows, err := tx.Query(ctx, q, order)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return 0, err
		}
	}
	return order, nil
}
func requireExternalRefundOrder(ctx context.Context, tx pgx.Tx, invoice int64) error {
	var reason string
	if err := tx.QueryRow(ctx, `SELECT external_refund_order_reason($1)`, invoice).Scan(&reason); err != nil {
		return err
	}
	if reason != "" {
		return orderRefundConflict(reason)
	}
	return nil
}

func (s *Store) ExternalRefundOrderContext(ctx context.Context, actor, invoice int64) (out ExternalRefundOrderContext, err error) {
	out.InvoiceID = invoice
	out.Services = []ExternalRefundOrderService{}
	defer func() { err = externalRefundDBError(err) }()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = LockAdminResourceCustomer(ctx, tx, actor, "invoice", invoice); err != nil {
		return out, err
	}
	if err = externalRefundAuthorize(ctx, tx, actor, "external_refunds.view", true); err != nil {
		return out, err
	}
	if err = LockAdminResourceCustomer(ctx, tx, actor, "invoice", invoice); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `SELECT version,coalesce(order_id,0) FROM invoices WHERE id=$1 FOR SHARE`, invoice).Scan(&out.InvoiceVersion, &out.OrderID); err != nil {
		return out, err
	}
	if out.OrderID > 0 {
		if err = tx.QueryRow(ctx, `SELECT version FROM orders WHERE id=$1 FOR SHARE`, out.OrderID).Scan(&out.OrderVersion); err != nil {
			return out, err
		}
		rows, e := tx.Query(ctx, `SELECT id,version,status,product_id,order_item_id,unit_number,provision_mode,inventory_released FROM services WHERE order_id=$1 ORDER BY id`, out.OrderID)
		if e != nil {
			return out, e
		}
		for rows.Next() {
			var v ExternalRefundOrderService
			if err = rows.Scan(&v.ID, &v.Version, &v.Status, &v.ProductID, &v.OrderItemID, &v.UnitNumber, &v.ProvisionMode, &v.InventoryReleased); err != nil {
				break
			}
			out.Services = append(out.Services, v)
		}
		rows.Close()
		if err != nil {
			return out, err
		}
		if err = rows.Err(); err != nil {
			return out, err
		}
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT external_refund_order_hold_json($1)`, out.OrderID).Scan(&raw); err != nil {
			return out, err
		}
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &out.OrderReversal); err != nil {
				return out, err
			}
		}
	}
	if err = tx.QueryRow(ctx, `SELECT external_refund_order_reason($1)`, invoice).Scan(&out.ReasonCode); err != nil {
		return out, err
	}
	if out.OrderReversal != nil && out.OrderReversal.Status != "released" {
		out.ReasonCode = "external_refund_order_hold"
	}
	out.Eligible = out.ReasonCode == ""
	if !out.Eligible {
		out.Reason = orderRefundConflict(out.ReasonCode).Error()
	}
	return out, tx.Commit(ctx)
}

func validateExternalRefundOrderVersions(ctx context.Context, tx pgx.Tx, invoice int64, in ExternalRefundInput) error {
	var orderVersion int64
	if err := tx.QueryRow(ctx, `SELECT o.version FROM orders o JOIN invoices i ON i.order_id=o.id WHERE i.id=$1`, invoice).Scan(&orderVersion); err != nil {
		return err
	}
	if in.ExpectedOrderVersion < 1 || in.ExpectedOrderVersion != orderVersion {
		return conflict("version_conflict", "订单版本已变化")
	}
	rows, err := tx.Query(ctx, `SELECT s.id,s.version FROM services s JOIN invoices i ON i.order_id=s.order_id WHERE i.id=$1 ORDER BY s.id`, invoice)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var id, version int64
		if err = rows.Scan(&id, &version); err != nil {
			return err
		}
		if index >= len(in.ExpectedServiceVersions) || in.ExpectedServiceVersions[index].ID != id || in.ExpectedServiceVersions[index].Version != version {
			return conflict("version_conflict", "服务版本或完整服务集合已变化")
		}
		index++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if index == 0 || index != len(in.ExpectedServiceVersions) {
		return conflict("version_conflict", "必须绑定完整服务版本集合")
	}
	return nil
}
func canonicalExternalRefundOrderVersions(in *ExternalRefundInput) error {
	in.ExpectedServiceVersions = slices.Clone(in.ExpectedServiceVersions)
	slices.SortFunc(in.ExpectedServiceVersions, func(a, b ExternalRefundServiceVersion) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for n, v := range in.ExpectedServiceVersions {
		if v.ID < 1 || v.Version < 1 || (n > 0 && in.ExpectedServiceVersions[n-1].ID == v.ID) {
			return conflict("version_conflict", "服务版本集合无效或重复")
		}
	}
	return nil
}
func reserveExternalRefundOrder(ctx context.Context, tx pgx.Tx, id, invoice int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO external_refund_order_holds(refund_id,invoice_id,payment_id,order_id,user_id,amount_cents,invoice_version,order_version,invoice_snapshot,order_snapshot,items_snapshot)
 SELECT r.id,i.id,r.payment_id,o.id,r.user_id,r.amount_cents,i.version,o.version,to_jsonb(i),to_jsonb(o),(SELECT jsonb_agg(to_jsonb(oi) ORDER BY oi.id) FROM order_items oi WHERE oi.order_id=o.id)
 FROM external_refunds r JOIN invoices i ON i.id=r.invoice_id JOIN orders o ON o.id=i.order_id WHERE r.id=$1 AND i.id=$2`, id, invoice)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO external_refund_order_services(refund_id,service_id,order_item_id,product_id,unit_number,version,snapshot,stock_at_request)
 SELECT $1,s.id,s.order_item_id,s.product_id,s.unit_number,s.version,to_jsonb(s),p.stock FROM services s JOIN products p ON p.id=s.product_id JOIN invoices i ON i.order_id=s.order_id WHERE i.id=$2 ORDER BY s.id`, id, invoice)
	return err
}
func applyExternalRefundOrder(ctx context.Context, tx pgx.Tx, actor, id, invoice int64) error {
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT h.status='pending' AND h.invoice_snapshot=to_jsonb(i) AND h.order_snapshot=to_jsonb(o)
 AND h.items_snapshot=(SELECT jsonb_agg(to_jsonb(oi) ORDER BY oi.id) FROM order_items oi WHERE oi.order_id=o.id)
 AND (SELECT count(*) FROM external_refund_order_services WHERE refund_id=h.refund_id)=(SELECT count(*) FROM services WHERE order_id=o.id)
 AND NOT EXISTS(SELECT 1 FROM external_refund_order_services x LEFT JOIN services s ON s.id=x.service_id WHERE x.refund_id=h.refund_id AND (s.id IS NULL OR x.snapshot<>to_jsonb(s)))
 FROM external_refund_order_holds h JOIN invoices i ON i.id=h.invoice_id JOIN orders o ON o.id=h.order_id WHERE h.refund_id=$1 AND i.id=$2 FOR UPDATE OF h`, id, invoice).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return conflict("version_conflict", "订单退款原始快照已变化")
	}
	if _, err := tx.Exec(ctx, `UPDATE external_refund_order_holds SET status='applying' WHERE refund_id=$1`, id); err != nil {
		return err
	}
	// Sorted immutable release inserts perform the actual stock update in DB.
	if _, err := tx.Exec(ctx, `INSERT INTO external_refund_order_stock_releases(refund_id,product_id,units)
 SELECT $1,product_id,count(*) FROM external_refund_order_services WHERE refund_id=$1 GROUP BY product_id ORDER BY product_id`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE services SET status='cancelled',inventory_released=true,version=version+1,updated_at=clock_timestamp() WHERE order_id=(SELECT order_id FROM external_refund_order_holds WHERE refund_id=$1)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET status='refunded',updated_at=clock_timestamp() WHERE id=(SELECT order_id FROM external_refund_order_holds WHERE refund_id=$1)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE invoices SET status='refunded' WHERE id=$1`, invoice); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE external_refund_order_holds SET status='applied',stock_units_released=(SELECT count(*) FROM external_refund_order_services WHERE refund_id=$1),applied_at=clock_timestamp() WHERE refund_id=$1`, id); err != nil {
		return err
	}
	return Audit(ctx, tx, actor, "external_refund.order_reversed", "external_refund", id, map[string]any{"invoice_id": invoice, "refund_id": id, "stock_release_reference": fmt.Sprint(id)})
}
