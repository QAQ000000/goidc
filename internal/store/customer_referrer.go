package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type CustomerReferrerInput struct {
	ReferrerCustomerID *int64 `json:"referrer_customer_id"`
	ExpectedVersion    int64  `json:"expected_version"`
	Reason             string `json:"reason"`
}
type CustomerReferrerFilter struct {
	Q             string
	Limit, Offset int
}

// Financial writers already hold user/invoice rows before invoice INSERT's
// shared graph lock. Do not lock any users row while holding the graph lock.
func lockCustomerReferrerAuthority(ctx context.Context, tx pgx.Tx, actor, customer int64, write bool, candidates ...int64) error {
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok || credential.Hash == "" {
		return conflict("unauthorized", "需要有效管理员凭据")
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return err
	}
	for _, permission := range []string{"customers.manage", "affiliates.manage"} {
		if err := RequireAdministratorPermission(ctx, tx, actor, permission); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return conflict("permission_denied", "没有修改推荐人的权限")
			}
			return err
		}
	}
	if err := recheckCustomerReferrerCredential(ctx, tx, actor); err != nil {
		return err
	}
	// All sales scopes precede the referral graph. A sales reassignment can
	// hold its exclusive scope while waiting for a financial user's row lock;
	// taking a new scope after the graph would complete a three-way deadlock.
	scopes := append([]int64{customer}, candidates...)
	sort.Slice(scopes, func(i, j int) bool { return scopes[i] < scopes[j] })
	for i, id := range scopes {
		if id < 1 || (i > 0 && scopes[i-1] == id) {
			continue
		}
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(hashtextextended('customer-sales:'||$1::bigint::text,0))", id); err != nil {
			return err
		}
	}
	// Candidate identities can become hidden after a successful operation. Lock
	// their scopes above, but authorize candidates only for new writes so that
	// an existing receipt can still be returned with its identity masked.
	if err := LockAdminCustomer(ctx, tx, actor, customer); err != nil {
		return err
	}
	query := "SELECT pg_advisory_xact_lock_shared(hashtextextended('affiliate-referral-graph',0))"
	if write {
		query = "SELECT pg_advisory_xact_lock(hashtextextended('affiliate-referral-graph',0))"
	}
	if _, err := tx.Exec(ctx, query); err != nil {
		return err
	}
	return recheckCustomerReferrerCredential(ctx, tx, actor)
}
func recheckCustomerReferrerCredential(ctx context.Context, tx pgx.Tx, actor int64) error {
	if err := lockStorageCredential(ctx, tx, actor); err != nil {
		return err
	}
	return lockStorageCredential(ctx, tx, actor)
}
func commitCustomerReferrer(ctx context.Context, tx pgx.Tx, actor int64) error {
	if err := recheckCustomerReferrerCredential(ctx, tx, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func currentCustomerReferrer(ctx context.Context, tx pgx.Tx, actor, customer int64) (map[string]any, error) {
	var out map[string]any
	err := tx.QueryRow(ctx, `SELECT jsonb_build_object(
 'customer_id',u.id,'referral_version',coalesce(st.version,1),
 'attributed',coalesce(at.status='active',false),
 'referrer_visible',CASE WHEN at.status='active' THEN admin_customer_allowed($1,a.user_id) ELSE true END,
 'referrer_customer_id',CASE WHEN at.status='active' AND admin_customer_allowed($1,a.user_id) THEN a.user_id END,
 'referrer_name',CASE WHEN at.status='active' AND admin_customer_allowed($1,a.user_id) THEN ref.display_name END,
 'effective_scope','future_invoices_only')
 FROM users u LEFT JOIN customer_referral_states st ON st.user_id=u.id
 LEFT JOIN affiliate_attributions at ON at.user_id=u.id LEFT JOIN affiliate_accounts a ON a.id=at.account_id
 LEFT JOIN users ref ON ref.id=a.user_id WHERE u.id=$2 AND u.role='customer' AND admin_customer_allowed($1,u.id)`, actor, customer).Scan(&out)
	return out, err
}
func (s *Store) AdminCustomerReferrer(ctx context.Context, actor, customer int64) (map[string]any, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockCustomerReferrerAuthority(ctx, tx, actor, customer, false); err != nil {
		return nil, err
	}
	out, err := currentCustomerReferrer(ctx, tx, actor, customer)
	if err != nil {
		return nil, err
	}
	return out, commitCustomerReferrer(ctx, tx, actor)
}
func referralMutationError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23514" && pg.Message == "affiliate_referral_cycle" {
		return conflict("affiliate_referral_cycle", "推荐人不能是本人，也不能形成循环关系")
	}
	return err
}
func (s *Store) SetAdminCustomerReferrer(ctx context.Context, actor, customer int64, key string, in CustomerReferrerInput) (map[string]any, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	key = strings.TrimSpace(key)
	if !utf8.ValidString(in.Reason) || utf8.RuneCountInString(in.Reason) < 1 || utf8.RuneCountInString(in.Reason) > 500 || strings.ContainsRune(in.Reason, 0) {
		return nil, &AccountBusinessError{400, "invalid_referral_reason", "请填写不超过500字的修改原因"}
	}
	if in.ExpectedVersion < 1 {
		return nil, &AccountBusinessError{409, "referral_version_required", "请提交当前推荐关系版本"}
	}
	if len(key) < 8 || len(key) > 128 {
		return nil, &AccountBusinessError{400, "invalid_idempotency_key", "请提供长度为8到128的请求键"}
	}
	if in.ReferrerCustomerID != nil && *in.ReferrerCustomerID < 1 {
		return nil, &AccountBusinessError{400, "invalid_referrer", "推荐客户编号无效"}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var candidate int64
	if in.ReferrerCustomerID != nil {
		candidate = *in.ReferrerCustomerID
	}
	if err = lockCustomerReferrerAuthority(ctx, tx, actor, customer, true, candidate); err != nil {
		return nil, err
	}
	scope := fmt.Sprintf("admin:customer.referrer:%d", customer)
	hash := accountBusinessHash(in)
	out := map[string]any{}
	replay, err := ReplayOperation(ctx, tx, actor, scope, key, hash, &out)
	if err != nil {
		return nil, err
	}
	if replay {
		// Returning a financial ownership fact must not reveal a now-hidden
		// counterparty. Preserve the committed version/changed fact, mask identity.
		out["replayed"] = true
		if raw, ok := out["referrer_customer_id"].(float64); ok {
			var visible bool
			if err = tx.QueryRow(ctx, "SELECT admin_customer_allowed($1,$2)", actor, int64(raw)).Scan(&visible); err != nil {
				return nil, err
			}
			if !visible {
				out["referrer_customer_id"] = nil
				out["referrer_name"] = nil
				out["referrer_visible"] = false
			}
		}
		return out, commitCustomerReferrer(ctx, tx, actor)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO customer_referral_states(user_id) VALUES($1) ON CONFLICT DO NOTHING", customer); err != nil {
		return nil, err
	}
	var version int64
	if err = tx.QueryRow(ctx, "SELECT version FROM customer_referral_states WHERE user_id=$1 FOR UPDATE", customer).Scan(&version); err != nil {
		return nil, err
	}
	if err = recheckCustomerReferrerCredential(ctx, tx, actor); err != nil {
		return nil, err
	}
	if version != in.ExpectedVersion {
		return nil, conflict("version_conflict", "推荐关系已变更，请刷新后重新核对")
	}
	var oldAccount, newAccount *int64
	var previous json.RawMessage
	var rowID int64
	err = tx.QueryRow(ctx, `SELECT id,CASE WHEN status='active' THEN account_id END,to_jsonb(at) FROM affiliate_attributions at WHERE user_id=$1 FOR UPDATE`, customer).Scan(&rowID, &oldAccount, &previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if previous == nil {
		previous = json.RawMessage(`{}`)
	}
	if in.ReferrerCustomerID != nil {
		if *in.ReferrerCustomerID == customer {
			return nil, conflict("affiliate_referral_cycle", "推荐人不能是本人，也不能形成循环关系")
		}
		if err = LockAdminCustomer(ctx, tx, actor, *in.ReferrerCustomerID); err != nil {
			return nil, err
		}
		var id int64
		err = tx.QueryRow(ctx, `SELECT a.id FROM affiliate_accounts a JOIN users u ON u.id=a.user_id JOIN affiliate_programs p ON p.id=a.program_id
   WHERE a.user_id=$1 AND u.role='customer' AND u.status='active' AND a.status='approved' AND p.status='active' FOR SHARE OF a,p`, *in.ReferrerCustomerID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, conflict("referrer_unavailable", "推荐人必须是已审核且已启用推广的客户")
		}
		if err != nil {
			return nil, err
		}
		newAccount = &id
	}
	changed := (oldAccount == nil) != (newAccount == nil) || (oldAccount != nil && newAccount != nil && *oldAccount != *newAccount)
	if changed {
		source, _ := json.Marshal(map[string]any{"type": "administrator_assignment", "actor_id": actor, "reason": in.Reason})
		if newAccount == nil {
			_, err = tx.Exec(ctx, "UPDATE affiliate_attributions SET status='unassigned',version=version+1,source_snapshot=$2 WHERE user_id=$1", customer, source)
		} else if rowID == 0 {
			_, err = tx.Exec(ctx, `INSERT INTO affiliate_attributions(user_id,account_id,status,first_clicked_at,registered_at,source_snapshot) VALUES($1,$2,'active',clock_timestamp(),clock_timestamp(),$3)`, customer, *newAccount, source)
		} else {
			_, err = tx.Exec(ctx, `UPDATE affiliate_attributions SET account_id=$2,status='active',link_id=NULL,click_id=NULL,registered_at=clock_timestamp(),source_snapshot=$3,version=version+1 WHERE user_id=$1`, customer, *newAccount, source)
		}
		if err != nil {
			return nil, referralMutationError(err)
		}
		var after int64
		if err = tx.QueryRow(ctx, "SELECT version FROM customer_referral_states WHERE user_id=$1", customer).Scan(&after); err != nil {
			return nil, err
		}
		if after != version+1 {
			return nil, fmt.Errorf("referral version transition invalid")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO customer_referral_changes(user_id,old_account_id,new_account_id,actor_id,reason,version_before,version_after,previous_attribution) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, customer, oldAccount, newAccount, actor, in.Reason, version, after, previous); err != nil {
			return nil, err
		}
	}
	if err = Audit(ctx, tx, actor, "customer.referrer.changed", "user", customer, map[string]any{"old_account_id": oldAccount, "new_account_id": newAccount, "reason": in.Reason, "version_before": version, "changed": changed, "effective_scope": "future_invoices_only"}); err != nil {
		return nil, err
	}
	out, err = currentCustomerReferrer(ctx, tx, actor, customer)
	if err != nil {
		return nil, err
	}
	out["changed"], out["replayed"] = changed, false
	if err = SaveOperation(ctx, tx, actor, scope, key, hash, out); err != nil {
		return nil, err
	}
	return out, commitCustomerReferrer(ctx, tx, actor)
}
func (s *Store) AdminCustomerReferrerOptions(ctx context.Context, actor, customer int64, f CustomerReferrerFilter) (map[string]any, error) {
	if f.Limit < 1 || f.Limit > 100 || f.Offset < 0 || !utf8.ValidString(f.Q) || utf8.RuneCountInString(f.Q) > 120 {
		return nil, &AccountBusinessError{400, "invalid_referrer_filter", "推荐人筛选条件无效"}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockCustomerReferrerAuthority(ctx, tx, actor, customer, false); err != nil {
		return nil, err
	}
	var out map[string]any
	err = tx.QueryRow(ctx, `WITH eligible AS (
 SELECT u.id,u.username,u.display_name,u.email FROM users u JOIN affiliate_accounts a ON a.user_id=u.id JOIN affiliate_programs p ON p.id=a.program_id
 WHERE u.role='customer' AND u.status='active' AND a.status='approved' AND p.status='active' AND u.id<>$2 AND admin_customer_allowed($1,u.id)
 AND ($3='' OR strpos(lower(concat_ws(' ',u.id::text,u.username,u.display_name,u.email)),lower($3))>0)
 ), chosen AS (SELECT * FROM eligible ORDER BY id LIMIT $4 OFFSET $5)
 SELECT jsonb_build_object('items',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY id),'[]'::jsonb) FROM chosen c),'total',(SELECT count(*) FROM eligible),'limit',$4::int,'offset',$5::int)`, actor, customer, strings.TrimSpace(f.Q), f.Limit, f.Offset).Scan(&out)
	if err != nil {
		return nil, err
	}
	return out, commitCustomerReferrer(ctx, tx, actor)
}
func (s *Store) AdminCustomerReferrerHistory(ctx context.Context, actor, customer int64, f CustomerReferrerFilter) (map[string]any, error) {
	if f.Limit < 1 || f.Limit > 100 || f.Offset < 0 || f.Q != "" {
		return nil, &AccountBusinessError{400, "invalid_referrer_filter", "推荐关系历史参数无效"}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockCustomerReferrerAuthority(ctx, tx, actor, customer, false); err != nil {
		return nil, err
	}
	var out map[string]any
	err = tx.QueryRow(ctx, `WITH events AS (
 SELECT c.id,c.user_id customer_id,c.actor_id,c.reason,c.version_before,c.version_after,c.created_at,
 CASE WHEN admin_customer_allowed($1,old.user_id) THEN old.user_id END old_referrer_customer_id,
 CASE WHEN admin_customer_allowed($1,nw.user_id) THEN nw.user_id END new_referrer_customer_id,
 CASE WHEN admin_customer_allowed($1,old.user_id) THEN ou.display_name END old_referrer_name,
 CASE WHEN admin_customer_allowed($1,nw.user_id) THEN nu.display_name END new_referrer_name,
 c.old_account_id IS NOT NULL old_attributed,c.new_account_id IS NOT NULL new_attributed,
 CASE WHEN c.old_account_id IS NOT NULL THEN admin_customer_allowed($1,old.user_id) ELSE true END old_referrer_visible,
 CASE WHEN c.new_account_id IS NOT NULL THEN admin_customer_allowed($1,nw.user_id) ELSE true END new_referrer_visible
 FROM customer_referral_changes c LEFT JOIN affiliate_accounts old ON old.id=c.old_account_id LEFT JOIN users ou ON ou.id=old.user_id
 LEFT JOIN affiliate_accounts nw ON nw.id=c.new_account_id LEFT JOIN users nu ON nu.id=nw.user_id
 WHERE c.user_id=$2
 ), chosen AS (SELECT * FROM events ORDER BY id DESC LIMIT $3 OFFSET $4)
 SELECT jsonb_build_object('items',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY id DESC),'[]'::jsonb) FROM chosen c),'total',(SELECT count(*) FROM events),'limit',$3::int,'offset',$4::int)`, actor, customer, f.Limit, f.Offset).Scan(&out)
	if err != nil {
		return nil, err
	}
	return out, commitCustomerReferrer(ctx, tx, actor)
}
