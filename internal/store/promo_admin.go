package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Keep the legacy field order and omission rules: existing creation receipts
// fingerprint this structure, including its empty product and cycle arrays.
type PromoAdminInput struct {
	Code                    string   `json:"code"`
	DiscountType            string   `json:"discount_type"`
	Value                   int64    `json:"value"`
	Cycles                  []string `json:"cycles"`
	AppliesProductIDs       []int64  `json:"applies_product_ids"`
	RequiresProductIDs      []int64  `json:"requires_product_ids"`
	RequiresExisting        bool     `json:"requires_existing"`
	StartsAt                string   `json:"starts_at"`
	ExpiresAt               string   `json:"expires_at"`
	MaxUses                 int64    `json:"max_uses"`
	OneTime                 bool     `json:"one_time"`
	OnlyNewCustomer         bool     `json:"only_new_customer"`
	OnlyOldCustomer         bool     `json:"only_old_customer"`
	OncePerCustomer         bool     `json:"once_per_customer"`
	Recurring               bool     `json:"recurring"`
	RecurringLimit          int64    `json:"recurring_limit"`
	UpgradeOnly             bool     `json:"upgrade_only"`
	Notes                   string   `json:"notes"`
	AppliesCustomerGroupIDs []int64  `json:"applies_customer_group_ids,omitempty"`
}

// OptionalPromoTime preserves the existing PATCH fingerprint and distinguishes
// omitted dates from explicit null/empty clears.
type OptionalPromoTime struct {
	Present bool
	Value   *time.Time
	Invalid bool
}

func (o *OptionalPromoTime) UnmarshalJSON(raw []byte) error {
	o.Present = true
	if string(raw) == "null" {
		o.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		o.Invalid = true
		return nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		o.Value = nil
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		o.Invalid = true
		return nil
	}
	o.Value = &parsed
	return nil
}

type PromoPatchInput struct {
	Active                  *bool             `json:"active"`
	StartsAt                OptionalPromoTime `json:"starts_at"`
	ExpiresAt               OptionalPromoTime `json:"expires_at"`
	MaxUses                 *int64            `json:"max_uses"`
	Notes                   *string           `json:"notes"`
	ExpectedVersion         *int64            `json:"expected_version"`
	AppliesCustomerGroupIDs *[]int64          `json:"applies_customer_group_ids,omitempty"`
}

func CanonicalPromoCustomerGroups(ids []int64) ([]int64, error) {
	if len(ids) > 100 {
		return nil, conflict("invalid_promo_customer_groups", "每个优惠码最多指定100个客户组")
	}
	if len(ids) == 0 {
		return nil, nil
	}
	out := slices.Clone(ids)
	slices.Sort(out)
	if out[0] < 1 {
		return nil, conflict("invalid_promo_customer_groups", "客户组编号无效")
	}
	return slices.Compact(out), nil
}

const promoAdminStatusSQL = `CASE WHEN expires_at<=statement_timestamp() THEN 'expired' WHEN NOT active THEN 'disabled' WHEN starts_at>statement_timestamp() THEN 'scheduled' WHEN (max_uses>0 AND used_count>=max_uses) OR (one_time AND used_count>0) THEN 'exhausted' ELSE 'active' END`
const promoAdminColumns = `id,code,discount_type,value,cycles,applies_product_ids,requires_product_ids,requires_existing,starts_at,expires_at,max_uses,used_count,one_time,only_new_customer,only_old_customer,once_per_customer,recurring,recurring_limit,upgrade_only,active,notes,created_at,updated_at,version,` + promoAdminStatusSQL + ` AS status,
 ARRAY(SELECT pcg.customer_group_id FROM promo_customer_groups pcg WHERE pcg.promo_id=promo_codes.id ORDER BY pcg.customer_group_id) AS applies_customer_group_ids`

type PromoAdminFilter struct {
	Query, Active, Status string
	OrderEligible         bool
	Limit, Offset         int
}

type PromoAdminPage struct {
	Items  json.RawMessage `json:"items"`
	Total  int64           `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

func (s *Store) ListAdminPromoCodes(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+promoAdminColumns+` FROM promo_codes ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if items == nil {
		items = []map[string]any{}
	}
	return items, err
}

func (s *Store) PageAdminPromoCodes(ctx context.Context, filter PromoAdminFilter) (PromoAdminPage, error) {
	out := PromoAdminPage{Limit: filter.Limit, Offset: filter.Offset}
	err := s.Pool.QueryRow(ctx, `WITH classified AS (SELECT `+promoAdminColumns+` FROM promo_codes), filtered AS (SELECT * FROM classified WHERE ($1='' OR code ILIKE '%'||$1||'%' OR notes ILIKE '%'||$1||'%') AND ($2='all' OR active=($2='active')) AND (NOT $3 OR NOT upgrade_only) AND ($4='all' OR status=$4)), page AS (SELECT * FROM filtered ORDER BY id DESC LIMIT $5 OFFSET $6) SELECT coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY id DESC) FROM page),'[]'::jsonb),(SELECT count(*) FROM filtered)`, filter.Query, filter.Active, filter.OrderEligible, filter.Status, filter.Limit, filter.Offset).Scan(&out.Items, &out.Total)
	return out, err
}

// These options expose group identifiers/names only, using the promo read
// capability; customer profiles and global member counts stay private.
func (s *Store) PromoCustomerGroupOptions(ctx context.Context, query string, limit, offset int) (PromoAdminPage, error) {
	out := PromoAdminPage{Limit: limit, Offset: offset}
	err := s.Pool.QueryRow(ctx, `WITH filtered AS (SELECT id,name FROM customer_groups WHERE $1='' OR name ILIKE '%'||$1||'%'), page AS (SELECT * FROM filtered ORDER BY id LIMIT $2 OFFSET $3) SELECT coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY id) FROM page),'[]'::jsonb),(SELECT count(*) FROM filtered)`, query, limit, offset).Scan(&out.Items, &out.Total)
	return out, err
}

func lockPromoCustomerGroups(ctx context.Context, tx pgx.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id FROM customer_groups WHERE id=ANY($1) ORDER BY id FOR KEY SHARE`, ids)
	if err != nil {
		return err
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	if !slices.Equal(found, ids) {
		return conflict("promo_customer_group_not_found", "适用客户组不存在，请刷新后选择")
	}
	return nil
}

func replacePromoCustomerGroups(ctx context.Context, tx pgx.Tx, id int64, groups []int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM promo_customer_groups WHERE promo_id=$1`, id); err != nil {
		return err
	}
	if len(groups) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO promo_customer_groups(promo_id,customer_group_id) SELECT $1,unnest($2::bigint[])`, id, groups)
	return err
}

func (s *Store) CreateAdminPromoCode(ctx context.Context, actor int64, key string, in PromoAdminInput, start, expire *time.Time) (map[string]any, error) {
	var err error
	in.AppliesCustomerGroupIDs, err = CanonicalPromoCustomerGroups(in.AppliesCustomerGroupIDs)
	if err != nil {
		return nil, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockAdminPricingMutation(ctx, tx, actor, "promos"); err != nil {
		return nil, err
	}
	out := map[string]any{}
	hash, replay, err := CreationReceipt(ctx, tx, actor, "promo_code.create", key, in, &out)
	if err != nil {
		return nil, err
	}
	if replay {
		return out, commitAdminPricingMutation(ctx, tx, actor)
	}
	if err = lockPromoCustomerGroups(ctx, tx, in.AppliesCustomerGroupIDs); err != nil {
		return nil, err
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO promo_codes(code,discount_type,value,cycles,applies_product_ids,requires_product_ids,requires_existing,starts_at,expires_at,max_uses,one_time,only_new_customer,only_old_customer,once_per_customer,recurring,notes,created_by,recurring_limit,upgrade_only)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19) RETURNING id`, in.Code, in.DiscountType, in.Value, in.Cycles, in.AppliesProductIDs, in.RequiresProductIDs, in.RequiresExisting, start, expire, in.MaxUses, in.OneTime, in.OnlyNewCustomer, in.OnlyOldCustomer, in.OncePerCustomer, in.Recurring, in.Notes, actor, in.RecurringLimit, in.UpgradeOnly).Scan(&id)
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return nil, conflict("promo_code_exists", "优惠码已存在")
		}
		return nil, err
	}
	if err = replacePromoCustomerGroups(ctx, tx, id, in.AppliesCustomerGroupIDs); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+promoAdminColumns+` FROM promo_codes WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	out, err = pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if err = SaveCreationReceipt(ctx, tx, actor, "promo_code.create", key, hash, out); err != nil {
		return nil, err
	}
	if err = Audit(ctx, tx, actor, "promo_code.created", "promo_code", id, map[string]any{"code": in.Code, "discount_type": in.DiscountType, "applies_customer_group_ids": in.AppliesCustomerGroupIDs}); err != nil {
		return nil, err
	}
	return out, commitAdminPricingMutation(ctx, tx, actor)
}

func (s *Store) UpdateAdminPromoCode(ctx context.Context, actor, id int64, key string, in PromoPatchInput) (map[string]any, error) {
	if in.AppliesCustomerGroupIDs != nil {
		if in.ExpectedVersion == nil || *in.ExpectedVersion < 1 {
			return nil, conflict("promo_version_required", "修改适用客户组需要优惠码当前版本")
		}
		groups, err := CanonicalPromoCustomerGroups(*in.AppliesCustomerGroupIDs)
		if err != nil {
			return nil, err
		}
		if groups == nil {
			groups = []int64{}
		}
		in.AppliesCustomerGroupIDs = &groups
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockAdminPricingMutation(ctx, tx, actor, "promos"); err != nil {
		return nil, err
	}
	out := map[string]any{}
	operation := fmt.Sprintf("promo_code.update.%d", id)
	hash, replay, err := CreationReceipt(ctx, tx, actor, operation, key, in, &out)
	if err != nil {
		return nil, err
	}
	if replay {
		return out, commitAdminPricingMutation(ctx, tx, actor)
	}
	var start, expire *time.Time
	var version int64
	if err = tx.QueryRow(ctx, `SELECT starts_at,expires_at,version FROM promo_codes WHERE id=$1 FOR UPDATE`, id).Scan(&start, &expire, &version); err != nil {
		return nil, err
	}
	if in.ExpectedVersion != nil && *in.ExpectedVersion != version {
		return nil, conflict("promo_version_conflict", "优惠码已更新，请重新加载后保存")
	}
	if in.StartsAt.Present {
		start = in.StartsAt.Value
	}
	if in.ExpiresAt.Present {
		expire = in.ExpiresAt.Value
	}
	if start != nil && expire != nil && !expire.After(*start) {
		return nil, conflict("invalid_promo_time", "结束时间必须晚于开始时间")
	}
	if in.AppliesCustomerGroupIDs != nil {
		if err = lockPromoCustomerGroups(ctx, tx, *in.AppliesCustomerGroupIDs); err != nil {
			return nil, err
		}
		if err = replacePromoCustomerGroups(ctx, tx, id, *in.AppliesCustomerGroupIDs); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `UPDATE promo_codes SET active=coalesce($2,active),starts_at=$3,expires_at=$4,max_uses=coalesce($5,max_uses),notes=coalesce($6,notes),version=version+1,updated_at=now() WHERE id=$1 RETURNING `+promoAdminColumns, id, in.Active, start, expire, in.MaxUses, in.Notes)
	if err != nil {
		return nil, err
	}
	out, err = pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if err = Audit(ctx, tx, actor, "promo_code.updated", "promo_code", id, map[string]any{"applies_customer_group_ids": in.AppliesCustomerGroupIDs}); err != nil {
		return nil, err
	}
	if err = SaveCreationReceipt(ctx, tx, actor, operation, key, hash, out); err != nil {
		return nil, err
	}
	return out, commitAdminPricingMutation(ctx, tx, actor)
}
