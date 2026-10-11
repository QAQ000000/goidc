package httpapi

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func adminOrderDraft(t *testing.T, f mutationOrderFixture) store.AdminOrderInput {
	t.Helper()
	return store.AdminOrderInput{CustomerID: f.customerID, Items: []store.AdminOrderItemInput{{ProductID: f.productID, ExpectedRevision: *configRevision(t, f.h, f.root, f.productID), BillingCycle: "monthly", Quantity: 1}}}
}
func adminOrderQuoted(t *testing.T, f mutationOrderFixture, in store.AdminOrderInput, cookie string) store.AdminOrderInput {
	t.Helper()
	w := request(f.h, "POST", "/api/v1/admin/orders/quote", in, cookie, "")
	expect(t, w, 200)
	in.QuoteHash = data(t, w)["quote_hash"].(string)
	return in
}

func TestAdminOrderCreateIndependentCartQuoteAndReplay(t *testing.T) {
	f := newMutationOrderFixture(t)
	cartVersion, cartItem := add(t, f.h, f.customer, f.productID, 1)
	in := adminOrderDraft(t, f)
	in.Items[0].Quantity = 2
	in.Notes = "管理员创建，待客户付款"
	in.PaymentProvider = "mock"
	in.ChannelInstanceID = "payment-mock-default"
	in = adminOrderQuoted(t, f, in, f.root)
	w := request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-create-independent")
	expect(t, w, 201)
	d := data(t, w)
	if d["total_cents"] != float64(2666) || d["invoice_id"].(float64) < 1 || d["review_status"] != "approved" {
		t.Fatal(d)
	}
	id := int64(d["order_id"].(float64))
	replay := request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-create-independent")
	expect(t, replay, 200)
	if data(t, replay)["replayed"] != true || data(t, replay)["order_id"] != d["order_id"] {
		t.Fatal("incorrect replay")
	}
	changed := in
	changed.Notes = "changed"
	expect(t, request(f.h, "POST", "/api/v1/admin/orders", changed, f.root, "admin-create-independent"), 409)
	v, item := cart(t, f.h, f.customer)
	if v != cartVersion || item != cartItem {
		t.Fatal("admin changed customer cart", v, item)
	}
	var stock, services, jobs, payments, receipts, audits int
	var status, serviceStatus, notes, provider, instance string
	err := f.db.Pool.QueryRow(t.Context(), `SELECT p.stock,o.status,o.notes,o.payment_provider,o.payment_channel_instance_id,
 (SELECT count(*) FROM services WHERE order_id=o.id),
 (SELECT min(status) FROM services WHERE order_id=o.id),
 (SELECT count(*) FROM provision_jobs), (SELECT count(*) FROM payment_transactions),
 (SELECT count(*) FROM admin_order_creation_receipts),
 (SELECT count(*) FROM audit_events WHERE action='order.admin_created') FROM orders o CROSS JOIN products p WHERE o.id=$1 AND p.id=$2`, id, f.productID).Scan(&stock, &status, &notes, &provider, &instance, &services, &serviceStatus, &jobs, &payments, &receipts, &audits)
	if err != nil {
		t.Fatal(err)
	}
	if stock != 10 || status != "pending" || serviceStatus != "pending_payment" || services != 2 || jobs != 0 || payments != 0 || receipts != 1 || audits != 1 || notes != in.Notes || provider != "mock" || instance != "payment-mock-default" {
		t.Fatal("invalid admin-created state", stock, status, services, serviceStatus, jobs, payments, receipts, audits, notes, provider, instance)
	}
}

func TestAdminOrderCreatePriceOverridesReviewAndDeferredInvoice(t *testing.T) {
	f := newMutationOrderFixture(t)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET approval_required=true WHERE id=$1", f.productID); err != nil {
		t.Fatal(err)
	}
	in := adminOrderDraft(t, f)
	first := int64(700)
	in.Items[0].FirstPriceOverride = &first
	generateInvoice := false
	in.GenerateInvoice = &generateInvoice
	quote := request(f.h, "POST", "/api/v1/admin/orders/quote", in, f.root, "")
	expect(t, quote, 200)
	q := data(t, quote)
	line := q["items"].([]any)[0].(map[string]any)
	if q["review_status"] != "pending" || q["total_cents"] != float64(700) || line["unit_recurring_cents"] != float64(1234) {
		t.Fatal(q)
	}
	in.QuoteHash = q["quote_hash"].(string)
	w := request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-defer-create")
	expect(t, w, 201)
	d := data(t, w)
	id := int64(d["order_id"].(float64))
	if d["invoice_id"] != float64(0) || d["invoice_deferred"] != true || d["review_status"] != "pending" {
		t.Fatal(d)
	}
	var recurring, storedFirst int64
	var invoiceCount int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT s.recurring_price_cents,oi.first_price_override_cents,(SELECT count(*) FROM invoices WHERE order_id=$1) FROM services s JOIN order_items oi ON oi.id=s.order_item_id WHERE s.order_id=$1`, id).Scan(&recurring, &storedFirst, &invoiceCount); err != nil {
		t.Fatal(err)
	}
	if recurring != 1234 || storedFirst != 700 || invoiceCount != 0 {
		t.Fatal("override/defer invalid", recurring, storedFirst, invoiceCount)
	}
	path := fmt.Sprintf("/api/v1/admin/orders/%d/invoice", id)
	v := int64(d["version"].(float64))
	expect(t, request(f.h, "POST", path, map[string]any{"expected_version": v - 1}, f.root, "admin-defer-stale"), 409)
	body := map[string]any{"expected_version": v}
	invoice := request(f.h, "POST", path, body, f.root, "admin-defer-invoice")
	expect(t, invoice, 201)
	next := data(t, invoice)
	if next["invoice_id"].(float64) < 1 || next["version"] != float64(v+1) {
		t.Fatal(next)
	}
	expect(t, request(f.h, "POST", path, body, f.root, "admin-defer-invoice"), 200)
	expect(t, request(f.h, "POST", path, map[string]any{"expected_version": v + 1}, f.root, "admin-defer-another"), 409)
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoices WHERE order_id=$1", id).Scan(&invoiceCount); err != nil || invoiceCount != 1 {
		t.Fatal("duplicate invoices", invoiceCount, err)
	}
	// Confirmation removes manual review only; it cannot pay an invoice or queue provisioning.
	in.Confirmed = true
	in.GenerateInvoice = enabled()
	renewal := int64(500)
	in.Items[0].RenewalPriceOverride = &renewal
	in = adminOrderQuoted(t, f, in, f.root)
	confirmed := request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-confirmed-create")
	expect(t, confirmed, 201)
	if data(t, confirmed)["review_status"] != "approved" {
		t.Fatal("confirmed review ignored")
	}
	var invoiceStatus, serviceStatus string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT i.status,s.status,s.recurring_price_cents FROM invoices i JOIN services s ON s.order_id=i.order_id WHERE i.id=$1`, int64(data(t, confirmed)["invoice_id"].(float64))).Scan(&invoiceStatus, &serviceStatus, &recurring); err != nil {
		t.Fatal(err)
	}
	if invoiceStatus != "unpaid" || serviceStatus != "pending_payment" || recurring != 500 {
		t.Fatal("confirmation fabricated payment", invoiceStatus, serviceStatus, recurring)
	}
}

func TestAdminOrderCreatePermissionsScopeAndActiveCustomer(t *testing.T) {
	f := newMutationOrderFixture(t)
	in := adminOrderDraft(t, f)
	contentRole(t, f.db, f.staffID, "orders.view")
	for _, path := range []string{"/api/v1/admin/orders/quote", "/api/v1/admin/orders"} {
		expect(t, request(f.h, "POST", path, in, f.staff, "admin-denied-create"), 403)
	}
	expect(t, request(f.h, "GET", "/api/v1/admin/orders/create-options?kind=customers", nil, f.staff, ""), 403)
	contentRole(t, f.db, f.staffID, "orders.manage")
	// Creating an order must not require customer/product view rights.
	expect(t, request(f.h, "GET", "/api/v1/admin/orders/create-options?kind=products&limit=1", nil, f.staff, ""), 200)
	expect(t, request(f.h, "GET", "/api/v1/admin/products", nil, f.staff, ""), 403)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.staffID); err != nil {
		t.Fatal(err)
	}
	empty := request(f.h, "GET", "/api/v1/admin/orders/create-options?kind=customers", nil, f.staff, "")
	expect(t, empty, 200)
	if data(t, empty)["total"] != float64(0) {
		t.Fatal("scope options leaked")
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/orders/quote", in, f.staff, ""), 404)
	contentRole(t, f.db, f.staffID, "orders.manage", "customers.view")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true WHERE id=$1", f.staffID); err != nil {
		t.Fatal(err)
	}
	salesAssignment(t, f.h, f.root, f.customerID, f.staffID, 1)
	contentRole(t, f.db, f.staffID, "orders.manage")
	in = adminOrderQuoted(t, f, in, f.staff)
	created := request(f.h, "POST", "/api/v1/admin/orders", in, f.staff, "admin-scope-create")
	expect(t, created, 201)
	salesAssignment(t, f.h, f.root, f.customerID, nil, 2)
	expect(t, request(f.h, "POST", "/api/v1/admin/orders", in, f.staff, "admin-scope-create"), 404)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", f.customerID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/orders/quote", in, f.root, ""), 409)
	expect(t, request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-inactive-create"), 409)
}

func TestAdminOrderCreateStaleQuoteStockAndPaymentConfiguration(t *testing.T) {
	f := newMutationOrderFixture(t)
	in := adminOrderQuoted(t, f, adminOrderDraft(t, f), f.root)
	changed := in
	changed.Notes = "new notes"
	expect(t, request(f.h, "POST", "/api/v1/admin/orders", changed, f.root, "admin-stale-request"), 409)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE product_prices SET price_cents=price_cents+1 WHERE product_id=$1", f.productID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-stale-price"), 409)
	in = adminOrderDraft(t, f)
	expect(t, request(f.h, "POST", "/api/v1/admin/channel-instances", selectedPaymentInstanceInput("https://payments.example.test"), f.root, ""), 201)
	in.PaymentProvider = "epay_alipay"
	in.ChannelInstanceID = selectedPaymentInstanceID
	in = adminOrderQuoted(t, f, in, f.root)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE channel_instances SET version=version+1 WHERE id=$1", selectedPaymentInstanceID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-stale-payment"), 409)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE channel_instances SET enabled=false,version=version+1 WHERE id=$1", selectedPaymentInstanceID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/orders/quote", in, f.root, ""), 409)
	in.PaymentProvider = ""
	in.ChannelInstanceID = ""
	in.Items = append(in.Items, in.Items[0])
	in.Items[0].Quantity = 7
	in.Items[1].Quantity = 7
	expect(t, request(f.h, "POST", "/api/v1/admin/orders/quote", in, f.root, ""), 409)
	in.Items[0].Quantity = 100
	in.Items[1].Quantity = 1
	expect(t, request(f.h, "POST", "/api/v1/admin/orders/quote", in, f.root, ""), 409)
	var count, stock int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM orders),stock FROM products WHERE id=$1", f.productID).Scan(&count, &stock); err != nil || count != 0 || stock != 12 {
		t.Fatal("failed quote/create changed state", count, stock, err)
	}
}

func TestAdminOrderCreateSensitiveQuoteSnapshotAndCoupon(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	f.db.PurchaseFieldEncryptionKey = []byte("01234567890123456789012345678901")
	savePurchaseDefinitions(t, f, []store.PurchaseField{{Key: "secret", Label: "密码", Type: "text", Active: true, Required: true, Encrypted: true, MaxLength: 255}})
	in := store.AdminOrderInput{CustomerID: f.uid, Items: []store.AdminOrderItemInput{{ProductID: f.pid, ExpectedRevision: *configRevision(t, f.h, f.root, f.pid), BillingCycle: "monthly", Quantity: 1, OptionIDs: []int64{f.small}, PurchaseFields: map[string]string{"secret": "admin-test-private-value"}}}}
	expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "ADMIN10", "discount_type": "percent", "value": 10, "max_uses": 1}, f.root, ""), 201)
	in.PromoCode = "admin10"
	quote := request(f.h, "POST", "/api/v1/admin/orders/quote", in, f.root, "")
	expect(t, quote, 200)
	if strings.Contains(quote.Body.String(), "admin-test-private-value") || strings.Contains(quote.Body.String(), "enc:v2:") {
		t.Fatal("quote leaked sensitive fields")
	}
	in.QuoteHash = data(t, quote)["quote_hash"].(string)
	quote2 := request(f.h, "POST", "/api/v1/admin/orders/quote", in, f.root, "")
	expect(t, quote2, 200)
	if data(t, quote2)["quote_hash"] != in.QuoteHash {
		t.Fatal("encrypted field changed hash")
	}
	created := request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-encrypted-coupon")
	expect(t, created, 201)
	d := data(t, created)
	if d["discount_cents"] != data(t, quote)["discount_cents"] || d["promo_code"] != "ADMIN10" {
		t.Fatal("coupon mismatch", d)
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-encrypted-coupon"), 200)
	var snapshot string
	var used int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT purchase_fields::text,(SELECT used_count FROM promo_codes WHERE code='ADMIN10') FROM order_items WHERE order_id=$1", int64(d["order_id"].(float64))).Scan(&snapshot, &used); err != nil {
		t.Fatal(err)
	}
	if used != 1 || !strings.Contains(snapshot, "enc:v2:") || strings.Contains(snapshot, "admin-test-private-value") {
		t.Fatal("coupon/encrypted snapshot invalid", used, snapshot)
	}
	override := int64(100)
	in.Items[0].FirstPriceOverride = &override
	expect(t, request(f.h, "POST", "/api/v1/admin/orders/quote", in, f.root, ""), 409)
}

func TestAdminOrderCreateEmailQueueAndAuditRollback(t *testing.T) {
	f := newMutationOrderFixture(t)
	in := adminOrderDraft(t, f)
	in.SendEmail = true
	in = adminOrderQuoted(t, f, in, f.root)
	skipped := request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-email-unverified")
	expect(t, skipped, 201)
	if data(t, skipped)["email_status"] != "skipped_unverified" {
		t.Fatal("unverified email queued")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET email_verified=true WHERE id=$1", f.customerID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE channel_instances SET enabled=true WHERE id='mail-smtp-default'"); err != nil {
		t.Fatal(err)
	}
	in = adminOrderQuoted(t, f, in, f.root)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_admin_create_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='order.admin_created' THEN RAISE EXCEPTION 'injected admin order audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_admin_create_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_admin_create_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-email-audit"), 500)
	var stock, orders, outbox, receipts, services, invoices int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT stock,(SELECT count(*) FROM orders),(SELECT count(*) FROM email_outbox),(SELECT count(*) FROM admin_order_creation_receipts),(SELECT count(*) FROM services),(SELECT count(*) FROM invoices) FROM products WHERE id=$1", f.productID).Scan(&stock, &orders, &outbox, &receipts, &services, &invoices); err != nil {
		t.Fatal(err)
	}
	if stock != 11 || orders != 1 || outbox != 0 || receipts != 1 || services != 1 || invoices != 1 {
		t.Fatal("audit failure left partial create", stock, orders, outbox, receipts, services, invoices)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER reject_admin_create_audit ON audit_events"); err != nil {
		t.Fatal(err)
	}
	queued := request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-email-audit")
	expect(t, queued, 201)
	if data(t, queued)["email_status"] != "queued" {
		t.Fatal("email not queued", queued.Body.String())
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-email-audit"), 200)
	var body, channel string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*),min(body),min(channel_instance_id) FROM email_outbox WHERE kind='order_created'").Scan(&outbox, &body, &channel); err != nil {
		t.Fatal(err)
	}
	if outbox != 1 || channel != "mail-smtp-default" || !strings.Contains(body, data(t, queued)["order_no"].(string)) || strings.Contains(body, "{{") {
		t.Fatal("invalid outbox", outbox, body, channel)
	}
}

func TestAdminOrderCreateConcurrentIdempotencyAndStock(t *testing.T) {
	f := newMutationOrderFixture(t)
	in := adminOrderDraft(t, f)
	in.Items[0].Quantity = 7
	in = adminOrderQuoted(t, f, in, f.root)
	responses := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-concurrent-same")
		}()
	}
	wg.Wait()
	close(responses)
	created, replayed := 0, 0
	for w := range responses {
		switch w.Code {
		case 201:
			created++
		case 200:
			replayed++
		default:
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if created != 1 || replayed != 1 {
		t.Fatal("duplicate submission", created, replayed)
	}
	// A distinct request cannot oversell after the first reservation.
	expect(t, request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-concurrent-other"), 409)
	var stock, count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT stock,(SELECT count(*) FROM orders) FROM products WHERE id=$1", f.productID).Scan(&stock, &count); err != nil || stock != 5 || count != 1 {
		t.Fatal("oversell", stock, count, err)
	}
}

func TestAdminOrderCreateOptionsPaginationAndNoOpaqueMetadata(t *testing.T) {
	f := newMutationOrderFixture(t)
	user(t, f.db, f.h, "another-active-customer", "customer")
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE products SET metadata='{"opaque":"should-not-leak"}' WHERE id=$1`, f.productID); err != nil {
		t.Fatal(err)
	}
	options := request(f.h, "GET", "/api/v1/admin/orders/create-options", nil, f.root, "")
	expect(t, options, 200)
	if _, ok := data(t, options)["payment_providers"].([]any); !ok {
		t.Fatal("missing providers")
	}
	customers := request(f.h, "GET", "/api/v1/admin/orders/create-options?kind=customers&limit=1&offset=1", nil, f.root, "")
	expect(t, customers, 200)
	d := data(t, customers)
	if d["total"] != float64(2) || d["limit"] != float64(1) || d["offset"] != float64(1) || len(d["items"].([]any)) != 1 {
		t.Fatal("incorrect options pagination", d)
	}
	selected := request(f.h, "GET", fmt.Sprintf("/api/v1/admin/orders/create-options?kind=products&product_id=%d", f.productID), nil, f.root, "")
	expect(t, selected, 200)
	d = data(t, selected)
	if d["total"] != float64(1) || strings.Contains(selected.Body.String(), "should-not-leak") || strings.Contains(selected.Body.String(), "delivery_config") {
		t.Fatal("opaque admin data leaked", selected.Body.String())
	}
	row := d["items"].([]any)[0].(map[string]any)
	if row["revision"] == nil || row["pricing"] == nil || row["option_groups"] == nil || row["purchase_field_definitions"] == nil {
		t.Fatal("missing typed product config", row)
	}
	empty := request(f.h, "GET", "/api/v1/admin/orders/create-options?kind=products&q=absent-search-term", nil, f.root, "")
	expect(t, empty, 200)
	if data(t, empty)["total"] != float64(0) || len(data(t, empty)["items"].([]any)) != 0 {
		t.Fatal("fake search results")
	}
	expect(t, request(f.h, "GET", "/api/v1/admin/orders/create-options?kind=products&product_id=bogus", nil, f.root, ""), 400)
}

func TestAdminOrderCreateDeferredCancellationAndOncePrice(t *testing.T) {
	f := newMutationOrderFixture(t)
	if _, err := f.db.Pool.Exec(t.Context(), "INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents) VALUES($1,'once',1234,99) ON CONFLICT(product_id,billing_cycle) DO UPDATE SET active=true", f.productID); err != nil {
		t.Fatal(err)
	}
	in := adminOrderDraft(t, f)
	in.Items[0].BillingCycle = "once"
	override := int64(800)
	in.Items[0].FirstPriceOverride = &override
	generateInvoice := false
	in.GenerateInvoice = &generateInvoice
	in = adminOrderQuoted(t, f, in, f.root)
	created := request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-once-deferred")
	expect(t, created, 201)
	d := data(t, created)
	id := int64(d["order_id"].(float64))
	var recurring int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT recurring_price_cents FROM services WHERE order_id=$1", id).Scan(&recurring); err != nil || recurring != 0 {
		t.Fatal("once became recurring", recurring, err)
	}
	path := fmt.Sprintf("/api/v1/admin/orders/%d", id)
	expect(t, request(f.h, "POST", path+"/cancel", map[string]any{"expected_version": d["version"]}, f.root, "admin-once-cancel"), 201)
	v := orderVersion(t, f.db, id)
	expect(t, request(f.h, "POST", path+"/invoice", map[string]any{"expected_version": v}, f.root, "admin-cancelled-invoice"), 409)
	var invoices, stock int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM invoices WHERE order_id=$1),stock FROM products WHERE id=$2", id, f.productID).Scan(&invoices, &stock); err != nil || invoices != 0 || stock != 12 {
		t.Fatal("deferred cancel leaked state", invoices, stock, err)
	}
}

func TestAdminOrderCreateEmailSkipChannelAndTemplate(t *testing.T) {
	f := newMutationOrderFixture(t)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET email_verified=true WHERE id=$1", f.customerID); err != nil {
		t.Fatal(err)
	}
	in := adminOrderDraft(t, f)
	in.SendEmail = true
	in = adminOrderQuoted(t, f, in, f.root)
	missing := request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-email-no-channel")
	expect(t, missing, 201)
	if data(t, missing)["email_status"] != "skipped_channel" {
		t.Fatal("unconfigured channel appeared sent")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE notification_templates SET active=false WHERE kind='order_created'"); err != nil {
		t.Fatal(err)
	}
	missing = request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-email-no-template")
	expect(t, missing, 201)
	if data(t, missing)["email_status"] != "skipped_template" {
		t.Fatal("disabled template appeared sent")
	}
}
