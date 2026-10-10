package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

type AffiliateAdminPage struct {
	Items        []map[string]any `json:"items"`
	Total        int64            `json:"total"`
	PendingCount int64            `json:"pending_count"`
}

func (s *Store) AdminAffiliatePage(ctx context.Context, actor int64, kind, search, status string, limit, offset int) (AffiliateAdminPage, error) {
	page := AffiliateAdminPage{Items: []map[string]any{}}
	var projection, from, text, state, order string
	switch kind {
	case "accounts":
		projection = "a.id,a.user_id,u.username,u.email,a.account_type,a.status,a.program_id,p.name,a.level,a.version,a.created_at,a.updated_at"
		from = "affiliate_accounts a JOIN users u ON u.id=a.user_id JOIN affiliate_programs p ON p.id=a.program_id"
		text, state, order = "concat_ws(' ',a.id::text,u.username,u.email,p.name)", "a.status", "a.id"
	case "attributions":
		projection = "at.id,at.user_id,u.username,at.account_id,a.user_id AS affiliate_user_id,au.username AS affiliate_username,at.link_id,at.first_clicked_at,at.registered_at,at.status"
		from = "affiliate_attributions at JOIN users u ON u.id=at.user_id JOIN affiliate_accounts a ON a.id=at.account_id JOIN users au ON au.id=a.user_id"
		text, state, order = "concat_ws(' ',at.id::text,u.username,au.username)", "at.status", "at.id"
	case "withdrawals":
		projection = affiliateWithdrawalProjection
		from = "affiliate_withdrawals w JOIN affiliate_accounts a ON a.id=w.account_id JOIN users u ON u.id=a.user_id"
		text, state, order = "concat_ws(' ',w.id::text,w.withdrawal_no,u.username,u.email)", "w.status", "w.id"
	default:
		return page, conflict("invalid_affiliate_list", "无效列表")
	}
	// Count and rows share a snapshot so concurrent writes cannot change the page total.
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	where := "admin_customer_owner_allowed($1,u.sales_owner_id) AND ($2='' OR strpos(lower(" + text + "),lower($2))>0) AND ($3='' OR " + state + "=$3)"
	args := []any{actor, strings.TrimSpace(search), strings.TrimSpace(status)}
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM "+from+" WHERE "+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	if kind == "withdrawals" {
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM "+from+" WHERE admin_customer_owner_allowed($1,u.sales_owner_id) AND w.status IN ('submitted','reviewing')", actor).Scan(&page.PendingCount); err != nil {
			return page, err
		}
	}
	rows, err := tx.Query(ctx, "SELECT "+projection+" FROM "+from+" WHERE "+where+" ORDER BY "+order+" DESC LIMIT $4 OFFSET $5", append(args, limit, offset)...)
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
