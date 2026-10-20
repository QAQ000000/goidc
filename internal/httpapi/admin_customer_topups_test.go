package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestAdminCustomerTopupUnpaidReplayAndExactlyOnceSettlement(t *testing.T) {
	f := onlineTopupFixture(t)
	path := fmt.Sprintf("/api/v1/admin/customers/%d", f.uid)
	options := request(f.h, "GET", path+"/topup-options", nil, f.root, "")
	expect(t, options, 200)
	body := map[string]any{"amount_cents": 1234, "expected_version": data(t, options)["version"], "note": "客户申请代建"}
	var before int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&before); err != nil {
		t.Fatal(err)
	}
	first := request(f.h, "POST", path+"/topups", body, f.root, "admin-topup-first")
	expect(t, first, 201)
	invoice := int64(data(t, first)["invoice_id"].(float64))
	var balance, credits int64
	var status string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT balance_cents,(SELECT count(*) FROM balance_ledger WHERE reference_type='gateway_topup' AND reference_id=$2),(SELECT status FROM invoices WHERE id=$2) FROM users WHERE id=$1`, f.uid, invoice).Scan(&balance, &credits, &status); err != nil {
		t.Fatal(err)
	}
	if balance != before || credits != 0 || status != "unpaid" || data(t, first)["status"] != "unpaid" {
		t.Fatalf("creation credited wallet: %d/%d/%s", balance, credits, status)
	}
	// The normal customer payment flow owns acceptance and the signed callback.
	checkout := epayIntent(t, f, invoice, "epay_alipay", "admin-topup-payment")
	for range 2 {
		expect(t, epayCallback(f.h, "POST", "epay_alipay", epayNotification(checkout, "admin-topup-gateway")), 200)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT balance_cents,(SELECT count(*) FROM balance_ledger WHERE reference_type='gateway_topup' AND reference_id=$2) FROM users WHERE id=$1`, f.uid, invoice).Scan(&balance, &credits); err != nil {
		t.Fatal(err)
	}
	if balance != before+1234 || credits != 1 {
		t.Fatalf("settlement not exactly once: %d %d", balance, credits)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE finance_settings SET topup_enabled=false,version=version+1"); err != nil {
		t.Fatal(err)
	}
	replay := request(f.h, "POST", path+"/topups", body, f.root, "admin-topup-first")
	expect(t, replay, 200)
	if data(t, replay)["invoice_id"] != float64(invoice) || data(t, replay)["status"] != "unpaid" {
		t.Fatal("receipt must preserve the original creation fact")
	}
	body["amount_cents"] = 1235
	expect(t, request(f.h, "POST", path+"/topups", body, f.root, "admin-topup-first"), 409)
	expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", invoice), nil, f.other, ""), 404)
}

func TestAdminCustomerTopupScopeInputsAndAdmission(t *testing.T) {
	f := newAdminBalanceFixture(t)
	path := fmt.Sprintf("/api/v1/admin/customers/%d", f.customer)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE finance_settings SET topup_enabled=true,min_topup_cents=100,max_topup_cents=10000,require_active_order=false,max_balance_cents=2000"); err != nil {
		t.Fatal(err)
	}
	option := request(f.h, "GET", path+"/topup-options", nil, f.cookie, "")
	expect(t, option, 200)
	body := map[string]any{"amount_cents": 1234, "expected_version": data(t, option)["version"], "note": "代建备注"}
	expect(t, request(f.h, "POST", path+"/topups", body, f.cookie, ""), 400)
	expect(t, request(f.h, "POST", path+"/topups", body, f.customerAuth, "invalid-customer"), 401)
	for _, change := range []map[string]any{{"amount_cents": 0}, {"amount_cents": 1.5}, {"expected_version": 0}, {"note": ""}} {
		bad := map[string]any{}
		for k, v := range body {
			bad[k] = v
		}
		for k, v := range change {
			bad[k] = v
		}
		expect(t, request(f.h, "POST", path+"/topups", bad, f.cookie, "admin-topup-invalid"), 400)
	}
	body["amount_cents"] = 3000
	expect(t, request(f.h, "POST", path+"/topups", body, f.cookie, "admin-topup-ceiling"), 409)
	body["amount_cents"] = 1234
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.manage'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", f.actor); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/topups", body, f.cookie, "admin-topup-no-view"), 403)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view','invoices.manage'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", f.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", path+"/topup-options", nil, f.cookie, ""), 404)
	expect(t, request(f.h, "POST", path+"/topups", body, f.cookie, "admin-topup-outside"), 404)
	f.assertFunds(t, 0, 0, 0)
}

func TestAdminCustomerTopupConcurrentCreationAndAuditRollback(t *testing.T) {
	f := newAdminBalanceFixture(t)
	path := fmt.Sprintf("/api/v1/admin/customers/%d/topups", f.customer)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE finance_settings SET topup_enabled=true,min_topup_cents=100,max_topup_cents=10000,require_active_order=false"); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT version FROM finance_settings").Scan(&version); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"amount_cents": 1234, "expected_version": version, "note": "并发代建"}
	done := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { done <- request(f.h, "POST", path, body, f.cookie, "admin-topup-concurrent") }()
	}
	statuses := map[int]int{}
	ids := map[float64]bool{}
	for range 2 {
		out := awaitBalanceResponse(t, done)
		if out.Code != 200 && out.Code != 201 {
			t.Fatalf("%d %s", out.Code, out.Body.String())
		}
		statuses[out.Code]++
		ids[data(t, out)["invoice_id"].(float64)] = true
	}
	if statuses[201] != 1 || statuses[200] != 1 || len(ids) != 1 {
		t.Fatal("duplicate bill", statuses, ids)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_admin_topup_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='balance.topup.invoice_created' THEN RAISE EXCEPTION 'isolated audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_admin_topup_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_admin_topup_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path, body, f.cookie, "admin-topup-audit-fail"), 500)
	var bills, receipts int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM balance_topups WHERE user_id=$1),(SELECT count(*) FROM mutation_operations WHERE idempotency_key='admin-topup-audit-fail')`, f.customer).Scan(&bills, &receipts); err != nil {
		t.Fatal(err)
	}
	if bills != 1 || receipts != 0 {
		t.Fatalf("audit rollback %d %d", bills, receipts)
	}
	f.assertFunds(t, 0, 0, 0)
}

func TestAdminCustomerTopupCredentialExpiryAfterWait(t *testing.T) {
	for _, kind := range []string{"session", "api_key"} {
		for _, wait := range []string{"customer", "receipt", "audit"} {
			t.Run(kind+"/"+wait, func(t *testing.T) {
				f := newAdminBalanceFixture(t)
				secret := ""
				if kind == "api_key" {
					secret = f.apiKey(t)
				}
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE finance_settings SET topup_enabled=true,min_topup_cents=100,max_topup_cents=10000,require_active_order=false"); err != nil {
					t.Fatal(err)
				}
				var version int64
				if err := f.db.Pool.QueryRow(t.Context(), "SELECT version FROM finance_settings").Scan(&version); err != nil {
					t.Fatal(err)
				}
				gate, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer gate.Rollback(context.Background())
				key := "admin-topup-expiring"
				operation := fmt.Sprintf("customer.topup.create:%d", f.customer)
				switch wait {
				case "customer":
					_, err = gate.Exec(t.Context(), "SELECT id FROM users WHERE id=$1 FOR UPDATE", f.customer)
				case "receipt":
					_, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("mutation:%d:admin:%s:%s", f.actor, operation, key))
				case "audit":
					_, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(741098544)")
					if err == nil {
						_, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION wait_topup_authority_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='balance.topup.invoice_created' THEN PERFORM pg_advisory_xact_lock(741098544); END IF; RETURN NEW; END $$; CREATE TRIGGER wait_topup_authority_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_topup_authority_audit()`)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				(customerAccountAuthorityFixture{adminBalanceFixture: f}).expireSoon(t, secret)
				headers := map[string]string{"Idempotency-Key": key}
				cookie := f.cookie
				if secret != "" {
					cookie = ""
					headers["Authorization"] = "Bearer " + secret
				}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					done <- requestFrom(f.h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/topups", f.customer), map[string]any{"amount_cents": 1234, "expected_version": version, "note": "等待后过期"}, "192.0.2.90:5678", cookie, headers)
				}()
				waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
				waitBalanceCredentialExpiry(t, f, secret)
				if err = gate.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				expect(t, awaitBalanceResponse(t, done), 401)
				var bills, receipts, audits int
				if err = f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM balance_topups WHERE user_id=$1),(SELECT count(*) FROM mutation_operations WHERE idempotency_key=$2),(SELECT count(*) FROM audit_events WHERE action='balance.topup.invoice_created')`, f.customer, key).Scan(&bills, &receipts, &audits); err != nil {
					t.Fatal(err)
				}
				if bills+receipts+audits != 0 {
					t.Fatal("expired admin issued bill", bills, receipts, audits)
				}
			})
		}
	}
}
