package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInvoiceDetailUsesItsTransactionConnection(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	f.h = New(f.db, epayTestConfig(), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	invoiceID := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "detail-single-connection-invoice")
	epayIntent(t, f, invoiceID, "epay_alipay", "detail-single-connection-payment")
	poolConfig := f.db.Pool.Config()
	poolConfig.MaxConns, poolConfig.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	f.db.Pool.Close()
	f.db.Pool = pool
	for _, route := range []struct {
		path, cookie string
	}{
		{fmt.Sprintf("/api/v1/invoices/%d", invoiceID), f.customer},
		{fmt.Sprintf("/api/v1/admin/invoices/%d", invoiceID), f.root},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		r := httptest.NewRequest("GET", route.path, nil).WithContext(ctx)
		r.Header.Set("Cookie", route.cookie)
		response := httptest.NewRecorder()
		f.h.ServeHTTP(response, r)
		cancel()
		expect(t, response, 200)
		payments := data(t, response)["payments"].([]any)
		if len(payments) != 1 || payments[0].(map[string]any)["query_enabled"] != true {
			t.Fatal("transaction credentials not resolved", response.Body.String())
		}
	}
}

func transactionFixture(t *testing.T, db *store.Store, h http.Handler) upgradeFixture {
	t.Helper()
	f := setupConfigurationUpgradeFixture(t, db, h, false)
	for n := 0; n < 2; n++ {
		v, item := configuredCart(t, h, f.customer, f.pid, []int64{f.small}, 1)
		expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, f.customer, fmt.Sprintf("transaction-pending-%d", n)), 201)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE orders SET notes='private-admin-note' WHERE user_id=$1", f.uid); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestTransactionDetailsPrivacyOwnershipAndScope(t *testing.T) {
	db, h := testServer(t)
	f := transactionFixture(t, db, h)
	var invoiceID int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM invoices WHERE order_id=$1", f.orderID).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	orderPath := fmt.Sprintf("/api/v1/orders/%d", f.orderID)
	order := request(h, "GET", orderPath, nil, f.customer, "")
	expect(t, order, 200)
	for _, token := range []string{"private-admin-note", "notes", "upstream_id", "username", "resource-upgrade", "last_error", "request_hash", "legacy_source"} {
		if bytes.Contains(order.Body.Bytes(), []byte(token)) {
			t.Fatalf("customer order leaked %q", token)
		}
	}
	item := data(t, order)["items"].([]any)[0].(map[string]any)
	if item["price_cents"] != float64(2500) || item["setup_fee_cents"] != float64(100) || item["config_snapshot"].([]any)[0].(map[string]any)["label"] != "4GB" {
		t.Fatal("order snapshot missing", item)
	}
	originalName := item["product_name"]
	if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET name='changed-product-name' WHERE id=$1", f.pid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE product_options SET label='changed-label' WHERE id=$1", f.small); err != nil {
		t.Fatal(err)
	}
	item = data(t, request(h, "GET", orderPath, nil, f.customer, ""))["items"].([]any)[0].(map[string]any)
	if item["product_name"] != originalName || item["config_snapshot"].([]any)[0].(map[string]any)["label"] != "4GB" {
		t.Fatal("catalog edits changed transaction snapshot", item)
	}
	for _, path := range []string{"orders", "orders/page"} {
		w := request(h, "GET", "/api/v1/"+path, nil, f.customer, "")
		expect(t, w, 200)
		if bytes.Contains(w.Body.Bytes(), []byte("private-admin-note")) || bytes.Contains(w.Body.Bytes(), []byte(`"notes"`)) {
			t.Fatal("customer list leaked internal notes", path)
		}
	}
	invoicePath := fmt.Sprintf("/api/v1/invoices/%d", invoiceID)
	invoice := request(h, "GET", invoicePath, nil, f.customer, "")
	expect(t, invoice, 200)
	detail := data(t, invoice)
	if detail["order_id"] != float64(f.orderID) || len(detail["order_items"].([]any)) != 1 || len(detail["ledger"].([]any)) != 1 || detail["amount_cents"] != float64(2600) {
		t.Fatal("purchase invoice detail", detail)
	}
	for _, path := range []string{orderPath, invoicePath} {
		expect(t, request(h, "GET", path, nil, "", ""), 401)
		expect(t, request(h, "GET", path, nil, f.other, ""), 404)
	}
	staffID, staff := user(t, db, h, "transaction-reader", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view'] WHERE name='transaction-reader-role'"); err != nil {
		t.Fatal(err)
	}
	adminPath := fmt.Sprintf("/api/v1/admin/invoices/%d", invoiceID)
	expect(t, request(h, "GET", adminPath, nil, staff, ""), 200)
	expect(t, request(h, "POST", adminPath+"/refund", map[string]any{}, staff, "transaction-reader-refund"), 403)
	expect(t, request(h, "GET", adminPath, nil, f.customer, ""), 403)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned' WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", adminPath, nil, staff, ""), 404)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view','customers.view'] WHERE name='transaction-reader-role'"); err != nil {
		t.Fatal(err)
	}
	salesAssignment(t, h, f.root, f.uid, staffID, 1)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view'] WHERE name='transaction-reader-role'"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", adminPath, nil, staff, ""), 200)
	salesAssignment(t, h, f.root, f.uid, nil, 2)
	expect(t, request(h, "GET", adminPath, nil, staff, ""), 404)
	page := data(t, request(h, "GET", "/api/v1/admin/invoices/page", nil, staff, ""))
	if page["total"] != float64(0) || len(page["items"].([]any)) != 0 {
		t.Fatal("invoice count leaked sales scope", page)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY[]::text[] WHERE name='transaction-reader-role'"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", adminPath, nil, staff, ""), 403)
	for _, kind := range []string{"orders", "invoices"} {
		for _, id := range []string{"0", "bad", "-1"} {
			expect(t, request(h, "GET", "/api/v1/"+kind+"/"+id, nil, f.customer, ""), 400)
		}
		expect(t, request(h, "GET", "/api/v1/"+kind+"/99999", nil, f.customer, ""), 404)
	}
}

func TestTransactionPagesAndPaymentRecords(t *testing.T) {
	db, h := testServer(t)
	f := transactionFixture(t, db, h)
	for _, kind := range []string{"orders", "invoices"} {
		page := data(t, request(h, "GET", "/api/v1/"+kind+"/page?limit=2&offset=2", nil, f.customer, ""))
		if page["total"] != float64(3) || len(page["items"].([]any)) != 1 || page["offset"] != float64(2) || page["limit"] != float64(2) {
			t.Fatal("page contract", kind, page)
		}
		empty := data(t, request(h, "GET", "/api/v1/"+kind+"/page?offset=20", nil, f.customer, ""))
		if empty["total"] != float64(3) || len(empty["items"].([]any)) != 0 {
			t.Fatal("empty trailing page lost total", empty)
		}
		other := data(t, request(h, "GET", "/api/v1/"+kind+"/page", nil, f.other, ""))
		if other["total"] != float64(0) {
			t.Fatal("page crossed ownership")
		}
		expect(t, request(h, "GET", "/api/v1/"+kind+"/page?status=invalid", nil, f.customer, ""), 400)
		expect(t, request(h, "GET", "/api/v1/"+kind+"/page?q="+strings.Repeat("x", 121), nil, f.customer, ""), 400)
	}
	paid := data(t, request(h, "GET", "/api/v1/invoices/page?status=paid", nil, f.customer, ""))
	if paid["total"] != float64(1) {
		t.Fatal("invoice status filter", paid)
	}
	products := data(t, request(h, "GET", "/api/v1/orders/page?q=upgrade-product&status=pending", nil, f.customer, ""))
	if products["total"] != float64(2) {
		t.Fatal("order product filter", products)
	}
	products = data(t, request(h, "GET", "/api/v1/invoices/page?q=upgrade-product&status=unpaid", nil, f.customer, ""))
	if products["total"] != float64(2) {
		t.Fatal("purchase invoice snapshot search", products)
	}
	var pendingID int64
	var invoiceNo, orderNo string
	if err := db.Pool.QueryRow(t.Context(), "SELECT i.id,i.invoice_no,o.order_no FROM invoices i JOIN orders o ON o.id=i.order_id WHERE i.status='unpaid' ORDER BY i.id LIMIT 1").Scan(&pendingID, &invoiceNo, &orderNo); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{invoiceNo, orderNo} {
		found := data(t, request(h, "GET", "/api/v1/invoices/page?q="+q, nil, f.customer, ""))
		if found["total"] != float64(1) {
			t.Fatal("invoice query filter", found)
		}
	}
	path := fmt.Sprintf("/api/v1/invoices/%d", pendingID)
	payment := request(h, "POST", path+"/payments", map[string]any{"provider": "mock"}, f.customer, "transaction-pending-intent")
	expect(t, payment, 201)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET raw_result='{\"secret\":\"gateway-secret\"}' WHERE invoice_id=$1", pendingID); err != nil {
		t.Fatal(err)
	}
	invoice := request(h, "GET", path, nil, f.customer, "")
	expect(t, invoice, 200)
	detail := data(t, invoice)
	if detail["payment_pending"] != true || len(detail["payments"].([]any)) != 1 || bytes.Contains(invoice.Body.Bytes(), []byte("gateway-secret")) || bytes.Contains(invoice.Body.Bytes(), []byte("idempotency_key")) {
		t.Fatal("unsafe payment record contract", detail)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET expires_at=now()-interval '1 minute' WHERE invoice_id=$1", pendingID); err != nil {
		t.Fatal(err)
	}
	detail = data(t, request(h, "GET", path, nil, f.customer, ""))
	if detail["payment_pending"] != true || detail["payments"].([]any)[0].(map[string]any)["expired"] != true {
		t.Fatal("expired pending payment", detail)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET expires_at=now()+interval '1 hour' WHERE invoice_id=$1", pendingID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", path+"/pay", map[string]any{}, f.customer, "transaction-pending-balance"), 201)
	detail = data(t, request(h, "GET", path, nil, f.customer, ""))
	if detail["status"] != "paid" || detail["payment_pending"] != false || len(detail["ledger"].([]any)) != 1 {
		t.Fatal("paid invoice ledger", detail)
	}
	paymentID := data(t, payment)["provider_payment_id"].(string)
	if _, err := db.ProcessPaymentWebhook(t.Context(), "mock", "late-transaction-callback", paymentID, pendingID, int64(detail["amount_cents"].(float64)), "succeeded", []byte(`{"status":"succeeded"}`)); err == nil {
		t.Fatal("late gateway callback settled an already paid invoice")
	}
	adminPath := fmt.Sprintf("/api/v1/admin/invoices/%d", pendingID)
	expect(t, request(h, "POST", adminPath+"/refund", map[string]any{}, f.root, "transaction-invoice-refund"), 201)
	detail = data(t, request(h, "GET", path, nil, f.customer, ""))
	if detail["status"] != "refunded" || len(detail["ledger"].([]any)) != 2 {
		t.Fatal("refund not reflected", detail)
	}
}

func TestTransactionInvoiceReadRechecksWaitingSalesTransfer(t *testing.T) {
	db, h := testServer(t)
	f := transactionFixture(t, db, h)
	staffID, staff := user(t, db, h, "transaction-race-reader", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned' WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", f.uid, staffID); err != nil {
		t.Fatal(err)
	}
	var invoiceID int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM invoices WHERE order_id=$1", f.orderID).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('customer-sales:'||$1::bigint::text,0))", f.uid); err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() {
		result <- request(h, "GET", fmt.Sprintf("/api/v1/admin/invoices/%d", invoiceID), nil, staff, "").Code
	}()
	for {
		var waiting bool
		if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'SELECT pg_advisory_xact_lock_shared%customer-sales:%')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("invoice read never reached shared scope lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE users SET sales_owner_id=NULL,sales_version=sales_version+1 WHERE id=$1", f.uid); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-result:
		if status != 404 {
			t.Fatalf("transferred invoice read status=%d", status)
		}
	case <-ctx.Done():
		t.Fatal("invoice read did not finish")
	}
}

func TestTransactionStandaloneServiceInvoices(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	change := f.submit(t, f.quote(t, f.large), "transaction-upgrade-submit")
	invoice := int64(change["invoice_id"].(float64))
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", invoice), nil, f.customer, ""))
	if detail["order_id"] != nil || len(detail["order_items"].([]any)) != 0 || detail["items"].([]any)[0].(map[string]any)["service_id"] != float64(f.sid) {
		t.Fatal("upgrade bill projection", detail)
	}
	expect(t, request(f.h, "DELETE", fmt.Sprintf("/api/v1/services/%d/configuration-changes/%d", f.sid, int64(change["id"].(float64))), nil, f.customer, ""), 200)
	renewal := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", f.sid), map[string]any{}, f.customer, "transaction-renewal")
	expect(t, renewal, 201)
	invoice = int64(data(t, renewal)["invoice_id"].(float64))
	detail = data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", invoice), nil, f.customer, ""))
	if detail["amount_cents"] != float64(2500) || detail["items"].([]any)[0].(map[string]any)["item_type"] != "renew" {
		t.Fatal("renewal bill projection", detail)
	}
}

func TestTransactionBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "transaction-super", func(db *store.Store, h http.Handler) {
		transactionFixture(t, db, h)
		user(t, db, h, "transaction-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view'] WHERE name='transaction-reader-role'"); err != nil {
			t.Fatal(err)
		}
	})
}
