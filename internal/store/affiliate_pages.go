package store

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

type AffiliateAdminPage struct {
	Items        []map[string]any `json:"items"`
	Total        int64            `json:"total"`
	PendingCount int64            `json:"pending_count"`
}

// AdminAffiliatePage preserves the original search/status caller contract.
func (s *Store) AdminAffiliatePage(ctx context.Context, actor int64, kind, search, status string, limit, offset int) (AffiliateAdminPage, error) {
	return s.AdminAffiliatePageWithFilters(ctx, actor, kind, url.Values{"search": {search}, "status": {status}}, limit, offset)
}

func (s *Store) AdminAffiliatePageWithFilters(ctx context.Context, actor int64, kind string, filters url.Values, limit, offset int) (AffiliateAdminPage, error) {
	page := AffiliateAdminPage{Items: []map[string]any{}}
	var projection, from, text, state, order, exact string
	switch kind {
	case "accounts":
		projection = "a.id,a.user_id,u.username,u.email,a.account_type,a.status,a.program_id,p.name,a.level,a.version,a.created_at,a.updated_at"
		from = "affiliate_accounts a JOIN users u ON u.id=a.user_id JOIN affiliate_programs p ON p.id=a.program_id"
		text, state, order = "concat_ws(' ',a.id::text,u.username,u.email,p.name)", "a.status", "a.id"
		exact = "($4::bigint IS NULL OR a.user_id=$4)"
	case "attributions":
		// The referred customer is the row's primary scope. A visible referral
		// never grants access to its promoter or their searchable identity.
		visiblePromoter := "admin_customer_allowed($1,a.user_id)"
		projection = "at.id,at.user_id,u.username,CASE WHEN " + visiblePromoter + " THEN at.account_id END AS account_id," +
			"CASE WHEN " + visiblePromoter + " THEN a.user_id END AS affiliate_user_id," +
			"CASE WHEN " + visiblePromoter + " THEN au.username END AS affiliate_username," +
			"CASE WHEN " + visiblePromoter + " THEN at.link_id END AS link_id,at.first_clicked_at,at.registered_at,at.status"
		from = "affiliate_attributions at JOIN users u ON u.id=at.user_id JOIN affiliate_accounts a ON a.id=at.account_id JOIN users au ON au.id=a.user_id"
		text, state, order = "concat_ws(' ',at.id::text,u.username,CASE WHEN "+visiblePromoter+" THEN au.username END)", "at.status", "at.id"
		exact = "($4::bigint IS NULL OR ($5 IN ('referred','either') AND at.user_id=$4) OR ($5 IN ('promoter','either') AND a.user_id=$4))"
	case "withdrawals":
		projection = affiliateWithdrawalProjection
		from = "affiliate_withdrawals w JOIN affiliate_accounts a ON a.id=w.account_id JOIN users u ON u.id=a.user_id"
		text, state, order = "concat_ws(' ',w.id::text,w.withdrawal_no,u.username,u.email)", "w.status", "w.id"
		exact = "($4::bigint IS NULL OR a.user_id=$4)"
	default:
		return page, conflict("invalid_affiliate_list", "无效列表")
	}
	// Authority, exact customer validation, total and rows share one snapshot.
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	if err = adminAffiliateReadAuthority(ctx, tx, actor, "affiliates.view"); err != nil {
		return page, err
	}
	customer, err := s.AdminCustomerFilterTx(ctx, tx, actor, filters)
	if err != nil {
		return page, err
	}
	role, err := affiliateCustomerRole(kind, filters)
	if err != nil {
		return page, err
	}
	where := "u.role='customer' AND admin_customer_allowed($1,u.id) AND ($2='' OR strpos(lower(" + text + "),lower($2))>0) AND ($3='' OR " + state + "=$3) AND " + exact
	args := []any{actor, strings.TrimSpace(filters.Get("search")), strings.TrimSpace(filters.Get("status")), customer}
	if kind == "attributions" {
		args = append(args, role)
	}
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM "+from+" WHERE "+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	if kind == "withdrawals" {
		// Preserve the queue badge's independence from search/status while
		// restricting it to the explicitly selected customer when provided.
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM "+from+" WHERE u.role='customer' AND admin_customer_allowed($1,u.id) AND ($2::bigint IS NULL OR a.user_id=$2) AND w.status IN ('submitted','reviewing')", actor, customer).Scan(&page.PendingCount); err != nil {
			return page, err
		}
	}
	if limit < 1 || limit > 100 {
		limit = 25
	}
	if offset < 0 {
		offset = 0
	}
	limitArg, offsetArg := len(args)+1, len(args)+2
	rows, err := tx.Query(ctx, "SELECT "+projection+" FROM "+from+" WHERE "+where+" ORDER BY "+order+" DESC LIMIT $"+strconv.Itoa(limitArg)+" OFFSET $"+strconv.Itoa(offsetArg), append(args, limit, offset)...)
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

func affiliateCustomerRole(kind string, filters url.Values) (string, error) {
	values, provided := filters["customer_role"]
	if !provided {
		return "either", nil
	}
	_, customerProvided := filters["customer_id"]
	if kind != "attributions" || !customerProvided || len(values) != 1 || (values[0] != "promoter" && values[0] != "referred" && values[0] != "either") {
		return "", &AccountBusinessError{400, "invalid_customer_role", "客户角色只适用于指定客户的归因列表，可选 promoter、referred 或 either"}
	}
	return values[0], nil
}

func adminAffiliateReadAuthority(ctx context.Context, tx pgx.Tx, actor int64, permission string) error {
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM administrators a LEFT JOIN admin_roles r ON r.id=a.admin_role_id
 WHERE a.id=$1 AND a.status='active' AND (a.role='super_admin' OR (a.role='admin' AND r.active AND $2=ANY(r.permissions))))`, actor, permission).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return &AccountBusinessError{403, "permission_denied", "没有读取此推广信息的权限"}
	}
	return nil
}
