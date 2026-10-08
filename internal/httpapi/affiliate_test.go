package httpapi

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// setupAffiliateFixture creates an approved referrer and two links without
// bypassing the customer/admin API boundaries.
func setupAffiliateFixture(t *testing.T) (*store.Store, http.Handler, int64, string, int64, int64, string, string) {
	t.Helper()
	db, h := testServer(t)
	referrerID, referrer := user(t, db, h, "affiliate-referrer", "customer")
	referredID, _ := user(t, db, h, "affiliate-referred", "customer")
	_, admin := user(t, db, h, "affiliate-admin", "super_admin")
	expect(t, request(h, http.MethodPost, "/api/v1/affiliate/apply", nil, referrer, "affiliate-apply-1"), http.StatusCreated)
	var accountID int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM affiliate_accounts WHERE user_id=$1", referrerID).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, http.MethodPost, fmt.Sprintf("/api/v1/admin/affiliate/accounts/%d/status", accountID), map[string]any{"status": "approved"}, admin, "affiliate-approve-1"), http.StatusOK)
	first := request(h, http.MethodPost, "/api/v1/affiliate/links", map[string]any{"landing_path": "/store/first", "note": "first"}, referrer, "affiliate-link-1")
	expect(t, first, http.StatusCreated)
	firstData := data(t, first)
	linkID := int64(firstData["id"].(float64))
	firstCode := firstData["code"].(string)
	second := request(h, http.MethodPost, "/api/v1/affiliate/links", map[string]any{"landing_path": "/store/second", "note": "second"}, referrer, "affiliate-link-2")
	expect(t, second, http.StatusCreated)
	secondCode := data(t, second)["code"].(string)
	return db, h, referrerID, referrer, referredID, linkID, firstCode, secondCode
}

func affiliateCookieValue(response *http.Response, name string) string {
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func TestAffiliateClickAttributionIdempotencyAndSelfReferral(t *testing.T) {
	db, h, referrerID, _, referredID, linkID, firstCode, secondCode := setupAffiliateFixture(t)
	first := request(h, http.MethodGet, "/api/v1/affiliate/click/"+firstCode, nil, "", "")
	expect(t, first, http.StatusOK)
	cookies := first.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("first click cookies=%d", len(cookies))
	}
	codeCookie := affiliateCookieValue(first.Result(), "zjmfgo_affiliate_code")
	session := affiliateCookieValue(first.Result(), "zjmfgo_affiliate_session")
	if codeCookie != firstCode || session == "" {
		t.Fatalf("first click cookies code=%q session=%q", codeCookie, session)
	}
	cookieHeader := "zjmfgo_affiliate_code=" + codeCookie + "; zjmfgo_affiliate_session=" + session
	second := request(h, http.MethodGet, "/api/v1/affiliate/click/"+secondCode, nil, cookieHeader, "")
	expect(t, second, http.StatusOK)
	if affiliateCookieValue(second.Result(), "zjmfgo_affiliate_code") != "" {
		t.Fatal("later click replaced first attribution cookie")
	}
	// Repeating the same link/session is idempotent at the database level.
	duplicate := request(h, http.MethodGet, "/api/v1/affiliate/click/"+firstCode, nil, cookieHeader, "")
	expect(t, duplicate, http.StatusOK)
	var clicks int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM affiliate_clicks WHERE session_hash=$1", tokenHash(session)).Scan(&clicks); err != nil || clicks != 2 {
		t.Fatalf("click idempotency count=%d err=%v", clicks, err)
	}
	// The first code remains authoritative even after a later link click.
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ConvertAffiliateClickTx(t.Context(), tx, referredID, firstCode, tokenHash(session)); err != nil {
		tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var attributionLink, attributionAccount int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT link_id,account_id FROM affiliate_attributions WHERE user_id=$1", referredID).Scan(&attributionLink, &attributionAccount); err != nil {
		t.Fatal(err)
	}
	if attributionLink != linkID || attributionAccount == 0 {
		t.Fatalf("wrong first-click attribution link=%d account=%d", attributionLink, attributionAccount)
	}
	// A repeated registration attempt cannot create a second attribution.
	tx, err = db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ConvertAffiliateClickTx(t.Context(), tx, referredID, firstCode, tokenHash(session)); err != nil {
		tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var attributions int
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM affiliate_attributions WHERE user_id=$1", referredID).Scan(&attributions); err != nil || attributions != 1 {
		t.Fatalf("duplicate attribution count=%d err=%v", attributions, err)
	}
	// Self-referral consumes the click so it cannot be retried, but creates no relation.
	selfSession := tokenHash("self-referral-session")
	if _, err = db.RecordAffiliateClick(t.Context(), firstCode, selfSession, "", "/", "test"); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ConvertAffiliateClickTx(t.Context(), tx, referrerID, firstCode, selfSession); err != nil {
		tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM affiliate_attributions WHERE account_id=$1", attributionAccount).Scan(&attributions); err != nil || attributions != 1 {
		t.Fatalf("self referral created attribution count=%d err=%v", attributions, err)
	}
	// A normal registration converts the first-party cookie inside the user transaction.
	registrationClick := request(h, http.MethodGet, "/api/v1/affiliate/click/"+firstCode, nil, "", "")
	expect(t, registrationClick, http.StatusOK)
	registrationCookie := "zjmfgo_affiliate_code=" + affiliateCookieValue(registrationClick.Result(), "zjmfgo_affiliate_code") + "; zjmfgo_affiliate_session=" + affiliateCookieValue(registrationClick.Result(), "zjmfgo_affiliate_session")
	registered := request(h, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"username": "affiliate-registered", "email": "affiliate-registered@example.test", "display_name": "归因客户", "password": "integration-password",
	}, registrationCookie, "")
	expect(t, registered, http.StatusCreated)
	registeredID := int64(data(t, registered)["id"].(float64))
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM affiliate_attributions WHERE user_id=$1 AND link_id=$2", registeredID, linkID).Scan(&attributions); err != nil || attributions != 1 {
		t.Fatalf("registration attribution count=%d err=%v", attributions, err)
	}
	invalid := request(h, http.MethodGet, "/api/v1/affiliate/click/not-a-valid-code", nil, "", "")
	expect(t, invalid, http.StatusOK)
	if got := data(t, invalid)["landing_path"]; got != "/" {
		t.Fatalf("invalid link exposed landing path: %v", got)
	}
}

func TestAffiliatePaymentCommissionSnapshotAndConcurrentIdempotency(t *testing.T) {
	db, h, _, _, referredID, _, firstCode, _ := setupAffiliateFixture(t)
	// Establish attribution through the real click and registration transaction.
	click := request(h, http.MethodGet, "/api/v1/affiliate/click/"+firstCode, nil, "", "")
	expect(t, click, http.StatusOK)
	session := affiliateCookieValue(click.Result(), "zjmfgo_affiliate_session")
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ConvertAffiliateClickTx(t.Context(), tx, referredID, firstCode, tokenHash(session)); err != nil {
		tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var accountID, programID int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT a.id,a.program_id FROM affiliate_accounts a JOIN affiliate_attributions at ON at.account_id=a.id WHERE at.user_id=$1", referredID).Scan(&accountID, &programID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), `UPDATE affiliate_rule_sets SET default_rules='{"first_purchase":{"rate_bps":1000},"renewal":{"rate_bps":1000},"upgrade":{"rate_bps":1000}}'::jsonb WHERE program_id=$1 AND effective_to IS NULL`, programID); err != nil {
		t.Fatal(err)
	}
	pid := product(t, db, 10)
	var orderID, orderItemID, invoiceID int64
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash)
VALUES('AFF-O-'||replace(gen_random_uuid()::text,'-',''),$1,'paid',1000,'affiliate-payment-order','fixture') RETURNING id`, referredID).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents)
VALUES($1,$2,'佣金商品','monthly',1,1000,0,1000) RETURNING id`, orderID, pid).Scan(&orderItemID); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents)
VALUES('AFF-I-'||replace(gen_random_uuid()::text,'-',''),$1,$2,'paid',1000) RETURNING id`, referredID, orderID).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	var beforeBalance int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", referredID).Scan(&beforeBalance); err != nil {
		t.Fatal(err)
	}
	queue := func() error {
		transaction, beginErr := db.Pool.Begin(t.Context())
		if beginErr != nil {
			return beginErr
		}
		defer transaction.Rollback(t.Context())
		if queueErr := db.QueueAffiliateCommissions(t.Context(), transaction, invoiceID, 0); queueErr != nil {
			return queueErr
		}
		return transaction.Commit(t.Context())
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- queue()
		}()
	}
	wg.Wait()
	close(errs)
	for queueErr := range errs {
		if queueErr != nil {
			t.Fatal(queueErr)
		}
	}
	var entries, events int
	var amount, basis int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*),coalesce(sum(amount_cents),0),coalesce(sum(basis_cents),0) FROM affiliate_commission_entries WHERE invoice_id=$1", invoiceID).Scan(&entries, &amount, &basis); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM affiliate_commission_events e JOIN affiliate_commission_entries c ON c.id=e.entry_id WHERE c.invoice_id=$1", invoiceID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if entries != 1 || events != 1 || amount != 100 || basis != 1000 {
		t.Fatalf("commission entries=%d events=%d amount=%d basis=%d", entries, events, amount, basis)
	}
	var status string
	if err = db.Pool.QueryRow(t.Context(), "SELECT status FROM affiliate_commission_entries WHERE invoice_id=$1", invoiceID).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("commission status=%s err=%v", status, err)
	}
	var afterBalance int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", referredID).Scan(&afterBalance); err != nil || afterBalance != beforeBalance {
		t.Fatalf("affiliate changed user balance before=%d after=%d err=%v", beforeBalance, afterBalance, err)
	}
	// Renewal/upgrade invoice items must not be misclassified as order items.
	var serviceID, renewalInvoiceID, renewalItemID int64
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,next_due_at,provision_mode,unit_number,recurring_price_cents)
VALUES($1,$2,$3,$4,'续费服务','affiliate-renew.example','monthly','active',now()+interval '1 month','simulation',1,1000) RETURNING id`, referredID, orderID, orderItemID, pid).Scan(&serviceID); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents)
VALUES('AFF-R-'||replace(gen_random_uuid()::text,'-',''),$1,NULL,'paid',1000) RETURNING id`, referredID).Scan(&renewalInvoiceID); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) VALUES($1,'续费','renew',1000,$2) RETURNING id`, renewalInvoiceID, serviceID).Scan(&renewalItemID); err != nil {
		t.Fatal(err)
	}
	transaction, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueueAffiliateCommissions(t.Context(), transaction, renewalInvoiceID, 0); err != nil {
		transaction.Rollback(t.Context())
		t.Fatal(err)
	}
	if err = transaction.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM affiliate_commission_entries WHERE invoice_item_id=$1 AND order_item_id IS NULL AND event_type='renewal'", renewalItemID).Scan(&entries); err != nil || entries != 1 {
		t.Fatalf("renewal commission entries=%d err=%v", entries, err)
	}
	_ = accountID
}

func TestAffiliateAdminPermissionBoundary(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "affiliate-permission-customer", "customer")
	_, admin := user(t, db, h, "affiliate-permission-admin", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['affiliates.view'] WHERE id=(SELECT admin_role_id FROM users WHERE username='affiliate-permission-admin')"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, http.MethodGet, "/api/v1/admin/affiliate/accounts", nil, admin, ""), http.StatusOK)
	expect(t, request(h, http.MethodGet, "/api/v1/admin/affiliate/attributions", nil, admin, ""), http.StatusOK)
	expect(t, request(h, http.MethodPost, "/api/v1/admin/affiliate/accounts/1/status", map[string]any{"status": "approved"}, admin, "affiliate-status-1"), http.StatusForbidden)
	expect(t, request(h, http.MethodGet, "/api/v1/admin/affiliate/accounts", nil, customer, ""), http.StatusForbidden)
}
