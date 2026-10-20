package store

import (
	"context"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
)

type CustomerAffiliatePage struct {
	Items  []map[string]any `json:"items"`
	Total  int64            `json:"total"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

// AdminCustomerAffiliate preserves the legacy activity array response.
func (s *Store) AdminCustomerAffiliate(ctx context.Context, actorID, customerID int64, limit, offset int) ([]map[string]any, error) {
	page, err := s.AdminCustomerAffiliatePage(ctx, actorID, customerID, limit, offset)
	return page.Items, err
}

// AdminCustomerAffiliatePage returns activity belonging to the selected customer,
// including both promoter and referral activity. A promoter's financial activity
// is their own fact; related invoice identifiers remain subject to invoice scope.
func (s *Store) AdminCustomerAffiliatePage(ctx context.Context, actorID, customerID int64, limit, offset int) (CustomerAffiliatePage, error) {
	if limit < 1 || limit > 100 {
		limit = 25
	}
	if offset < 0 {
		offset = 0
	}
	page := CustomerAffiliatePage{Items: []map[string]any{}, Limit: limit, Offset: offset}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	if err = adminAffiliateReadAuthority(ctx, tx, actorID, "customers.view"); err != nil {
		return page, err
	}
	filters := url.Values{"customer_id": {strconv.FormatInt(customerID, 10)}}
	if _, err = s.AdminCustomerFilterTx(ctx, tx, actorID, filters); err != nil {
		return page, err
	}
	query := `WITH target AS (
 SELECT id FROM users WHERE id=$2 AND role='customer' AND admin_customer_allowed($1,id)
), activity AS (
 SELECT 'affiliate_account'::text kind,a.id,''::text invoice_no,'推广账户'::text label,
   (SELECT coalesce(sum(c.amount_cents),0) FROM affiliate_commission_entries c WHERE c.account_id=a.id)::bigint amount_cents,
   a.status,a.created_at
 FROM affiliate_accounts a JOIN target ON target.id=a.user_id
 UNION ALL
 SELECT 'affiliate_attribution'::text,at.id,''::text,'推荐归因'::text,0::bigint,at.status,coalesce(at.registered_at,at.first_clicked_at)
 FROM affiliate_attributions at JOIN affiliate_accounts a ON a.id=at.account_id
 JOIN target ON at.user_id=target.id OR a.user_id=target.id
 UNION ALL
 SELECT 'affiliate_commission'::text,c.id,CASE WHEN admin_customer_allowed($1,i.user_id) THEN i.invoice_no ELSE '' END,c.event_type,c.amount_cents,c.status,c.created_at
 FROM affiliate_commission_entries c JOIN affiliate_accounts a ON a.id=c.account_id JOIN invoices i ON i.id=c.invoice_id
 JOIN target ON target.id=a.user_id
 UNION ALL
 SELECT 'affiliate_withdrawal'::text,w.id,w.withdrawal_no,w.payout_method,w.payout_cents,w.status,w.created_at
 FROM affiliate_withdrawals w JOIN affiliate_accounts a ON a.id=w.account_id JOIN target ON target.id=a.user_id
)
`
	if err = tx.QueryRow(ctx, query+"SELECT count(*) FROM activity", actorID, customerID).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := tx.Query(ctx, query+"SELECT kind,id,invoice_no,label,amount_cents,status,created_at FROM activity ORDER BY created_at DESC NULLS LAST,id DESC,kind LIMIT $3 OFFSET $4", actorID, customerID, limit, offset)
	if err != nil {
		return page, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		return page, err
	}
	if items != nil {
		page.Items = items
	}
	return page, tx.Commit(ctx)
}
