package httpapi

import (
	"errors"
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
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var cartID string
		var version int64
		if err = tx.QueryRow(r.Context(), "SELECT id::text,version FROM carts WHERE user_id=$1 FOR UPDATE", identityFrom(r).ID).Scan(&cartID, &version); err != nil {
			return txResponse{}, err
		}
		if version != input.Version {
			return txResponse{}, &txHTTPError{http.StatusConflict, "stale_cart", "购物车已更新，请刷新后操作"}
		}
		quote, _, err := s.db.CartPromoQuote(r.Context(), tx, identityFrom(r).ID, cartID, input.Code, false)
		if err != nil {
			return txResponse{}, err
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO cart_promo_codes(cart_id,promo_code_id,code) VALUES($1,$2,$3)
ON CONFLICT(cart_id) DO UPDATE SET promo_code_id=EXCLUDED.promo_code_id,code=EXCLUDED.code,created_at=now()`, cartID, quote.ID, quote.Code); err != nil {
			return txResponse{}, err
		}
		if _, err = tx.Exec(r.Context(), "UPDATE carts SET version=version+1,updated_at=now() WHERE id=$1", cartID); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, map[string]any{"code": quote.Code, "discount_cents": quote.DiscountCents, "version": version + 1}}, nil
	})
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
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var cartID string
		var version int64
		if err = tx.QueryRow(r.Context(), "SELECT id::text,version FROM carts WHERE user_id=$1 FOR UPDATE", identityFrom(r).ID).Scan(&cartID, &version); err != nil {
			return txResponse{}, err
		}
		if version != input.Version {
			return txResponse{}, &txHTTPError{http.StatusConflict, "stale_cart", "购物车已更新，请刷新后操作"}
		}
		if _, err = tx.Exec(r.Context(), "DELETE FROM cart_promo_codes WHERE cart_id=$1", cartID); err != nil {
			return txResponse{}, err
		}
		if _, err = tx.Exec(r.Context(), "UPDATE carts SET version=version+1,updated_at=now() WHERE id=$1", cartID); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, map[string]any{"version": version + 1, "removed": true}}, nil
	})
}

type promoAdminInput = store.PromoAdminInput

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
	items, err := s.db.ListAdminPromoCodes(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, items)
}

func (s *Server) promoCustomerGroupsOptions(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 120 {
		writeError(w, 400, "invalid_query", "搜索条件过长")
		return
	}
	limit, offset := pagination(r)
	page, err := s.db.PromoCustomerGroupOptions(r.Context(), q, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, page)
}

// Both items and total use one statement snapshot, including an empty/out-of-range page.
func (s *Server) promoCodesPage(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 120 {
		writeError(w, 400, "invalid_query", "搜索条件过长")
		return
	}
	active := r.URL.Query().Get("active")
	if active == "" {
		active = "all"
	}
	if active != "all" && active != "active" && active != "inactive" {
		writeError(w, 400, "invalid_status", "优惠码状态筛选无效")
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "all"
	}
	switch status {
	case "all", "active", "expired", "disabled", "scheduled", "exhausted":
	default:
		writeError(w, 400, "invalid_status", "优惠码状态筛选无效")
		return
	}
	orderEligible := false
	if raw := r.URL.Query().Get("order_eligible"); raw != "" {
		var err error
		orderEligible, err = strconv.ParseBool(raw)
		if err != nil {
			writeError(w, 400, "invalid_filter", "订单优惠筛选无效")
			return
		}
	}
	limit, offset := pagination(r)
	page, err := s.db.PageAdminPromoCodes(r.Context(), store.PromoAdminFilter{Query: q, Active: active, Status: status, OrderEligible: orderEligible, Limit: limit, Offset: offset})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, page)
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
	out, err := s.db.CreateAdminPromoCode(requestFileContext(r), identityFrom(r).ID, r.Header.Get("Idempotency-Key"), input, start, expire)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusCreated, out)
}

type optionalPromoTime = store.OptionalPromoTime
type promoPatchInput = store.PromoPatchInput

func (s *Server) updatePromoCode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input promoPatchInput
	if !decode(w, r, &input) {
		return
	}
	if input.StartsAt.Invalid || input.ExpiresAt.Invalid {
		writeError(w, 400, "invalid_promo_time", "日期须为 RFC3339、空串或 null")
		return
	}
	if input.MaxUses != nil && *input.MaxUses < 0 || input.Notes != nil && len(*input.Notes) > 2000 || input.ExpectedVersion != nil && *input.ExpectedVersion < 1 {
		writeError(w, 400, "invalid_promo", "优惠码字段无效")
		return
	}
	out, err := s.db.UpdateAdminPromoCode(requestFileContext(r), identityFrom(r).ID, id, r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		var invalid *store.Conflict
		if errors.As(err, &invalid) && invalid.Code == "invalid_promo_time" {
			writeError(w, 400, invalid.Code, invalid.Message)
			return
		}
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
