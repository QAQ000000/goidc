package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	expect(t, request(h, http.MethodPost, fmt.Sprintf("/api/v1/admin/affiliate/accounts/%d/status", accountID), map[string]any{"status": "approved", "expected_version": 1}, admin, "affiliate-approve-1"), http.StatusOK)
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
	issued := request(h, http.MethodPost, "/api/v1/auth/verification/request", map[string]string{"purpose": "register", "channel": "email", "recipient": "affiliate-registered@example.test"}, "", "")
	expect(t, issued, http.StatusAccepted)
	challenge := data(t, issued)["challenge_id"].(string)
	registered := request(h, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"username": "affiliate-registered", "email": "affiliate-registered@example.test", "display_name": "归因客户", "password": "integration-password",
		"verification_id": challenge, "verification_code": verificationEmailCode(t, db, challenge), "affiliate_code": secondCode,
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
	if len(invalid.Result().Cookies()) != 0 {
		t.Fatal("invalid link poisoned attribution cookies")
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE affiliate_links SET status='disabled' WHERE id=$1", linkID); err != nil {
		t.Fatal(err)
	}
	disabled := request(h, http.MethodGet, "/api/v1/affiliate/click/"+firstCode, nil, "", "")
	expect(t, disabled, http.StatusOK)
	if data(t, disabled)["landing_path"] != "/" || len(disabled.Result().Cookies()) != 0 {
		t.Fatal("disabled link did not degrade safely")
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

func TestAffiliateLinkOwnershipIdempotencyAndAccountVersion(t *testing.T) {
	db, h, referrerID, referrer, _, linkID, _, _ := setupAffiliateFixture(t)
	_, outsider := user(t, db, h, "affiliate-link-outsider", "customer")
	path := fmt.Sprintf("/api/v1/affiliate/links/%d", linkID)
	payload := map[string]any{"landing_path": "/store/changed", "status": "disabled"}
	expect(t, request(h, http.MethodPatch, path, payload, outsider, "affiliate-link-update"), http.StatusNotFound)
	expect(t, request(h, http.MethodPatch, path, payload, referrer, "affiliate-link-update"), http.StatusOK)
	expect(t, request(h, http.MethodPatch, path, payload, referrer, "affiliate-link-update"), http.StatusOK)
	payload["status"] = "active"
	expect(t, request(h, http.MethodPatch, path, payload, referrer, "affiliate-link-update"), http.StatusConflict)
	var audits int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='affiliate.link.updated' AND entity_id=$1", fmt.Sprint(linkID)).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("link replay audit=%d err=%v", audits, err)
	}
	login := request(h, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "affiliate-admin", "password": "integration-password"}, "", "")
	expect(t, login, http.StatusOK)
	admin := "zjmfgo_session=" + affiliateCookieValue(login.Result(), "zjmfgo_session")
	var accountID int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM affiliate_accounts WHERE user_id=$1", referrerID).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	statusPath := fmt.Sprintf("/api/v1/admin/affiliate/accounts/%d/status", accountID)
	expect(t, request(h, http.MethodPost, statusPath, map[string]any{"status": "approved", "expected_version": 1}, admin, "affiliate-approve-1"), http.StatusOK)
	expect(t, request(h, http.MethodPost, statusPath, map[string]any{"status": "suspended", "expected_version": 1}, admin, "affiliate-suspend-stale"), http.StatusConflict)
	expect(t, request(h, http.MethodPost, statusPath, map[string]any{"status": "suspended", "expected_version": 2}, admin, "affiliate-suspend-current"), http.StatusOK)
}

func TestAffiliatePaymentSettlementChains(t *testing.T) {
	for _, method := range []string{"balance", "epay_callback", "epay_query"} {
		t.Run(method, func(t *testing.T) {
			db, h, referrerID, referrer, referredID, _, code, _ := setupAffiliateFixture(t)
			click := request(h, http.MethodGet, "/api/v1/affiliate/click/"+code, nil, "", "")
			expect(t, click, http.StatusOK)
			tx, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			if err = db.ConvertAffiliateClickTx(t.Context(), tx, referredID, code, tokenHash(affiliateCookieValue(click.Result(), "zjmfgo_affiliate_session"))); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Pool.Exec(t.Context(), `UPDATE affiliate_rule_sets SET default_rules='{"first_purchase":{"rate_bps":1000}}'`); err != nil {
				t.Fatal(err)
			}
			merchant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "pid": "10001", "type": "alipay", "trade_no": "affiliate-chain-trade", "out_trade_no": r.URL.Query().Get("out_trade_no"), "money": "10.00", "status": 1})
			}))
			defer merchant.Close()
			cfg := epayTestConfig()
			cfg.EpayAPIURL = merchant.URL
			h = New(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
			login := request(h, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "affiliate-referred", "password": "integration-password"}, "", "")
			expect(t, login, http.StatusOK)
			customer := "zjmfgo_session=" + affiliateCookieValue(login.Result(), "zjmfgo_session")
			pid := product(t, db, 10)
			version, itemID := add(t, h, customer, pid, 1)
			order := request(h, http.MethodPost, "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "affiliate-chain-order")
			expect(t, order, http.StatusCreated)
			invoiceID := int64(data(t, order)["invoice_id"].(float64))
			// The order's item snapshot remains gross; the payable invoice is net.
			if _, err = db.Pool.Exec(t.Context(), `WITH changed AS (UPDATE invoices SET amount_cents=1000 WHERE id=$1 RETURNING order_id)
UPDATE orders SET total_cents=1000,discount_cents=333 WHERE id=(SELECT order_id FROM changed)`, invoiceID); err != nil {
				t.Fatal(err)
			}
			f := upgradeFixture{db: db, h: h, uid: referredID, customer: customer}
			// Publish a later rule version before payment. The invoice must keep
			// the rule that was effective when it was created.
			if _, err = db.Pool.Exec(t.Context(), `WITH old AS (
 UPDATE affiliate_rule_sets SET effective_to=clock_timestamp() WHERE effective_to IS NULL
 RETURNING program_id,version,effective_to
)
INSERT INTO affiliate_rule_sets(program_id,version,effective_from,default_rules)
SELECT program_id,version+1,effective_to,'{"first_purchase":{"rate_bps":9000}}'::jsonb FROM old`); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			results := make(chan *httptest.ResponseRecorder, 2)
			if method == "balance" {
				if _, err = db.Pool.Exec(t.Context(), "UPDATE users SET balance_cents=5000 WHERE id=$1", referredID); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						results <- request(h, http.MethodPost, fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "affiliate-chain-pay")
					}()
				}
			} else {
				var invoiceVersion int64
				if err = db.Pool.QueryRow(t.Context(), "SELECT version FROM invoices WHERE id=$1", invoiceID).Scan(&invoiceVersion); err != nil {
					t.Fatal(err)
				}
				intent := request(h, http.MethodPost, fmt.Sprintf("/api/v1/invoices/%d/payments", invoiceID), map[string]any{"provider": "epay_alipay", "expected_version": invoiceVersion}, f.customer, "affiliate-chain-intent")
				expect(t, intent, http.StatusCreated)
				checkoutURL, err := url.Parse(data(t, intent)["checkout_url"].(string))
				if err != nil {
					t.Fatal(err)
				}
				checkout := checkoutURL.Query()
				callback := epayNotification(checkout, "affiliate-chain-trade")
				var paymentID int64
				if err = db.Pool.QueryRow(t.Context(), "SELECT id FROM payment_transactions WHERE invoice_id=$1", invoiceID).Scan(&paymentID); err != nil {
					t.Fatal(err)
				}
				if method == "epay_query" {
					expect(t, request(h, http.MethodPost, queryPath(invoiceID, paymentID, false), map[string]any{}, customer, ""), http.StatusOK)
				}
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						results <- epayCallback(h, http.MethodGet, "epay_alipay", callback)
					}()
				}
			}
			wg.Wait()
			close(results)
			for result := range results {
				if result.Code != http.StatusOK && result.Code != http.StatusCreated {
					t.Fatalf("settlement failed: %d %s", result.Code, result.Body.String())
				}
			}
			var count, events int
			var amount, basis, balance, referrerBalance int64
			var state string
			var ruleSnapshot, sourceSnapshot map[string]any
			if err = db.Pool.QueryRow(t.Context(), `SELECT amount_cents,basis_cents,status,rule_snapshot,source_snapshot FROM affiliate_commission_entries WHERE invoice_id=$1`, invoiceID).Scan(&amount, &basis, &state, &ruleSnapshot, &sourceSnapshot); err != nil {
				t.Fatal(err)
			}
			if amount != 100 || basis != 1000 || state != "pending" || ruleSnapshot["rate_bps"] != float64(1000) || ruleSnapshot["rule_version"] != float64(1) || sourceSnapshot["invoice"] == nil || sourceSnapshot["item"] == nil {
				t.Fatalf("incorrect commission amount=%d basis=%d state=%s rules=%v source=%v", amount, basis, state, ruleSnapshot, sourceSnapshot)
			}
			if err = db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM affiliate_commission_entries WHERE invoice_id=$1),
(SELECT count(*) FROM affiliate_commission_events e JOIN affiliate_commission_entries c ON c.id=e.entry_id WHERE c.invoice_id=$1),
(SELECT balance_cents FROM users WHERE id=$2),(SELECT balance_cents FROM users WHERE id=$3)`, invoiceID, referredID, referrerID).Scan(&count, &events, &balance, &referrerBalance); err != nil {
				t.Fatal(err)
			}
			wantBalance := int64(0)
			if method == "balance" {
				wantBalance = 4000
			}
			if count != 1 || events != 1 || balance != wantBalance || referrerBalance != 0 {
				t.Fatalf("duplicate settlement entries=%d events=%d balance=%d referrer_balance=%d", count, events, balance, referrerBalance)
			}
			overview := request(h, http.MethodGet, "/api/v1/affiliate/overview", nil, referrer, "")
			expect(t, overview, http.StatusOK)
			if data(t, overview)["pending_cents"] != float64(100) {
				t.Fatalf("overview failed: %s", overview.Body.String())
			}
			// Replaying after rule changes cannot mutate historical amounts/events.
			if _, err = db.Pool.Exec(t.Context(), `UPDATE affiliate_rule_sets SET default_rules='{"first_purchase":{"rate_bps":9000}}'`); err != nil {
				t.Fatal(err)
			}
			tx, err = db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = db.QueueAffiliateCommissions(t.Context(), tx, invoiceID, 0); err != nil {
				tx.Rollback(t.Context())
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err = db.Pool.QueryRow(t.Context(), `SELECT amount_cents FROM affiliate_commission_entries WHERE invoice_id=$1`, invoiceID).Scan(&amount); err != nil || amount != 100 {
				t.Fatalf("replay rewrote commission amount=%d err=%v", amount, err)
			}
		})
	}
}

func TestAffiliateConcurrentAttributionAndSettlementRollback(t *testing.T) {
	db, h, _, _, referredID, _, code, _ := setupAffiliateFixture(t)
	otherID, _ := user(t, db, h, "affiliate-other-registration", "customer")
	session := tokenHash("affiliate-shared-registration-session")
	if _, err := db.RecordAffiliateClick(t.Context(), code, session, "", "/", "test"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []int64{referredID, otherID} {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			tx, err := db.Pool.Begin(t.Context())
			if err != nil {
				errs <- err
				return
			}
			defer tx.Rollback(t.Context())
			if err = db.ConvertAffiliateClickTx(t.Context(), tx, id, code, session); err == nil {
				err = tx.Commit(t.Context())
			}
			errs <- err
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var actualUser int64
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*),min(user_id) FROM affiliate_attributions").Scan(&count, &actualUser); err != nil || count != 1 {
		t.Fatalf("concurrent attribution count=%d err=%v", count, err)
	}
	var name string
	if err := db.Pool.QueryRow(t.Context(), "SELECT username FROM users WHERE id=$1", actualUser).Scan(&name); err != nil {
		t.Fatal(err)
	}
	login := request(h, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": name, "password": "integration-password"}, "", "")
	expect(t, login, http.StatusOK)
	customer := "zjmfgo_session=" + affiliateCookieValue(login.Result(), "zjmfgo_session")
	pid := product(t, db, 10)
	version, item := add(t, h, customer, pid, 1)
	order := request(h, http.MethodPost, "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, customer, "affiliate-rollback-order")
	expect(t, order, http.StatusCreated)
	invoice := int64(data(t, order)["invoice_id"].(float64))
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO affiliate_product_rules(rule_set_id,product_id,event_type,mode,amount_cents)
SELECT id,$1,'first_purchase','override',87 FROM affiliate_rule_sets WHERE effective_to IS NULL`, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET balance_cents=5000 WHERE id=$1", actualUser); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_affiliate_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'affiliate event fixture rejection'; END $$;
CREATE TRIGGER reject_affiliate_event BEFORE INSERT ON affiliate_commission_events FOR EACH ROW EXECUTE FUNCTION reject_affiliate_event();`); err != nil {
		t.Fatal(err)
	}
	payPath := fmt.Sprintf("/api/v1/invoices/%d/pay", invoice)
	expect(t, request(h, http.MethodPost, payPath, map[string]any{}, customer, "affiliate-rollback-pay"), http.StatusInternalServerError)
	var balance int64
	var state string
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT balance_cents FROM users WHERE id=$1),
(SELECT status FROM invoices WHERE id=$2),(SELECT count(*) FROM affiliate_commission_entries)`, actualUser, invoice).Scan(&balance, &state, &count); err != nil || balance != 5000 || state != "unpaid" || count != 0 {
		t.Fatalf("rollback failed balance=%d state=%s commissions=%d err=%v", balance, state, count, err)
	}
	if _, err := db.Pool.Exec(t.Context(), "DROP TRIGGER reject_affiliate_event ON affiliate_commission_events"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, http.MethodPost, payPath, map[string]any{}, customer, "affiliate-rollback-pay"), http.StatusCreated)
	var amount int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT amount_cents FROM affiliate_commission_entries WHERE invoice_id=$1 AND rule_snapshot->>'rule_source'='product'", invoice).Scan(&amount); err != nil || amount != 87 {
		t.Fatalf("fixed product rule amount=%d err=%v", amount, err)
	}
}
