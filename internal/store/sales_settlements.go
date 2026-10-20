package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"math"
	"time"
)

type SalesSettlementInput struct {
	Month         string  `json:"month"`
	OrderIDs      []int64 `json:"order_ids"`
	PreviewDigest string  `json:"preview_digest"`
}
type SalesSettlementItem struct {
	TierThresholdCents   int64     `json:"tier_threshold_cents"`
	EffectiveRateBPS     int       `json:"effective_rate_bps"`
	RuleGroupID          int64     `json:"rule_group_id"`
	RuleGroupName        string    `json:"rule_group_name"`
	TierID               int64     `json:"tier_id"`
	OrderID              int64     `json:"order_id"`
	InvoiceID            int64     `json:"invoice_id"`
	CustomerID           int64     `json:"customer_id"`
	SalesOwnerID         int64     `json:"sales_owner_id"`
	SalesOwnerName       string    `json:"sales_owner_name"`
	BasisCents           int64     `json:"basis_cents"`
	PurchaseRateBPS      int       `json:"purchase_rate_bps"`
	TierExtraRateBPS     int       `json:"tier_extra_rate_bps"`
	MonthlyTurnoverCents int64     `json:"monthly_turnover_cents"`
	CommissionCents      int64     `json:"commission_cents"`
	CashReceivedAt       time.Time `json:"cash_received_at"`
	RuleVersion          int64     `json:"rule_version"`
	StaffVersion         int64     `json:"staff_version"`
	OwnerVersion         int64     `json:"owner_version"`
	TierVersion          int64     `json:"tier_version"`
}

func salesSettlementAuth(ctx context.Context, tx pgx.Tx, actor int64, sessionHash, permission string) error {
	var e error
	if sessionHash == "" {
		return conflict("permission_denied", "会话失效")
	}
	var ok bool
	var sessionUser int64
	e = tx.QueryRow(ctx, `SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>now() FOR SHARE`, sessionHash, actor).Scan(&sessionUser)
	if errors.Is(e, pgx.ErrNoRows) {
		return conflict("permission_denied", "会话失效")
	}
	if e != nil {
		return e
	}
	var role string
	var rid *int64
	e = tx.QueryRow(ctx, `SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE`, actor).Scan(&role, &rid)
	if errors.Is(e, pgx.ErrNoRows) {
		return conflict("permission_denied", "账号已失效")
	}
	if e != nil {
		return e
	}
	if role == "super_admin" {
		return nil
	}
	if role != "admin" || rid == nil {
		return conflict("permission_denied", "没有销售权限")
	}
	e = tx.QueryRow(ctx, `SELECT active AND $2=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE`, *rid, permission).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return conflict("permission_denied", "没有销售权限")
	}
	return nil
}
func salesSettlementPreviewTx(ctx context.Context, tx pgx.Tx, actor int64, in SalesSettlementInput, start, end time.Time) ([]SalesSettlementItem, error) {
	rows, e := tx.Query(ctx, `WITH eligible AS MATERIALIZED (
 SELECT o.id order_id,i.id invoice_id,o.user_id customer_id,c.sales_owner_id,c.sales_version owner_version,u.display_name,u.admin_version staff_version,g.id rule_group_id,g.name rule_group_name,g.version rule_version,g.purchase_rate_bps,LEAST(o.total_cents,i.amount_cents)::bigint basis,
 COALESCE((SELECT max(received_at) FROM credit_repayments cr WHERE cr.invoice_id=i.id),i.paid_at) cash_at
 FROM orders o JOIN invoices i ON i.order_id=o.id AND i.user_id=o.user_id JOIN users c ON c.id=o.user_id JOIN administrators u ON u.id=c.sales_owner_id JOIN sales_commission_groups g ON g.id=u.sales_commission_group_id
 WHERE o.deleted_at IS NULL AND o.status IN ('paid','active')  AND i.status='paid' AND i.invoice_kind='automatic' AND u.sales_enabled AND u.status='active' AND g.active
 AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments cp WHERE cp.invoice_id=i.id AND (cp.status<>'repaid' OR cp.remaining_cents<>0 OR cp.amount_cents<>(SELECT COALESCE(sum(cr.amount_cents),0) FROM credit_repayments cr WHERE cr.invoice_id=cp.invoice_id)))
 AND EXISTS(SELECT 1 FROM order_items oi WHERE oi.order_id=o.id) AND NOT EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.item_type<>'service')
), turnover AS (SELECT sales_owner_id,sum(basis)::bigint amount FROM eligible WHERE cash_at >=$2 AND cash_at<$3 GROUP BY sales_owner_id)
 SELECT e.order_id,e.invoice_id,e.customer_id,e.sales_owner_id,e.display_name,e.basis,e.purchase_rate_bps,COALESCE(t.extra_rate_bps,0),v.amount,
 round(e.basis::numeric*LEAST(10000,e.purchase_rate_bps+COALESCE(t.extra_rate_bps,0))/10000)::bigint,e.cash_at,e.rule_version,e.staff_version,e.owner_version,COALESCE(t.version,0),e.rule_group_id,e.rule_group_name,COALESCE(t.id,0),COALESCE(t.threshold_cents,0),LEAST(10000,e.purchase_rate_bps+COALESCE(t.extra_rate_bps,0))
 FROM eligible e JOIN turnover v ON v.sales_owner_id=e.sales_owner_id LEFT JOIN LATERAL(SELECT id,extra_rate_bps,version,threshold_cents FROM sales_commission_tiers WHERE active AND threshold_cents<=v.amount ORDER BY threshold_cents DESC,id DESC LIMIT 1)t ON true
 WHERE e.order_id=ANY($1::bigint[]) AND e.cash_at >=$2 AND e.cash_at<$3 AND admin_customer_allowed($4,e.customer_id)
 AND NOT EXISTS(SELECT 1 FROM sales_settlement_items si WHERE si.invoice_id=e.invoice_id) ORDER BY e.order_id`, in.OrderIDs, start, end, actor)
	if e != nil {
		return nil, e
	}
	items, e := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SalesSettlementItem, error) {
		var i SalesSettlementItem
		e := row.Scan(&i.OrderID, &i.InvoiceID, &i.CustomerID, &i.SalesOwnerID, &i.SalesOwnerName, &i.BasisCents, &i.PurchaseRateBPS, &i.TierExtraRateBPS, &i.MonthlyTurnoverCents, &i.CommissionCents, &i.CashReceivedAt, &i.RuleVersion, &i.StaffVersion, &i.OwnerVersion, &i.TierVersion, &i.RuleGroupID, &i.RuleGroupName, &i.TierID, &i.TierThresholdCents, &i.EffectiveRateBPS)
		return i, e
	})
	if e != nil {
		return nil, e
	}
	if len(items) != len(in.OrderIDs) {
		return nil, conflict("ineligible_settlement", "订单不在范围内、尚未现金收清、非首购或已经结算，请重新选择")
	}
	return items, nil
}

func (s *Store) WriteSalesSettlement(ctx context.Context, actor int64, sessionHash, key string, in SalesSettlementInput, start, end time.Time, create bool) (map[string]any, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)

	permission := "sales.view"
	if create {
		permission = "sales.manage"
	}

	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(70108190)`); e != nil {
		return nil, e
	}
	if e = salesSettlementAuth(ctx, tx, actor, sessionHash, permission); e != nil {
		return nil, e
	}
	if create {
		if e = salesSettlementAuth(ctx, tx, actor, sessionHash, "sales.view"); e != nil {
			return nil, e
		}
	}
	var fullScope bool
	if e = tx.QueryRow(ctx, `SELECT role='super_admin' OR customer_scope='all' FROM administrators WHERE id=$1`, actor).Scan(&fullScope); e != nil {
		return nil, e
	}
	if !fullScope {
		return nil, &AccountBusinessError{403, "full_customer_scope_required", "正式销售结算需要全部客户范围权限"}
	}

	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(13520261013)`); e != nil {
		return nil, e
	}
	hash := accountBusinessHash(in)
	if create {
		if replay, yes, err := loadAccountBusinessReplay(ctx, tx, actor, "sales.settlement", key, hash); err != nil {
			return nil, err
		} else if yes {
			var allowed bool
			if e = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM sales_settlement_items WHERE settlement_id=$1 AND NOT admin_customer_allowed($2,customer_id))`, replay["id"], actor).Scan(&allowed); e != nil {
				return nil, e
			}
			if !allowed {
				return nil, &AccountBusinessError{404, "not_found", "结算不存在或无权查看"}
			}

			return replay, tx.Commit(ctx)
		}
	}

	sourceRows, e := tx.Query(ctx, `SELECT DISTINCT user_id FROM orders WHERE id=ANY($1::bigint[]) ORDER BY user_id`, in.OrderIDs)
	if e != nil {
		return nil, e
	}
	customers, e := pgx.CollectRows(sourceRows, pgx.RowTo[int64])
	if e != nil {
		return nil, e
	}
	for _, customer := range customers {
		if e = LockAdminCustomer(ctx, tx, actor, customer); e != nil {
			return nil, e
		}
		if e = LockFinancialCustomer(ctx, tx, customer); e != nil {
			return nil, e
		}
	}
	sourceRows, e = tx.Query(ctx, `SELECT id FROM invoices WHERE order_id=ANY($1::bigint[]) ORDER BY id FOR UPDATE`, in.OrderIDs)
	if e != nil {
		return nil, e
	}
	_, e = pgx.CollectRows(sourceRows, pgx.RowTo[int64])
	if e != nil {
		return nil, e
	}
	sourceRows, e = tx.Query(ctx, `SELECT id FROM orders WHERE id=ANY($1::bigint[]) ORDER BY id FOR UPDATE`, in.OrderIDs)
	if e != nil {
		return nil, e
	}
	_, e = pgx.CollectRows(sourceRows, pgx.RowTo[int64])
	if e != nil {
		return nil, e
	}
	// Rules and global turnover are read by one SQL snapshot. A later refund of a
	// different source does not retroactively recalculate immutable tier snapshots.
	items, e := salesSettlementPreviewTx(ctx, tx, actor, in, start, end)
	if e != nil {
		return nil, e
	}
	digest := accountBusinessHash(struct {
		Month string
		Items []SalesSettlementItem
	}{in.Month, items})
	var total int64
	for _, i := range items {
		if total > math.MaxInt64-i.CommissionCents {
			return nil, &AccountBusinessError{400, "settlement_too_large", "结算总额过大，请减少订单"}
		}
		total += i.CommissionCents
	}
	result := map[string]any{"month": in.Month, "items": items, "total_commission_cents": total, "commission_cents": total, "reversed_cents": int64(0), "net_commission_cents": total, "preview_digest": digest, "attribution_basis": "current_at_confirmation", "scope": "purchase_only", "turnover_basis": "all_eligible_purchase_cash_receipts"}
	if create {
		if digest != in.PreviewDigest {
			return nil, &AccountBusinessError{409, "preview_changed", "预览已变更，请重新预览并确认"}
		}
		var id int64
		e = tx.QueryRow(ctx, `INSERT INTO sales_settlements(month,actor_id) VALUES($1,$2) RETURNING id`, in.Month, actor).Scan(&id)
		if e != nil {
			return nil, e
		}
		for _, i := range items {
			raw, _ := json.Marshal(i)
			_, e = tx.Exec(ctx, `INSERT INTO sales_settlement_items(settlement_id,order_id,invoice_id,customer_id,sales_owner_id,snapshot,commission_cents) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, i.OrderID, i.InvoiceID, i.CustomerID, i.SalesOwnerID, raw, i.CommissionCents)
			if e != nil {
				return nil, e
			}
		}
		result["id"] = id
		result["status"] = "confirmed"
		if e = Audit(ctx, tx, actor, "sales.settlement_confirmed", "sales_settlement", id, result); e != nil {
			return nil, e
		}
		if e = SaveCreationReceipt(ctx, tx, actor, "sales.settlement", key, hash, result); e != nil {
			return nil, e
		}
	}

	return result, tx.Commit(ctx)
}

func (s *Store) SalesSettlements(ctx context.Context, actor, id int64, sessionHash string, limit, offset int) (map[string]any, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(70108190)`); err != nil {
		return nil, err
	}
	if err = salesSettlementAuth(ctx, tx, actor, sessionHash, "sales.view"); err != nil {
		return nil, err
	}
	filter := ` WHERE ($2=0 OR s.id=$2) AND NOT EXISTS(SELECT 1 FROM sales_settlement_items hidden WHERE hidden.settlement_id=s.id AND NOT admin_customer_allowed($1,hidden.customer_id))`
	var total int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM sales_settlements s`+filter, actor, id).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT s.id,s.month,s.created_at,COALESCE(jsonb_agg(si.snapshot || jsonb_build_object('status',si.status,'reversed_at',si.reversed_at) ORDER BY si.order_id),'[]'),sum(si.commission_cents)::bigint,COALESCE(sum(si.commission_cents) FILTER(WHERE si.status='reversed'),0)::bigint,count(*) FILTER(WHERE si.status='reversed'),count(*) FROM sales_settlements s JOIN sales_settlement_items si ON si.settlement_id=s.id`+filter+` GROUP BY s.id ORDER BY s.id DESC LIMIT $3 OFFSET $4`, actor, id, limit, offset)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (map[string]any, error) {
		var sid, gross, rev, reversedCount, itemCount int64
		var month string
		var at time.Time
		var raw json.RawMessage
		err := row.Scan(&sid, &month, &at, &raw, &gross, &rev, &reversedCount, &itemCount)
		status := "confirmed"
		if reversedCount > 0 {
			status = "partially_reversed"
			if reversedCount == itemCount {
				status = "reversed"
			}
		}
		return map[string]any{"id": sid, "month": month, "created_at": at, "items": raw, "status": status, "commission_cents": gross, "reversed_cents": rev, "net_commission_cents": gross - rev, "scope": "purchase_only", "turnover_basis": "all_eligible_purchase_cash_receipts", "attribution_basis": "current_at_confirmation"}, err
	})
	if err != nil {
		return nil, err
	}
	if id > 0 {
		if len(items) == 0 {
			return nil, &AccountBusinessError{404, "not_found", "结算不存在或无权查看"}
		}
		return items[0], tx.Commit(ctx)
	}
	if items == nil {
		items = []map[string]any{}
	}
	return map[string]any{"total": total, "items": items, "limit": limit, "offset": offset}, tx.Commit(ctx)
}
