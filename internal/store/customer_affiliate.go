package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// AdminCustomerAffiliate returns the affiliate activity that belongs to a
// customer, including both their promoter account and referral activity.
func (s *Store) AdminCustomerAffiliate(ctx context.Context, actorID, customerID int64, limit, offset int) ([]map[string]any, error) {
	var allowed bool
	if err := s.Pool.QueryRow(ctx, "SELECT admin_customer_allowed($1,$2)", actorID, customerID).Scan(&allowed); err != nil {
		return nil, err
	}
	if !allowed {
		return nil, conflict("not_found", "客户不存在")
	}
	rows, err := s.Pool.Query(ctx, `
SELECT kind,id,invoice_no,label,amount_cents,status,created_at FROM (
 SELECT 'affiliate_account'::text kind,a.id,''::text invoice_no,'推广账户'::text label,
   (SELECT coalesce(sum(c.amount_cents),0) FROM affiliate_commission_entries c WHERE c.account_id=a.id)::bigint amount_cents,
   a.status,a.created_at
 FROM affiliate_accounts a WHERE a.user_id=$1
 UNION ALL
 SELECT 'affiliate_attribution'::text,at.id,''::text,'推荐归因'::text,0::bigint,at.status,coalesce(at.registered_at,at.first_clicked_at)
 FROM affiliate_attributions at WHERE at.user_id=$1 OR at.account_id IN (SELECT id FROM affiliate_accounts WHERE user_id=$1)
 UNION ALL
 SELECT 'affiliate_commission'::text,c.id,i.invoice_no,c.event_type,c.amount_cents,c.status,c.created_at
 FROM affiliate_commission_entries c JOIN affiliate_accounts a ON a.id=c.account_id JOIN invoices i ON i.id=c.invoice_id
 WHERE a.user_id=$1
 UNION ALL
 SELECT 'affiliate_withdrawal'::text,w.id,w.withdrawal_no,w.payout_method,w.payout_cents,w.status,w.created_at
 FROM affiliate_withdrawals w JOIN affiliate_accounts a ON a.id=w.account_id WHERE a.user_id=$1
) activity ORDER BY created_at DESC NULLS LAST,id DESC LIMIT $2 OFFSET $3`, customerID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if items == nil {
		items = []map[string]any{}
	}
	return items, err
}
