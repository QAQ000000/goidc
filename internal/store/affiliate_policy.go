package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type AffiliatePolicyRule struct {
	RateBPS     int    `json:"rate_bps"`
	AmountCents int64  `json:"amount_cents"`
	MaxCents    *int64 `json:"max_cents"`
}
type AffiliatePolicy struct {
	ID               int64                          `json:"id"`
	Name             string                         `json:"name"`
	Enabled          bool                           `json:"enabled"`
	AttributionDays  int                            `json:"attribution_days"`
	HoldDays         int                            `json:"hold_days"`
	MinWithdrawCents int64                          `json:"min_withdraw_cents"`
	FeeCents         int64                          `json:"fee_cents"`
	DefaultRules     map[string]AffiliatePolicyRule `json:"default_rules"`
	Version          int64                          `json:"version"`
	UpdatedAt        time.Time                      `json:"updated_at"`
}
type AffiliatePolicyInput struct {
	Name             string                         `json:"name"`
	Enabled          *bool                          `json:"enabled"`
	AttributionDays  int                            `json:"attribution_days"`
	HoldDays         *int                           `json:"hold_days"`
	MinWithdrawCents int64                          `json:"min_withdraw_cents"`
	FeeCents         *int64                         `json:"fee_cents"`
	DefaultRules     map[string]AffiliatePolicyRule `json:"default_rules"`
	ExpectedVersion  int64                          `json:"expected_version"`
}

const affiliatePolicySelect = `SELECT p.id,p.name,p.status='active',p.attribution_days,p.hold_days,p.min_withdraw_cents,p.fee_cents,rs.default_rules,p.rule_version,p.updated_at FROM affiliate_programs p JOIN affiliate_rule_sets rs ON rs.program_id=p.id AND rs.effective_to IS NULL ORDER BY p.id LIMIT 1`

func readAffiliatePolicy(row pgx.Row) (AffiliatePolicy, error) {
	var out AffiliatePolicy
	var rules []byte
	err := row.Scan(&out.ID, &out.Name, &out.Enabled, &out.AttributionDays, &out.HoldDays, &out.MinWithdrawCents, &out.FeeCents, &rules, &out.Version, &out.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(rules, &out.DefaultRules)
	}
	return out, err
}
func (s *Store) AffiliatePolicy(ctx context.Context) (AffiliatePolicy, error) {
	return readAffiliatePolicy(s.Pool.QueryRow(ctx, affiliatePolicySelect))
}
func recheckAffiliatePolicyManager(ctx context.Context, tx pgx.Tx, actor int64, sessionHash string) error {
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, `SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE`, actor).Scan(&role, &roleID); err != nil {
		if err == pgx.ErrNoRows {
			return conflict("permission_denied", "没有管理推介设置的权限")
		}
		return err
	}
	if role != "super_admin" {
		if role != "admin" || roleID == nil {
			return conflict("permission_denied", "没有管理推介设置的权限")
		}
		var allowed bool
		if err := tx.QueryRow(ctx, `SELECT active AND 'affiliates.manage'=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE`, *roleID).Scan(&allowed); err != nil {
			if err == pgx.ErrNoRows {
				return conflict("permission_denied", "没有管理推介设置的权限")
			}
			return err
		}
		if !allowed {
			return conflict("permission_denied", "没有管理推介设置的权限")
		}
	}
	var sessionUser int64
	if err := tx.QueryRow(ctx, `SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE`, sessionHash, actor).Scan(&sessionUser); err != nil {
		if err == pgx.ErrNoRows {
			return conflict("unauthorized", "登录已失效")
		}
		return err
	}
	return nil
}
func (s *Store) UpdateAffiliatePolicy(ctx context.Context, actor int64, sessionHash, key string, in AffiliatePolicyInput) (AffiliatePolicy, error) {
	var out AffiliatePolicy
	in.Name = strings.TrimSpace(in.Name)
	key = strings.TrimSpace(key)
	if !contentTextValid(in.Name, 120, false) || in.Name == "" || in.Enabled == nil || in.HoldDays == nil || in.FeeCents == nil || in.ExpectedVersion < 1 || len(key) < 8 || len(key) > 128 || in.AttributionDays < 1 || in.AttributionDays > 365 || *in.HoldDays < 0 || *in.HoldDays > 365 || in.MinWithdrawCents < 1 || in.MinWithdrawCents > 100000000 || *in.FeeCents < 0 || *in.FeeCents >= in.MinWithdrawCents || in.DefaultRules == nil {
		return out, conflict("invalid_affiliate_policy", "请提供完整推介设置；手续费需低于最低提现金额，金额最多 100 万元")
	}
	for event, rule := range in.DefaultRules {
		if event != "first_purchase" && event != "reorder" && event != "renewal" && event != "upgrade" {
			return out, conflict("invalid_affiliate_policy", "不支持此计佣事件")
		}
		if _, err := affiliateCommissionAmount(100, affiliateRule{RateBPS: rule.RateBPS, AmountCents: rule.AmountCents, MaxCents: rule.MaxCents}); err != nil {
			return out, err
		}
		if rule.AmountCents > 100000000 || (rule.MaxCents != nil && *rule.MaxCents > 100000000) {
			return out, conflict("invalid_affiliate_policy", "计佣金额最多 100 万元")
		}
	}
	raw, _ := json.Marshal(in)
	hash := affiliateHash(string(raw))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	// Follow the global settings lock before locking the program so role/session
	// changes cannot race a setting commit.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(70108190)`); err != nil {
		return out, err
	}
	if err = lockOperationKey(ctx, tx, "affiliate-policy", "default"); err != nil {
		return out, err
	}
	if err = recheckAffiliatePolicyManager(ctx, tx, actor, sessionHash); err != nil {
		return out, err
	}
	if receipt, replay, e := affiliateOperation(ctx, tx, actor, "policy.update", key, hash); e != nil {
		return out, e
	} else if replay {
		encoded, _ := json.Marshal(receipt)
		if err = json.Unmarshal(encoded, &out); err != nil {
			return out, err
		}
		return out, tx.Commit(ctx)
	}
	current, err := readAffiliatePolicy(tx.QueryRow(ctx, affiliatePolicySelect+` FOR UPDATE OF p,rs`))
	if err != nil {
		return out, err
	}
	if current.Version != in.ExpectedVersion {
		return out, conflict("version_conflict", "推介设置已变更，请重新加载")
	}
	var oldID, newID int64
	var effective time.Time
	if err = tx.QueryRow(ctx, `SELECT id,GREATEST(clock_timestamp(),effective_from+interval '1 microsecond') FROM affiliate_rule_sets WHERE program_id=$1 AND effective_to IS NULL`, current.ID).Scan(&oldID, &effective); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE affiliate_rule_sets SET effective_to=$2 WHERE id=$1`, oldID, effective); err != nil {
		return out, err
	}
	rules, _ := json.Marshal(in.DefaultRules)
	if err = tx.QueryRow(ctx, `INSERT INTO affiliate_rule_sets(program_id,version,effective_from,default_rules,created_by) VALUES($1,$2,$3,$4,$5) RETURNING id`, current.ID, current.Version+1, effective, rules, actor).Scan(&newID); err != nil {
		return out, err
	}
	// Product overrides and settlement tiers survive default policy changes.
	if _, err = tx.Exec(ctx, `INSERT INTO affiliate_product_rules(rule_set_id,product_id,event_type,mode,rate_bps,amount_cents,max_cents) SELECT $2,product_id,event_type,mode,rate_bps,amount_cents,max_cents FROM affiliate_product_rules WHERE rule_set_id=$1`, oldID, newID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO affiliate_tier_rules(rule_set_id,lower_cents,upper_cents,rate_bps,amount_cents) SELECT $2,lower_cents,upper_cents,rate_bps,amount_cents FROM affiliate_tier_rules WHERE rule_set_id=$1`, oldID, newID); err != nil {
		return out, err
	}
	status := "disabled"
	if *in.Enabled {
		status = "active"
	}
	if _, err = tx.Exec(ctx, `UPDATE affiliate_programs SET name=$2,status=$3,attribution_days=$4,hold_days=$5,min_withdraw_cents=$6,fee_cents=$7,rule_version=rule_version+1,updated_at=$8 WHERE id=$1`, current.ID, in.Name, status, in.AttributionDays, *in.HoldDays, in.MinWithdrawCents, *in.FeeCents, effective); err != nil {
		return out, err
	}
	out, err = readAffiliatePolicy(tx.QueryRow(ctx, affiliatePolicySelect))
	if err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, "affiliate.policy.updated", "affiliate_program", current.ID, map[string]any{"before": current, "after": out}); err != nil {
		return out, err
	}
	encoded, _ := json.Marshal(out)
	var receipt map[string]any
	_ = json.Unmarshal(encoded, &receipt)
	if err = saveAffiliateOperation(ctx, tx, actor, "policy.update", key, hash, receipt); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
