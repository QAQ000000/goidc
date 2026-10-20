package httpapi

import (
	"fmt"
	"strings"
	"testing"
)

func TestTransactionCreditSourcesCashDatesAndCustomerScope(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "credit-entries-root", "super_admin")
	staffID, staff := user(t, db, h, "credit-entries-staff", "admin")
	uid, _ := user(t, db, h, "credit-entries-customer", "customer")
	other, _ := user(t, db, h, "credit-entries-other", "customer")
	contentRole(t, db, staffID, "invoices.view")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE administrators SET customer_scope='owned',customer_ownerless=false,sales_enabled=true WHERE id=$1", staffID)
	exec("UPDATE users SET sales_owner_id=$2 WHERE id=$1", uid, staffID)
	var credit, fallback, gateway int64
	for _, entry := range []struct {
		name   string
		amount int64
		target *int64
	}{{"credit", 1200, &credit}, {"fallback", 300, &fallback}, {"gateway", 700, &gateway}} {
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at)
VALUES($1,$2,'paid',$3,'2026-01-01T15:00:00Z') RETURNING id`, "credit-entry-"+entry.name, uid, entry.amount).Scan(entry.target); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,completed_at)
VALUES('mock','credit-entry-gateway',$1,$2,700,'succeeded','2026-01-01T15:00:00Z')`, gateway, uid)
	exec(`INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,expected_version,idempotency_key,created_at)
VALUES($1,$2,1200,700,1,'credit-entry-consumption','2026-01-01T15:00:00Z')`, credit, uid)
	for i, received := range []string{"2026-01-01T15:59:59Z", "2026-01-01T16:00:00Z"} {
		exec(`INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,note,idempotency_key,expected_version,version_after,remaining_after,created_at)
VALUES($1,$2,$3,$4,$5,$6,'private-proof-note',$5,$7,$8,$9,'2026-02-15T00:00:00Z')`, credit, uid, rootID, 200+100*i, fmt.Sprintf("private-bank-receipt-%d", i), received, i+1, i+2, 1000-300*i)
	}
	exec("INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after) VALUES($1,'topup',1000,1000)", uid)
	// The independent other customer's cash must not leak in items or totals.
	var foreignInvoice int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at)
VALUES('credit-entry-foreign',$1,'paid',99999,now()) RETURNING id`, other).Scan(&foreignInvoice); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,completed_at)
VALUES('mock','credit-entry-foreign',$1,$2,99999,'succeeded',now())`, foreignInvoice, other)
	get := func(path, cookie string) map[string]any {
		t.Helper()
		w := request(h, "GET", "/api/v1/admin/transactions/"+path, nil, cookie, "")
		expect(t, w, 200)
		if strings.Contains(w.Body.String(), "private-proof-note") || strings.Contains(w.Body.String(), "private-bank-receipt") {
			t.Fatal("transaction projection exposed private credit evidence")
		}
		return data(t, w)
	}
	page := get("page?limit=2", staff)
	if page["total"] != float64(6) || page["limit"] != float64(2) || page["offset"] != float64(0) || len(page["items"].([]any)) != 2 {
		t.Fatalf("backward compatible pagination: %v", page)
	}
	seen := map[string]int{}
	for _, raw := range get("page?limit=100", staff)["items"].([]any) {
		row := raw.(map[string]any)
		seen[row["source"].(string)]++
		if row["user_id"] != float64(uid) {
			t.Fatal("foreign credit fact", row)
		}
		if row["source"] == "invoice" && row["invoice_id"] != float64(fallback) {
			t.Fatal("credit/gateway receipt duplicated by invoice fallback", row)
		}
		if row["source"] == "credit_consumption" && (row["direction"] != "neutral" || row["channel"] != "credit") {
			t.Fatal("credit debt treated as cash", row)
		}
		if row["source"] == "credit_repayment" && (row["direction"] != "income" || row["channel"] != "credit_repayment") {
			t.Fatal("repayment provenance", row)
		}
	}
	if seen["credit_consumption"] != 1 || seen["credit_repayment"] != 2 || seen["invoice"] != 1 || seen["payment"] != 1 || seen["ledger"] != 1 {
		t.Fatal("missing or duplicate sources", seen)
	}
	summary := get("summary", staff)
	if summary["gateway_income_cents"] != float64(700) || summary["net_gateway_cents"] != float64(700) || summary["external_refund_cents"] != float64(0) || summary["credit_repayment_cents"] != float64(500) || summary["cash_income_cents"] != float64(1200) || summary["net_cash_cents"] != float64(1200) {
		t.Fatal("cash double counted debt/wallet or excluded receipt", summary)
	}
	day := "?channel=credit_repayment&from=2026-01-02&to=2026-01-02"
	datePage := get("page"+day, staff)
	if datePage["total"] != float64(1) || datePage["items"].([]any)[0].(map[string]any)["amount_cents"] != float64(300) {
		t.Fatal("repayment must bucket received_at at Shanghai midnight", datePage)
	}
	if v := get("summary"+day, staff); v["credit_repayment_cents"] != float64(300) || v["net_cash_cents"] != float64(300) || v["net_gateway_cents"] != float64(0) {
		t.Fatal("summary filters differ from entries", v)
	}
	if v := get("page?channel=credit_repayment&from=2026-02-15&to=2026-02-15", staff); v["total"] != float64(0) {
		t.Fatal("used repayment creation time instead of receipt time", v)
	}
	exec("UPDATE users SET sales_owner_id=NULL WHERE id=$1", uid)
	if v := get("page", staff); v["total"] != float64(0) {
		t.Fatal("ownership revocation retained transactions", v)
	}
	if v := get("summary", staff); v["net_cash_cents"] != float64(0) || v["credit_repayment_cents"] != float64(0) {
		t.Fatal("ownership revocation leaked cash totals", v)
	}
	if v := get(fmt.Sprintf("summary?customer_id=%d", uid), root); v["net_cash_cents"] != float64(1200) {
		t.Fatal("explicit customer summary scope", v)
	}
}
