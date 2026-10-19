package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/codex/zjmfgo/internal/correlation"
	"github.com/jackc/pgx/v5"
)

type Conflict struct{ Code, Message string }

func (e *Conflict) Error() string         { return e.Message }
func conflict(code, message string) error { return &Conflict{code, message} }

type CheckoutInput struct {
	CartVersion int64   `json:"cart_version"`
	ItemIDs     []int64 `json:"item_ids"`
}
type CheckoutResult struct {
	OrderID       int64  `json:"order_id"`
	InvoiceID     int64  `json:"invoice_id"`
	OrderNo       string `json:"order_no"`
	TotalCents    int64  `json:"total_cents"`
	DiscountCents int64  `json:"discount_cents"`
	PromoCode     string `json:"promo_code,omitempty"`
	Replayed      bool   `json:"replayed"`
	promoID       int64
}

func (s *Store) Checkout(ctx context.Context, userID int64, key string, input CheckoutInput) (CheckoutResult, error) {
	var result CheckoutResult
	if len(key) < 8 || len(key) > 128 || input.CartVersion < 1 || len(input.ItemIDs) == 0 || len(input.ItemIDs) > 100 {
		return result, conflict("invalid_checkout", "请选择购物车项目并提供有效版本与幂等键")
	}
	ids := append([]int64(nil), input.ItemIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i, id := range ids {
		if id < 1 || (i > 0 && ids[i-1] == id) {
			return result, conflict("invalid_selection", "购物车项目无效或重复")
		}
	}
	input.ItemIDs = ids
	b, _ := json.Marshal(input)
	sum := sha256.Sum256(b)
	requestHash := hex.EncodeToString(sum[:])
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	var cartID string
	var version int64
	if err = tx.QueryRow(ctx, "SELECT id::text,version FROM carts WHERE user_id=$1 FOR UPDATE", userID).Scan(&cartID, &version); err != nil {
		return result, err
	}
	var savedHash string
	err = tx.QueryRow(ctx, "SELECT o.id,i.id,o.order_no,o.total_cents,o.request_hash FROM orders o JOIN invoices i ON i.order_id=o.id WHERE o.user_id=$1 AND o.idempotency_key=$2", userID, key).Scan(&result.OrderID, &result.InvoiceID, &result.OrderNo, &result.TotalCents, &savedHash)
	if err == nil {
		if savedHash != requestHash {
			return result, conflict("idempotency_conflict", "相同幂等键对应不同的请求")
		}
		result.Replayed = true
		return result, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	if version != input.CartVersion {
		return result, conflict("stale_cart", "购物车已发生变化，请重新确认")
	}
	var promoCode string
	if err = tx.QueryRow(ctx, "SELECT code FROM cart_promo_codes WHERE cart_id=$1", cartID).Scan(&promoCode); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		promoCode = ""
	}
	if err = CatalogReadLock(ctx, tx); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT ci.id,ci.product_id,ci.quantity,ci.product_revision,p.revision,p.name,ci.billing_cycle,ci.option_ids,ci.option_quantities,pp.price_cents,pp.setup_fee_cents,p.stock,(p.active AND catalog_category_visible(p.category_id)),pp.active,p.approval_required,ci.purchase_fields
 FROM cart_items ci JOIN products p ON p.id=ci.product_id JOIN product_prices pp ON pp.product_id=ci.product_id AND pp.billing_cycle=ci.billing_cycle WHERE ci.cart_id=$1 AND ci.id=ANY($2) ORDER BY p.id FOR UPDATE OF p,ci,pp`, cartID, ids)
	if err != nil {
		return result, err
	}
	items := []checkoutLine{}
	for rows.Next() {
		var v checkoutLine
		if err = rows.Scan(&v.ID, &v.ProductID, &v.Qty, &v.Revision, &v.CurrentRevision, &v.Name, &v.Cycle, &v.OptionJSON, &v.OptionQuantityJSON, &v.Price, &v.Setup, &v.Stock, &v.Active, &v.PriceActive, &v.ApprovalRequired, &v.PurchaseJSON); err != nil {
			rows.Close()
			return result, err
		}
		v.StoredFields = true
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(items) != len(ids) {
		return result, conflict("stale_selection", "选中的购物车项目不存在")
	}
	result, renewalBenefits, requiresApproval, err := s.prepareCheckoutLines(ctx, tx, userID, items, promoCode)
	if err != nil {
		return result, err
	}
	result, err = s.persistCheckoutLines(ctx, tx, userID, key, requestHash, items, result, renewalBenefits, requiresApproval, result.promoID, checkoutOptions{GenerateInvoice: true, ActorID: userID, Source: "customer", ItemIDs: ids})
	if err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM cart_items WHERE cart_id=$1 AND id=ANY($2)", cartID, ids); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, "UPDATE carts SET version=version+1,updated_at=now() WHERE id=$1", cartID); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func nullableInt64(value int64) any {
	if value < 1 {
		return nil
	}
	return value
}

func Audit(ctx context.Context, tx pgx.Tx, actor int64, action, entity string, id int64, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var actorValue any
	if actor > 0 {
		actorValue = actor
	}
	if correlation.ID(ctx) == "" {
		// Preserve the original writer for genuine predecessor-schema fixtures.
		// On the current schema the lease-scoped DEFAULT still records an
		// origin inherited from the transaction's acquisition context.
		_, err = tx.Exec(ctx, "INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload) VALUES($1,$2,$3,$4,$5)", actorValue, action, entity, fmt.Sprint(id), b)
	} else {
		_, err = tx.Exec(ctx, "INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload,correlation_id) VALUES($1,$2,$3,$4,$5,$6)", actorValue, action, entity, fmt.Sprint(id), b, correlation.ID(ctx))
	}
	return err
}
