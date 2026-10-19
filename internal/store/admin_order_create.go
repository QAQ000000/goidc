package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

type AdminOrderItemInput struct {
	ProductID            int64             `json:"product_id"`
	ExpectedRevision     int64             `json:"expected_revision"`
	BillingCycle         string            `json:"billing_cycle"`
	Quantity             int               `json:"quantity"`
	OptionIDs            []int64           `json:"option_ids"`
	OptionQuantities     map[int64]int64   `json:"option_quantities"`
	PurchaseFields       map[string]string `json:"purchase_fields"`
	FirstPriceOverride   *int64            `json:"first_price_override_cents"`
	RenewalPriceOverride *int64            `json:"renewal_price_override_cents"`
}
type AdminOrderInput struct {
	CustomerID        int64                 `json:"customer_id"`
	PaymentProvider   string                `json:"payment_provider"`
	ChannelInstanceID string                `json:"channel_instance_id"`
	Confirmed         bool                  `json:"confirmed"`
	GenerateInvoice   *bool                 `json:"generate_invoice"`
	SendEmail         bool                  `json:"send_email"`
	Notes             string                `json:"notes"`
	PromoCode         string                `json:"promo_code"`
	Items             []AdminOrderItemInput `json:"items"`
	QuoteHash         string                `json:"quote_hash"`
	paymentVersion    string
}
type AdminOrderQuoteLine struct {
	ProductID          int64                `json:"product_id"`
	ExpectedRevision   int64                `json:"expected_revision"`
	Name               string               `json:"name"`
	BillingCycle       string               `json:"billing_cycle"`
	Quantity           int                  `json:"quantity"`
	UnitFirstCents     int64                `json:"unit_first_cents"`
	UnitRecurringCents int64                `json:"unit_recurring_cents"`
	TotalCents         int64                `json:"total_cents"`
	ConfigSnapshot     []OptionSnapshot     `json:"config_snapshot"`
	Pricing            *PurchasePricingLine `json:"pricing,omitempty"`
}
type AdminOrderQuote struct {
	QuoteHash          string                `json:"quote_hash"`
	SubtotalCents      int64                 `json:"subtotal_cents"`
	DiscountCents      int64                 `json:"discount_cents"`
	GroupDiscountCents int64                 `json:"group_discount_cents"`
	PromoDiscountCents int64                 `json:"promo_discount_cents"`
	TotalCents         int64                 `json:"total_cents"`
	PromoCode          string                `json:"promo_code"`
	ReviewStatus       string                `json:"review_status"`
	GenerateInvoice    bool                  `json:"generate_invoice"`
	Items              []AdminOrderQuoteLine `json:"items"`
}
type AdminOrderResult struct {
	CheckoutResult
	Version         int64  `json:"version"`
	ReviewStatus    string `json:"review_status"`
	InvoiceDeferred bool   `json:"invoice_deferred"`
	EmailStatus     string `json:"email_status"`
}

func adminOrderHash(value any) string {
	body, _ := json.Marshal(value)
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
func normalizeAdminOrder(in AdminOrderInput) (AdminOrderInput, error) {
	in.PaymentProvider = strings.TrimSpace(in.PaymentProvider)
	in.ChannelInstanceID = strings.TrimSpace(in.ChannelInstanceID)
	in.PromoCode = strings.TrimSpace(in.PromoCode)
	in.Notes = strings.TrimSpace(in.Notes)
	if in.GenerateInvoice == nil {
		v := true
		in.GenerateInvoice = &v
	}
	if in.CustomerID < 1 || len(in.Items) == 0 || len(in.Items) > 100 || len(in.Notes) > 5000 || len(in.PromoCode) > 120 || len(in.PaymentProvider) > 40 || len(in.ChannelInstanceID) > 64 {
		return in, conflict("invalid_admin_order", "客户、产品数量或订单信息无效")
	}
	totalQuantity := 0
	for i := range in.Items {
		v := &in.Items[i]
		v.BillingCycle = strings.TrimSpace(v.BillingCycle)
		if v.ProductID < 1 || v.ExpectedRevision < 1 || v.Quantity < 1 || v.Quantity > 100 || len(v.OptionIDs) > 100 || len(v.OptionQuantities) > 100 || len(v.PurchaseFields) > 20 {
			return in, conflict("invalid_admin_order", "商品、版本、数量或配置无效")
		}
		totalQuantity += v.Quantity
		if totalQuantity > 100 {
			return in, conflict("invalid_admin_order", "每笔订单最多创建100项服务")
		}
		if !supplier.IsBillingCycle(v.BillingCycle) {
			return in, conflict("invalid_cycle", "账期无效")
		}
		for _, amount := range []*int64{v.FirstPriceOverride, v.RenewalPriceOverride} {
			if amount != nil && (*amount < 0 || *amount > 100000000) {
				return in, conflict("invalid_admin_price", "内部价格应为0至100000000整数分")
			}
		}
		if v.BillingCycle == "once" && v.RenewalPriceOverride != nil {
			return in, conflict("invalid_admin_price", "一次性商品不应设置续费价格")
		}
		// Explicit first purchase prices already include setup/configuration. Avoid
		// silently combining a negotiated price with a second coupon discount.
		if in.PromoCode != "" && (v.FirstPriceOverride != nil || v.RenewalPriceOverride != nil) {
			return in, conflict("override_promo_conflict", "内部价格覆盖与优惠码不能同时使用")
		}
	}
	return in, nil
}
func requireAdminOrdersManager(ctx context.Context, tx pgx.Tx, actor int64) error {
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, `SELECT role,admin_role_id FROM users WHERE id=$1 AND status='active' FOR SHARE`, actor).Scan(&role, &roleID); err != nil {
		return err
	}
	if role == "super_admin" {
		return nil
	}
	if role != "admin" || roleID == nil {
		return conflict("permission_denied", "没有管理订单的权限")
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT active AND 'orders.manage'=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE`, *roleID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return conflict("permission_denied", "没有管理订单的权限")
	}
	return nil
}
func lockActiveAdminOrderCustomer(ctx context.Context, tx pgx.Tx, actor, customer int64) error {
	if err := LockAdminCustomer(ctx, tx, actor, customer); err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRow(ctx, "SELECT status='active' FROM users WHERE id=$1 FOR SHARE", customer).Scan(&active); err != nil {
		return err
	}
	if !active {
		return conflict("customer_inactive", "客户已停用，不能创建订单")
	}
	return nil
}
func (s *Store) loadAdminCheckoutLines(ctx context.Context, tx pgx.Tx, in AdminOrderInput) ([]checkoutLine, error) {
	if err := GroupPricingReadLock(ctx, tx); err != nil {
		return nil, err
	}
	if err := CatalogReadLock(ctx, tx); err != nil {
		return nil, err
	}
	ids := []int64{}
	for _, line := range in.Items {
		ids = append(ids, line.ProductID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	// All product rows are locked in the same order as customer checkout.
	rows, err := tx.Query(ctx, "SELECT id FROM products WHERE id=ANY($1) ORDER BY id FOR UPDATE", ids)
	if err != nil {
		return nil, err
	}
	_, err = pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	items := make([]checkoutLine, 0, len(in.Items))
	for _, line := range in.Items {
		v := checkoutLine{ProductID: line.ProductID, Revision: line.ExpectedRevision, Qty: line.Quantity, Cycle: line.BillingCycle, FirstPriceOverride: line.FirstPriceOverride, RenewalPriceOverride: line.RenewalPriceOverride}
		if err = tx.QueryRow(ctx, `SELECT p.revision,p.name,pp.price_cents,pp.setup_fee_cents,p.stock,(p.active AND catalog_category_visible(p.category_id)),pp.active,p.approval_required FROM products p JOIN product_prices pp ON pp.product_id=p.id AND pp.billing_cycle=$2 WHERE p.id=$1 FOR UPDATE OF pp`, line.ProductID, line.BillingCycle).Scan(&v.CurrentRevision, &v.Name, &v.Price, &v.Setup, &v.Stock, &v.Active, &v.PriceActive, &v.ApprovalRequired); err != nil {
			return nil, err
		}
		v.OptionJSON, _ = json.Marshal(line.OptionIDs)
		if line.OptionIDs == nil {
			v.OptionJSON = []byte("[]")
		}
		v.OptionQuantityJSON, _ = json.Marshal(line.OptionQuantities)
		if line.OptionQuantities == nil {
			v.OptionQuantityJSON = []byte("{}")
		}
		v.PurchaseJSON, _ = json.Marshal(line.PurchaseFields)
		if line.PurchaseFields == nil {
			v.PurchaseJSON = []byte("{}")
		}
		items = append(items, v)
	}
	return items, nil
}
func buildAdminOrderQuote(in AdminOrderInput, lines []checkoutLine, result CheckoutResult, requiresApproval bool, benefits []RenewalBenefit) AdminOrderQuote {
	out := AdminOrderQuote{SubtotalCents: result.TotalCents + result.DiscountCents, DiscountCents: result.DiscountCents, GroupDiscountCents: result.GroupDiscountCents, PromoDiscountCents: result.PromoDiscountCents, TotalCents: result.TotalCents, PromoCode: result.PromoCode, ReviewStatus: "approved", GenerateInvoice: *in.GenerateInvoice, Items: []AdminOrderQuoteLine{}}
	if requiresApproval && !in.Confirmed {
		out.ReviewStatus = "pending"
	}
	for _, v := range lines {
		recurring := v.UnitPrice
		if v.Pricing != nil {
			recurring = v.Pricing.RecurringCents
		}
		if v.FirstPriceOverride != nil {
			recurring = v.DefaultRecurring
		}
		if v.RenewalPriceOverride != nil {
			recurring = *v.RenewalPriceOverride
		}
		if v.Cycle == "once" {
			recurring = 0
		}
		first, total := v.UnitPrice+v.UnitSetup, (v.UnitPrice+v.UnitSetup)*int64(v.Qty)
		if v.Pricing != nil {
			first = (v.Pricing.GrossCents - v.Pricing.GroupDiscountCents) / int64(v.Qty)
			total = v.Pricing.NetCents
		}
		out.Items = append(out.Items, AdminOrderQuoteLine{v.ProductID, v.CurrentRevision, v.Name, v.Cycle, v.Qty, first, recurring, total, v.Snapshot, v.Pricing})
	}
	// Hash the request and complete authoritative snapshots, including purchase
	// field definitions and default renewal benefit terms. No plaintext is returned.
	in.QuoteHash = ""
	out.QuoteHash = adminOrderHash(struct {
		Input          AdminOrderInput
		Quote          AdminOrderQuote
		Fields         [][]PurchaseFieldSnapshot
		PaymentVersion string
		Benefits       []RenewalBenefit
	}{in, out, func() [][]PurchaseFieldSnapshot {
		result := make([][]PurchaseFieldSnapshot, len(lines))
		for i := range lines {
			result[i] = lines[i].PurchaseSnapshot
		}
		return result
	}(), in.paymentVersion, benefits})
	return out
}
func (s *Store) QuoteAdminOrder(ctx context.Context, actor int64, input AdminOrderInput, fallback PaymentConfiguration) (AdminOrderQuote, error) {
	var out AdminOrderQuote
	in, err := normalizeAdminOrder(input)
	if err != nil {
		return out, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = requireAdminOrdersManager(ctx, tx, actor); err != nil {
		return out, err
	}
	if err = lockActiveAdminOrderCustomer(ctx, tx, actor, in.CustomerID); err != nil {
		return out, err
	}
	in.paymentVersion, err = s.checkAdminOrderPayment(ctx, tx, in, fallback)
	if err != nil {
		return out, err
	}
	lines, err := s.loadAdminCheckoutLines(ctx, tx, in)
	if err != nil {
		return out, err
	}
	result, benefits, required, err := s.prepareCheckoutLines(ctx, tx, in.CustomerID, lines, in.PromoCode)
	if err != nil {
		return out, err
	}
	return buildAdminOrderQuote(in, lines, result, required, benefits), tx.Commit(ctx)
}
func (s *Store) CreateAdminOrder(ctx context.Context, actor int64, key string, input AdminOrderInput, fallback PaymentConfiguration) (AdminOrderResult, error) {
	var out AdminOrderResult
	in, err := normalizeAdminOrder(input)
	if err != nil {
		return out, err
	}
	if len(key) < 8 || len(key) > 128 || len(in.QuoteHash) != 64 {
		return out, conflict("invalid_admin_order", "请先报价并提供有效幂等键")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = requireAdminOrdersManager(ctx, tx, actor); err != nil {
		return out, err
	}
	if err = lockActiveAdminOrderCustomer(ctx, tx, actor, in.CustomerID); err != nil {
		return out, err
	}
	requestHash := adminOrderHash(in)
	if replayed, err := ReplayOperation(ctx, tx, actor, "admin:order.create", key, requestHash, &out); err != nil {
		return out, err
	} else if replayed {
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	in.paymentVersion, err = s.checkAdminOrderPayment(ctx, tx, in, fallback)
	if err != nil {
		return out, err
	}
	lines, err := s.loadAdminCheckoutLines(ctx, tx, in)
	if err != nil {
		return out, err
	}
	result, benefits, required, err := s.prepareCheckoutLines(ctx, tx, in.CustomerID, lines, in.PromoCode)
	if err != nil {
		return out, err
	}
	quote := buildAdminOrderQuote(in, lines, result, required, benefits)
	if quote.QuoteHash != in.QuoteHash {
		return out, conflict("stale_quote", "价格、配置、库存或客户信息已变化，请重新报价确认")
	}
	result, err = s.persistCheckoutLines(ctx, tx, in.CustomerID, "admin:"+fmt.Sprint(actor)+":"+key, requestHash, lines, result, benefits, required, result.promoID, checkoutOptions{GenerateInvoice: *in.GenerateInvoice, Confirmed: in.Confirmed, ActorID: actor, Source: "admin"})
	if err != nil {
		return out, err
	}
	out = AdminOrderResult{CheckoutResult: result, ReviewStatus: quote.ReviewStatus, InvoiceDeferred: !*in.GenerateInvoice, EmailStatus: "not_requested"}
	if in.SendEmail {
		out.EmailStatus, err = s.queueAdminOrderEmail(ctx, tx, in.CustomerID, result.OrderNo, result.TotalCents, result.InvoiceID)
		if err != nil {
			return out, err
		}
	}
	err = tx.QueryRow(ctx, `UPDATE orders SET created_by=$2,payment_provider=$3,payment_channel_instance_id=NULLIF($4,''),invoice_deferred=$5,send_order_email=$6,email_status=$7,notes=$8 WHERE id=$1 RETURNING version`, out.OrderID, actor, in.PaymentProvider, in.ChannelInstanceID, out.InvoiceDeferred, in.SendEmail, out.EmailStatus, in.Notes).Scan(&out.Version)
	if err != nil {
		return out, err
	}
	if err = SaveOperation(ctx, tx, actor, "admin:order.create", key, requestHash, out, OperationReferences{CustomerID: in.CustomerID, OrderID: out.OrderID}); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, "order.admin_created", "order", out.OrderID, map[string]any{"customer_id": in.CustomerID, "invoice_deferred": out.InvoiceDeferred, "payment_provider": in.PaymentProvider, "channel_instance_id": in.ChannelInstanceID, "confirmed": in.Confirmed, "send_email": in.SendEmail, "email_status": out.EmailStatus}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) queueAdminOrderEmail(ctx context.Context, tx pgx.Tx, customer int64, orderNo string, amount, invoice int64) (string, error) {
	var recipient string
	var verified bool
	if err := tx.QueryRow(ctx, "SELECT email,email_verified FROM users WHERE id=$1", customer).Scan(&recipient, &verified); err != nil {
		return "", err
	}
	if !verified || recipient == "" {
		return "skipped_unverified", nil
	}
	var subject, body, instance string
	var active bool
	err := tx.QueryRow(ctx, "SELECT subject,body,active FROM notification_templates WHERE channel='email' AND kind='order_created'").Scan(&subject, &body, &active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		return "skipped_template", nil
	}
	if err != nil {
		return "", err
	}
	err = tx.QueryRow(ctx, "SELECT id FROM channel_instances WHERE category='mail' AND enabled ORDER BY sort_order,id LIMIT 1").Scan(&instance)
	if errors.Is(err, pgx.ErrNoRows) {
		return "skipped_channel", nil
	}
	if err != nil {
		return "", err
	}
	message := "账单将在管理员确认后生成。"
	if invoice > 0 {
		message = "账单已生成，请登录客户中心查看并支付。"
	}
	replacer := strings.NewReplacer("{{order_no}}", orderNo, "{{amount}}", fmt.Sprintf("¥%d.%02d", amount/100, amount%100), "{{invoice_message}}", message)
	if _, err = tx.Exec(ctx, "INSERT INTO email_outbox(recipient,subject,body,kind,channel_instance_id) VALUES($1,$2,$3,'order_created',$4)", recipient, replacer.Replace(subject), replacer.Replace(body), instance); err != nil {
		return "", err
	}
	return "queued", nil
}

// Payment is a preference for the eventual customer checkout, never a captured
// charge. Take configuration row locks and bind its versions into the quote.
func (s *Store) checkAdminOrderPayment(ctx context.Context, tx pgx.Tx, in AdminOrderInput, fallback PaymentConfiguration) (string, error) {
	if in.PaymentProvider == "" {
		if in.ChannelInstanceID != "" {
			return "", conflict("invalid_order_payment", "未指定付款方式不能绑定渠道")
		}
		return "", nil
	}
	if in.ChannelInstanceID == "" {
		return "", conflict("invalid_order_payment", "请选择支付渠道实例")
	}
	legacyInstance := in.ChannelInstanceID == "payment-default" || in.ChannelInstanceID == "payment-mock-default"
	// The legacy singleton is authoritative and synchronizes with channel writes.
	// Lock only that authority for legacy instances: generic channel writes take
	// the channel first, so also locking both rows here could deadlock.
	if legacyInstance {
		var id int
		if err := tx.QueryRow(ctx, "SELECT id FROM payment_settings WHERE id=1 FOR SHARE").Scan(&id); err != nil {
			return "", err
		}
	}
	configuration, err := s.PaymentConfigurationForInstanceTx(ctx, tx, in.ChannelInstanceID, fallback)
	if err != nil {
		return "", err
	}
	singletonVersion := int64(0)
	if configuration.Runtime != nil && configuration.Runtime.Legacy {
		singletonVersion = configuration.Runtime.SingletonVersion
	}
	owner, known := payment.MethodOwner(in.PaymentProvider)
	if !known || owner != configuration.ImplementationID() {
		return "", conflict("invalid_order_payment", "付款方式与渠道不匹配")
	}
	if _, ok := configuration.Gateway(in.PaymentProvider, false); !ok {
		return "", conflict("payment_provider_disabled", "付款方式已停用或配置不完整")
	}
	version := configuration.Version
	return fmt.Sprintf("%d:%d", version, singletonVersion), nil
}

type DeferredOrderInvoiceResult struct {
	OrderID   int64 `json:"order_id"`
	InvoiceID int64 `json:"invoice_id"`
	Version   int64 `json:"version"`
	Replayed  bool  `json:"replayed"`
}

func (s *Store) CreateDeferredOrderInvoice(ctx context.Context, actor, id, expected int64, key string) (DeferredOrderInvoiceResult, error) {
	var out DeferredOrderInvoiceResult
	if expected < 1 {
		return out, conflict("order_version_required", "请提交当前订单版本")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = requireAdminOrdersManager(ctx, tx, actor); err != nil {
		return out, err
	}
	if err = LockAdminResourceCustomer(ctx, tx, actor, "order", id); err != nil {
		return out, err
	}
	hash, replay, err := orderOperationStart(ctx, tx, actor, id, "invoice", key, expected, &out)
	if err != nil {
		return out, err
	}
	if replay {
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	// Existing invoices are locked before orders, matching cancellation/payment.
	var existingInvoice int64
	err = tx.QueryRow(ctx, "SELECT id FROM invoices WHERE order_id=$1 FOR UPDATE", id).Scan(&existingInvoice)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var version, customer, amount int64
	var deferred, deleted bool
	var status string
	if err = tx.QueryRow(ctx, "SELECT version,user_id,total_cents,invoice_deferred,deleted_at IS NOT NULL,status FROM orders WHERE id=$1 FOR UPDATE", id).Scan(&version, &customer, &amount, &deferred, &deleted, &status); err != nil {
		return out, err
	}
	if err = checkOrderVersion(version, []int64{expected}); err != nil {
		return out, err
	}
	if deleted || status != "pending" || !deferred || existingInvoice > 0 {
		return out, conflict("order_invoice_not_deferred", "该订单当前不能生成延期账单")
	}
	out.OrderID = id
	if err = tx.QueryRow(ctx, "INSERT INTO invoices(invoice_no,user_id,order_id,amount_cents) VALUES('I-'||replace(gen_random_uuid()::text,'-',''),$1,$2,$3) RETURNING id", customer, id, amount).Scan(&out.InvoiceID); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, "UPDATE orders SET invoice_deferred=false WHERE id=$1 RETURNING version", id).Scan(&out.Version); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, "order.invoice_generated", "order", id, map[string]any{"invoice_id": out.InvoiceID, "expected_version": expected}); err != nil {
		return out, err
	}
	if err = saveOrderOperation(ctx, tx, actor, id, "invoice", key, hash, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
