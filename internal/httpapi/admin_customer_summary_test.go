package httpapi

import (
	"fmt"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestAdminCustomerSummaryRealFactsAndPermissionSections(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "summary-root", "super_admin")
	staffID, staff := user(t, db, h, "summary-staff", "admin")
	uid, customer := user(t, db, h, "summary-customer", "customer")
	other, _ := user(t, db, h, "summary-other", "customer")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE users SET email_verified=true,phone_verified=false WHERE id=$1", uid)
	exec("INSERT INTO certification_profiles(user_id,status) VALUES($1,'approved')", uid)
	exec("UPDATE customer_credit_accounts SET limit_cents=5000,used_cents=500,frozen=true WHERE user_id=$1", uid)
	pid := product(t, db, 10)
	v, item := add(t, h, customer, pid, 1)
	w := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "summary-order-key")
	expect(t, w, 201)
	invoice := int64(data(t, w)["invoice_id"].(float64))
	exec("UPDATE services SET status='active',next_due_at=now()+interval '1 day' WHERE user_id=$1", uid)
	exec("UPDATE invoices SET due_at=now()-interval '1 day' WHERE id=$1", invoice)
	// A gateway topup is one external receipt and one wallet movement. Wallet
	// spending is not a second external receipt; foreign receipts must not leak.
	for _, entry := range []struct {
		user   int64
		number string
		amount int64
	}{{uid, "summary-topup", 1000}, {other, "summary-foreign", 99999}} {
		var id int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents,paid_at) VALUES($1,$2,'topup','paid',$3,now()) RETURNING id`, entry.number, entry.user, entry.amount).Scan(&id); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,completed_at) VALUES('mock',$1,$2,$3,$4,'succeeded',now())`, entry.number, id, entry.user, entry.amount)
	}
	exec("UPDATE users SET balance_cents=800 WHERE id=$1", uid)
	exec("INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after) VALUES($1,'topup',1000,1000),($1,'payment',-200,800)", uid)
	var creditInvoice int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at) VALUES('summary-credit',$1,'paid',500,now()) RETURNING id", uid).Scan(&creditInvoice); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,expected_version,idempotency_key) VALUES($1,$2,500,400,1,'summary-credit-pay')`, creditInvoice, uid)
	exec(`INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,idempotency_key,expected_version,version_after,remaining_after) VALUES($1,$2,$3,100,'summary-credit-receipt',now(),'summary-credit-repay',1,2,400)`, creditInvoice, uid, rootID)
	var department, ticket int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES('Summary','summary') RETURNING id").Scan(&department); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO tickets(ticket_no,user_id,subject,body,department_id) VALUES('summary-open',$1,'test','test',$2) RETURNING id", uid, department).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO tickets(ticket_no,user_id,subject,body,department_id,merged_ticket_id) VALUES('summary-merged',$1,'test','test',$2,$3)", uid, department, ticket)
	exec("INSERT INTO tickets(ticket_no,user_id,subject,body) VALUES('summary-hidden',$1,'hidden','hidden')", uid)
	path := fmt.Sprintf("/api/v1/admin/customers/%d/summary", uid)
	get := func(cookie string) map[string]any {
		t.Helper()
		w := request(h, "GET", path, nil, cookie, "")
		expect(t, w, 200)
		return data(t, w)
	}
	result := get(root)
	identity := result["customer"].(map[string]any)
	if identity["email_verified"] != true || identity["phone_verified"] != false || identity["last_login_at"] == nil || identity["profile_version"] != float64(1) || result["as_of"] == nil {
		t.Fatalf("identity facts: %v", result)
	}
	sections := result["sections"].(map[string]any)
	part := func(name string) map[string]any {
		t.Helper()
		section := sections[name].(map[string]any)
		if section["available"] != true || section["reason"] != nil {
			t.Fatalf("section %s: %v", name, section)
		}
		return section["data"].(map[string]any)
	}
	if part("services")["expiring_30_days"] != float64(1) || part("services")["groups"].([]any)[0].(map[string]any)["group_id"] != nil {
		t.Fatalf("service groups: %v", sections)
	}
	if part("invoices")["unpaid_count"] != float64(1) || part("invoices")["overdue_cents"] != float64(1333) {
		t.Fatalf("invoice facts: %v", sections)
	}
	if part("wallet")["balance_cents"] != float64(800) || part("wallet")["debits_cents"] != float64(200) || part("cash")["receipts_cents"] != float64(1100) || part("cash")["net_receipts_cents"] != float64(1100) {
		t.Fatalf("cash/wallet: %v", sections)
	}
	if part("certification")["status"] != "approved" || part("credit")["used_cents"] != float64(500) || part("credit")["frozen"] != true || part("referral")["attributed"] != false {
		t.Fatalf("credit/certification/referral: %v", sections)
	}
	if part("tickets")["total"] != float64(3) || part("tickets")["open"] != float64(2) || part("tickets")["merged_sources"] != float64(1) {
		t.Fatalf("ticket history: %v", sections)
	}
	// A permission is not implied by customers.view, and denial must not be a
	// fabricated zero or leak data through the summary endpoint.
	contentRole(t, db, staffID, "customers.view")
	result = get(staff)
	sections = result["sections"].(map[string]any)
	for name, raw := range sections {
		section := raw.(map[string]any)
		if section["available"] != false || section["reason"] != "permission_denied" || section["data"] != nil {
			t.Fatalf("denied %s: %v", name, section)
		}
	}
	contentRole(t, db, staffID, "customers.view", "tickets.view")
	exec("UPDATE administrators SET ticket_scope='departments',ticket_unassigned=false WHERE id=$1", staffID)
	exec("INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)", staffID, department)
	result = get(staff)
	sections = result["sections"].(map[string]any)
	if part("tickets")["total"] != float64(2) || part("tickets")["open"] != float64(1) {
		t.Fatalf("department scope: %v", sections)
	}
	// Revocation is reflected by the next request, including section grants.
	contentRole(t, db, staffID, "tickets.view")
	expect(t, request(h, "GET", path, nil, staff, ""), 403)
	expect(t, request(h, "GET", path, nil, customer, ""), 401)
	expect(t, request(h, "GET", path, nil, "", ""), 401)
}

func TestAdminCustomerSummaryScopeAndEmptyFacts(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "summary-scope-staff", "admin")
	uid, _ := user(t, db, h, "summary-scope-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/customers/%d/summary", uid)
	expect(t, request(h, "GET", path, nil, staff, ""), 404)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, uid); err != nil {
		t.Fatal(err)
	}
	w := request(h, "GET", path, nil, staff, "")
	expect(t, w, 200)
	sections := data(t, w)["sections"].(map[string]any)
	for name, key := range map[string]string{"services": "total", "invoices": "unpaid_count", "tickets": "total", "wallet": "credits_cents", "cash": "receipts_cents"} {
		section := sections[name].(map[string]any)
		if section["available"] != true || section["data"].(map[string]any)[key] != float64(0) {
			t.Fatalf("empty %s: %v", name, section)
		}
	}
	if sections["certification"].(map[string]any)["data"].(map[string]any)["status"] != "unverified" {
		t.Fatal("absent certification not explicit")
	}
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/customers/%d/summary", staffID), nil, staff, ""), 404)
	expect(t, request(h, "GET", "/api/v1/admin/customers/invalid/summary", nil, staff, ""), 400)
}

func TestAdminCustomerSummaryReferrerScope(t *testing.T) {
	db, h, referrer, _, customer, link, _, _ := setupAffiliateFixture(t)
	staffID, staff := user(t, db, h, "summary-referral-staff", "admin")
	var click int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO affiliate_clicks(link_id,session_hash) VALUES($1,repeat('c',64)) RETURNING id", link).Scan(&click); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO affiliate_attributions(user_id,account_id,link_id,click_id,first_clicked_at) SELECT $1,account_id,id,$3,now() FROM affiliate_links WHERE id=$2`, customer, link, click); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, customer); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/customers/%d/summary", customer)
	getReferral := func() map[string]any {
		t.Helper()
		w := request(h, "GET", path, nil, staff, "")
		expect(t, w, 200)
		return data(t, w)["sections"].(map[string]any)["referral"].(map[string]any)["data"].(map[string]any)
	}
	referral := getReferral()
	if referral["attributed"] != true || referral["referrer_customer_id"] != nil || referral["referrer_name"] != nil {
		t.Fatalf("foreign referrer leaked: %v", referral)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, referrer); err != nil {
		t.Fatal(err)
	}
	if referral = getReferral(); referral["referrer_customer_id"] != float64(referrer) || referral["referrer_name"] != "affiliate-referrer" {
		t.Fatalf("scoped referrer missing: %v", referral)
	}
}
