package httpapi

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

var promoCodePattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{2,49}$`)

type cartPromoInput struct {
	Version int64  `json:"version"`
	Code    string `json:"code"`
}

func (s *Server) applyCartPromo(w http.ResponseWriter, r *http.Request) {
	var input cartPromoInput
	if !decode(w, r, &input) {
		return
	}
	input.Code = strings.ToUpper(strings.TrimSpace(input.Code))
	if input.Version < 1 || !promoCodePattern.MatchString(input.Code) {
		writeError(w, http.StatusBadRequest, "invalid_promo", "优惠码或购物车版本无效")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var cartID string
	var version int64
	if err = tx.QueryRow(r.Context(), "SELECT id::text,version FROM carts WHERE user_id=$1 FOR UPDATE", identityFrom(r).ID).Scan(&cartID, &version); err != nil {
		s.fail(w, r, err)
		return
	}
	if version != input.Version {
		writeError(w, http.StatusConflict, "stale_cart", "购物车已更新，请刷新后操作")
		return
	}
	quote, _, err := s.db.CartPromoQuote(r.Context(), tx, identityFrom(r).ID, cartID, input.Code, false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO cart_promo_codes(cart_id,promo_code_id,code) VALUES($1,$2,$3)
ON CONFLICT(cart_id) DO UPDATE SET promo_code_id=EXCLUDED.promo_code_id,code=EXCLUDED.code,created_at=now()`, cartID, quote.ID, quote.Code); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE carts SET version=version+1,updated_at=now() WHERE id=$1", cartID); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"code": quote.Code, "discount_cents": quote.DiscountCents, "version": version + 1})
}

func (s *Server) removeCartPromo(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version int64 `json:"version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Version < 1 {
		writeError(w, http.StatusBadRequest, "invalid_cart", "购物车版本无效")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var cartID string
	var version int64
	if err = tx.QueryRow(r.Context(), "SELECT id::text,version FROM carts WHERE user_id=$1 FOR UPDATE", identityFrom(r).ID).Scan(&cartID, &version); err != nil {
		s.fail(w, r, err)
		return
	}
	if version != input.Version {
		writeError(w, http.StatusConflict, "stale_cart", "购物车已更新，请刷新后操作")
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM cart_promo_codes WHERE cart_id=$1", cartID); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE carts SET version=version+1,updated_at=now() WHERE id=$1", cartID); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"version": version + 1, "removed": true})
}

type promoAdminInput struct {
	Code               string   `json:"code"`
	DiscountType       string   `json:"discount_type"`
	Value              int64    `json:"value"`
	Cycles             []string `json:"cycles"`
	AppliesProductIDs  []int64  `json:"applies_product_ids"`
	RequiresProductIDs []int64  `json:"requires_product_ids"`
	RequiresExisting   bool     `json:"requires_existing"`
	StartsAt           string   `json:"starts_at"`
	ExpiresAt          string   `json:"expires_at"`
	MaxUses            int64    `json:"max_uses"`
	OneTime            bool     `json:"one_time"`
	OnlyNewCustomer    bool     `json:"only_new_customer"`
	OnlyOldCustomer    bool     `json:"only_old_customer"`
	OncePerCustomer    bool     `json:"once_per_customer"`
	Recurring          bool     `json:"recurring"`
	RecurringLimit     int64    `json:"recurring_limit"`
	UpgradeOnly        bool     `json:"upgrade_only"`
	Notes              string   `json:"notes"`
}

func parsePromoTime(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func validPromoInput(input *promoAdminInput) (*time.Time, *time.Time, error) {
	input.Code = strings.ToUpper(strings.TrimSpace(input.Code))
	input.DiscountType = strings.ToLower(strings.TrimSpace(input.DiscountType))
	input.Notes = strings.TrimSpace(input.Notes)
	if !promoCodePattern.MatchString(input.Code) || (input.DiscountType != "percent" && input.DiscountType != "fixed" && input.DiscountType != "free") || input.Value < 0 || input.MaxUses < 0 || len(input.Notes) > 2000 {
		return nil, nil, &store.Conflict{Code: "invalid_promo", Message: "优惠码字段无效"}
	}
	if input.DiscountType == "percent" && (input.Value < 1 || input.Value > 100) {
		return nil, nil, &store.Conflict{Code: "invalid_promo", Message: "折扣比例必须在 1-100 之间"}
	}
	if input.DiscountType == "fixed" && input.Value < 1 {
		return nil, nil, &store.Conflict{Code: "invalid_promo", Message: "固定折扣金额无效"}
	}
	if input.Recurring && input.DiscountType == "free" {
		return nil, nil, &store.Conflict{Code: "recurring_promo_unsupported", Message: "免初装费不支持循环续费"}
	}
	if input.UpgradeOnly && (input.Recurring || input.DiscountType == "free") {
		return nil, nil, &store.Conflict{Code: "invalid_upgrade_promo", Message: "升级专用优惠不支持循环或免初装费"}
	}
	if input.RecurringLimit < 0 || input.RecurringLimit > 10000 || !input.Recurring && input.RecurringLimit != 0 {
		return nil, nil, &store.Conflict{Code: "invalid_recurring_limit", Message: "续费优惠次数须在0-10000之间，且仅用于循环优惠"}
	}
	allowedCycles := map[string]bool{"monthly": true, "quarterly": true, "annually": true, "once": true}
	for _, cycle := range input.Cycles {
		if !allowedCycles[cycle] {
			return nil, nil, &store.Conflict{Code: "invalid_promo", Message: "优惠码账期无效"}
		}
	}
	start, err := parsePromoTime(input.StartsAt)
	if err != nil {
		return nil, nil, &store.Conflict{Code: "invalid_promo_time", Message: "开始时间必须是 RFC3339"}
	}
	expire, err := parsePromoTime(input.ExpiresAt)
	if err != nil {
		return nil, nil, &store.Conflict{Code: "invalid_promo_time", Message: "结束时间必须是 RFC3339"}
	}
	if start != nil && expire != nil && !expire.After(*start) {
		return nil, nil, &store.Conflict{Code: "invalid_promo_time", Message: "结束时间必须晚于开始时间"}
	}
	return start, expire, nil
}

func (s *Server) promoCodes(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, `SELECT id,code,discount_type,value,cycles,applies_product_ids,requires_product_ids,requires_existing,starts_at,expires_at,max_uses,used_count,one_time,only_new_customer,only_old_customer,once_per_customer,recurring,recurring_limit,upgrade_only,active,notes,created_at,updated_at FROM promo_codes ORDER BY id DESC`)
}

// promoCodesPage is the searchable, paginated contract used by admin pickers.
// Keep promoCodes above as the legacy array contract for existing screens.
func (s *Server) promoCodesPage(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 120 {
		writeError(w, http.StatusBadRequest, "invalid_query", "搜索条件过长")
		return
	}
	active := r.URL.Query().Get("active")
	if active == "" {
		active = "all"
	}
	if active != "all" && active != "active" && active != "inactive" {
		writeError(w, http.StatusBadRequest, "invalid_status", "优惠码状态筛选无效")
		return
	}
	orderEligible := false
	if raw := r.URL.Query().Get("order_eligible"); raw != "" {
		var err error
		orderEligible, err = strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_filter", "订单优惠筛选无效")
			return
		}
	}
	limit, offset := pagination(r)
	where := `($1='' OR code ILIKE '%'||$1||'%' OR notes ILIKE '%'||$1||'%')
AND ($2='all' OR active=($2='active'))
AND (NOT $3 OR NOT upgrade_only)`
	var total int64
	if err := s.db.Pool.QueryRow(r.Context(), `SELECT count(*) FROM promo_codes WHERE `+where, q, active, orderEligible).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `SELECT id,code,discount_type,value,cycles,applies_product_ids,requires_product_ids,requires_existing,starts_at,expires_at,max_uses,used_count,one_time,only_new_customer,only_old_customer,once_per_customer,recurring,recurring_limit,upgrade_only,active,notes,created_at,updated_at FROM promo_codes WHERE `+where+` ORDER BY id DESC LIMIT $4 OFFSET $5`, q, active, orderEligible, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	write(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) createPromoCode(w http.ResponseWriter, r *http.Request) {
	var input promoAdminInput
	if !decode(w, r, &input) {
		return
	}
	if input.Cycles == nil {
		input.Cycles = []string{}
	}
	if input.AppliesProductIDs == nil {
		input.AppliesProductIDs = []int64{}
	}
	if input.RequiresProductIDs == nil {
		input.RequiresProductIDs = []int64{}
	}
	start, expire, err := validPromoInput(&input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var id int64
	err = tx.QueryRow(r.Context(), `INSERT INTO promo_codes(code,discount_type,value,cycles,applies_product_ids,requires_product_ids,requires_existing,starts_at,expires_at,max_uses,one_time,only_new_customer,only_old_customer,once_per_customer,recurring,notes,created_by,recurring_limit,upgrade_only)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19) RETURNING id`, input.Code, input.DiscountType, input.Value, input.Cycles, input.AppliesProductIDs, input.RequiresProductIDs, input.RequiresExisting, start, expire, input.MaxUses, input.OneTime, input.OnlyNewCustomer, input.OnlyOldCustomer, input.OncePerCustomer, input.Recurring, input.Notes, identityFrom(r).ID, input.RecurringLimit, input.UpgradeOnly).Scan(&id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "promo_code.created", "promo_code", id, map[string]any{"code": input.Code, "discount_type": input.DiscountType}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusCreated, map[string]any{"id": id, "code": input.Code})
}

func (s *Server) updatePromoCode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Active    *bool   `json:"active"`
		StartsAt  *string `json:"starts_at"`
		ExpiresAt *string `json:"expires_at"`
		MaxUses   *int64  `json:"max_uses"`
		Notes     *string `json:"notes"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.MaxUses != nil && *input.MaxUses < 0 {
		writeError(w, 400, "invalid_promo", "最大使用次数无效")
		return
	}
	var start, expire *time.Time
	var err error
	if input.StartsAt != nil {
		start, err = parsePromoTime(*input.StartsAt)
		if err != nil {
			writeError(w, 400, "invalid_promo_time", "开始时间无效")
			return
		}
	}
	if input.ExpiresAt != nil {
		expire, err = parsePromoTime(*input.ExpiresAt)
		if err != nil {
			writeError(w, 400, "invalid_promo_time", "结束时间无效")
			return
		}
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var updated int64
	err = tx.QueryRow(r.Context(), "UPDATE promo_codes SET active=COALESCE($2,active),starts_at=COALESCE($3,starts_at),expires_at=COALESCE($4,expires_at),max_uses=COALESCE($5,max_uses),notes=COALESCE($6,notes),updated_at=now() WHERE id=$1 RETURNING id", id, input.Active, start, expire, input.MaxUses, input.Notes).Scan(&updated)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, 404, "not_found", "优惠码不存在")
			return
		}
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "promo_code.updated", "promo_code", id, map[string]any{}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"id": updated})
}
