package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type AffiliateProductRule struct {
	ProductID   int64  `json:"product_id"`
	EventType   string `json:"event_type"`
	Mode        string `json:"mode"`
	RateBPS     int    `json:"rate_bps"`
	AmountCents int64  `json:"amount_cents"`
	MaxCents    *int64 `json:"max_cents"`
}
type AffiliateTierRule struct {
	LowerCents  int64  `json:"lower_cents"`
	UpperCents  *int64 `json:"upper_cents"`
	RateBPS     int    `json:"rate_bps"`
	AmountCents int64  `json:"amount_cents"`
}
type AffiliateRules struct {
	Version      int64                  `json:"version"`
	ProductRules []AffiliateProductRule `json:"product_rules"`
	TierRules    []AffiliateTierRule    `json:"tier_rules"`
}
type AffiliateRulesInput struct {
	ExpectedVersion int64                  `json:"expected_version"`
	ProductRules    []AffiliateProductRule `json:"product_rules"`
	TierRules       []AffiliateTierRule    `json:"tier_rules"`
}

func readAffiliateRules(ctx context.Context, tx pgx.Tx) (AffiliateRules, error) {
	out := AffiliateRules{ProductRules: []AffiliateProductRule{}, TierRules: []AffiliateTierRule{}}
	var id int64
	if err := tx.QueryRow(ctx, `SELECT rs.id,p.rule_version FROM affiliate_programs p JOIN affiliate_rule_sets rs ON rs.program_id=p.id AND rs.effective_to IS NULL ORDER BY p.id LIMIT 1`).Scan(&id, &out.Version); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT product_id,event_type,CASE WHEN mode='override' THEN CASE WHEN amount_cents>0 THEN 'fixed' ELSE 'rate' END ELSE mode END,rate_bps,amount_cents,max_cents FROM affiliate_product_rules WHERE rule_set_id=$1 ORDER BY product_id,event_type`, id)
	if err != nil {
		return out, err
	}
	out.ProductRules, err = pgx.CollectRows(rows, pgx.RowToStructByPos[AffiliateProductRule])
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `SELECT lower_cents,upper_cents,rate_bps,amount_cents FROM affiliate_tier_rules WHERE rule_set_id=$1 ORDER BY lower_cents`, id)
	if err != nil {
		return out, err
	}
	out.TierRules, err = pgx.CollectRows(rows, pgx.RowToStructByPos[AffiliateTierRule])
	return out, err
}
func (s *Store) AffiliateRules(ctx context.Context) (AffiliateRules, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return AffiliateRules{}, err
	}
	defer tx.Rollback(ctx)
	out, err := readAffiliateRules(ctx, tx)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func validateAffiliateRules(in AffiliateRulesInput) error {
	invalid := func() error {
		return conflict("invalid_affiliate_rule", "商品规则或阶梯边界无效；金额最多100万元，阶梯不得重叠")
	}
	if in.ExpectedVersion < 1 || in.ProductRules == nil || in.TierRules == nil || len(in.ProductRules) > 2000 || len(in.TierRules) > 200 {
		return invalid()
	}
	seen := map[string]bool{}
	for _, r := range in.ProductRules {
		if r.ProductID < 1 || (r.EventType != "first_purchase" && r.EventType != "reorder" && r.EventType != "renewal" && r.EventType != "upgrade") {
			return invalid()
		}
		k := string(mustAffiliateJSON([]any{r.ProductID, r.EventType}))
		if seen[k] {
			return invalid()
		}
		seen[k] = true
		if _, err := affiliateCommissionAmount(100, affiliateRule{RateBPS: r.RateBPS, AmountCents: r.AmountCents, MaxCents: r.MaxCents}); err != nil {
			return invalid()
		}
		if r.AmountCents > 100000000 || (r.MaxCents != nil && *r.MaxCents > 100000000) {
			return invalid()
		}
		switch r.Mode {
		case "inherit", "disabled":
			if r.RateBPS != 0 || r.AmountCents != 0 || r.MaxCents != nil {
				return invalid()
			}
		case "rate":
			if r.RateBPS <= 0 || r.AmountCents != 0 {
				return invalid()
			}
		case "fixed":
			if r.AmountCents <= 0 || r.RateBPS != 0 {
				return invalid()
			}
		default:
			return invalid()
		}
	}
	tiers := append([]AffiliateTierRule{}, in.TierRules...)
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].LowerCents < tiers[j].LowerCents })
	for i, r := range tiers {
		if r.LowerCents < 0 || r.LowerCents > 100000000 || (r.UpperCents != nil && (*r.UpperCents <= r.LowerCents || *r.UpperCents > 100000000)) || r.AmountCents > 100000000 {
			return invalid()
		}
		if _, err := affiliateCommissionAmount(100, affiliateRule{RateBPS: r.RateBPS, AmountCents: r.AmountCents}); err != nil {
			return invalid()
		}
		if i > 0 && (tiers[i-1].UpperCents == nil || *tiers[i-1].UpperCents > r.LowerCents) {
			return invalid()
		}
	}
	return nil
}
func mustAffiliateJSON(v any) []byte { raw, _ := json.Marshal(v); return raw }
func (s *Store) UpdateAffiliateRules(ctx context.Context, actor int64, sessionHash, key string, in AffiliateRulesInput) (AffiliateRules, error) {
	var out AffiliateRules
	key = strings.TrimSpace(key)
	if len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_affiliate_rule", "需要8至128字符的幂等键")
	}
	if err := validateAffiliateRules(in); err != nil {
		return out, err
	}
	hash := affiliateHash(string(mustAffiliateJSON(in)))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(70108190)`); err != nil {
		return out, err
	}
	if err = lockOperationKey(ctx, tx, "affiliate-policy", "default"); err != nil {
		return out, err
	}
	if err = recheckAffiliatePolicyManager(ctx, tx, actor, sessionHash); err != nil {
		return out, err
	}
	if receipt, replay, e := affiliateOperation(ctx, tx, actor, "rules.update", key, hash); e != nil {
		return out, e
	} else if replay {
		if err = json.Unmarshal(mustAffiliateJSON(receipt), &out); err != nil {
			return out, err
		}
		return out, tx.Commit(ctx)
	}
	policy, err := readAffiliatePolicy(tx.QueryRow(ctx, affiliatePolicySelect+` FOR UPDATE OF p,rs`))
	if err != nil {
		return out, err
	}
	if policy.Version != in.ExpectedVersion {
		return out, conflict("version_conflict", "推介规则已变更，请重新加载")
	}
	before, err := readAffiliateRules(ctx, tx)
	if err != nil {
		return out, err
	}
	for _, r := range in.ProductRules {
		var id int64
		if err = tx.QueryRow(ctx, `SELECT id FROM products WHERE id=$1 FOR KEY SHARE`, r.ProductID).Scan(&id); err != nil {
			if err == pgx.ErrNoRows {
				return out, conflict("invalid_affiliate_rule", "商品不存在")
			}
			return out, err
		}
	}
	var oldID, newID int64
	var effective time.Time
	if err = tx.QueryRow(ctx, `SELECT id,GREATEST(clock_timestamp(),effective_from+interval '1 microsecond') FROM affiliate_rule_sets WHERE program_id=$1 AND effective_to IS NULL`, policy.ID).Scan(&oldID, &effective); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE affiliate_rule_sets SET effective_to=$2 WHERE id=$1`, oldID, effective); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO affiliate_rule_sets(program_id,version,effective_from,default_rules,created_by) SELECT program_id,version+1,$2,default_rules,$3 FROM affiliate_rule_sets WHERE id=$1 RETURNING id`, oldID, effective, actor).Scan(&newID); err != nil {
		return out, err
	}
	for _, r := range in.ProductRules {
		mode := r.Mode
		if mode == "rate" || mode == "fixed" {
			mode = "override"
		}
		if _, err = tx.Exec(ctx, `INSERT INTO affiliate_product_rules(rule_set_id,product_id,event_type,mode,rate_bps,amount_cents,max_cents) VALUES($1,$2,$3,$4,$5,$6,$7)`, newID, r.ProductID, r.EventType, mode, r.RateBPS, r.AmountCents, r.MaxCents); err != nil {
			return out, err
		}
	}
	for _, r := range in.TierRules {
		if _, err = tx.Exec(ctx, `INSERT INTO affiliate_tier_rules(rule_set_id,lower_cents,upper_cents,rate_bps,amount_cents) VALUES($1,$2,$3,$4,$5)`, newID, r.LowerCents, r.UpperCents, r.RateBPS, r.AmountCents); err != nil {
			return out, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE affiliate_programs SET rule_version=rule_version+1,updated_at=$2 WHERE id=$1`, policy.ID, effective); err != nil {
		return out, err
	}
	out, err = readAffiliateRules(ctx, tx)
	if err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, "affiliate.rules.updated", "affiliate_program", policy.ID, map[string]any{"before": before, "after": out}); err != nil {
		return out, err
	}
	var receipt map[string]any
	_ = json.Unmarshal(mustAffiliateJSON(out), &receipt)
	if err = saveAffiliateOperation(ctx, tx, actor, "rules.update", key, hash, receipt); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
