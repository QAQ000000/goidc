package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

type adminCreditReadFixture struct {
	creditPortalFixture
	staffID                     int64
	staff                       string
	firstInvoice, secondInvoice int64
}

func newAdminCreditReadFixture(t *testing.T) adminCreditReadFixture {
	t.Helper()
	f := adminCreditReadFixture{creditPortalFixture: newCreditPortalFixture(t)}
	f.staffID, f.staff = user(t, f.db, f.h, "credit-read-staff", "admin")
	// Reading a credit page must not acquire a customers.view dependency.
	contentRole(t, f.db, f.staffID, "credit.view")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=ANY($2::bigint[])", f.staffID, []int64{f.aid, f.eid}); err != nil {
		t.Fatal(err)
	}
	f.firstInvoice = f.invoice(t, f.aid, f.a, "admin-credit-first", true)
	f.secondInvoice = f.invoice(t, f.aid, f.a, "admin-credit-second", true)
	other := f.invoice(t, f.bid, f.b, "admin-credit-other", true)
	f.repay(t, f.firstInvoice, 1000, 1, "admin-credit-first-repay")
	f.repay(t, f.secondInvoice, 3000, 1, "admin-credit-second-repay")
	f.repay(t, other, 1000, 1, "admin-credit-other-repay")
	return f
}

func assertAdminCreditReadPage(t *testing.T, response *httptest.ResponseRecorder, total, count int, customer int64, account bool) map[string]any {
	t.Helper()
	expect(t, response, http.StatusOK)
	page := data(t, response)
	items := page["items"].([]any)
	if page["total"] != float64(total) || len(items) != count {
		t.Fatalf("credit page total/rows: %s", response.Body.String())
	}
	for _, raw := range items {
		row := raw.(map[string]any)
		key := "user_id"
		if account {
			key = "id"
		}
		if customer != 0 && row[key] != float64(customer) {
			t.Fatalf("foreign credit record: %s", response.Body.String())
		}
	}
	return page
}

func TestAdminCreditReadsStrictCustomerContext(t *testing.T) {
	f := newAdminCreditReadFixture(t)
	for _, suffix := range []string{"", "/consumptions", "/repayments"} {
		t.Run(suffix, func(t *testing.T) {
			base := "/api/v1/admin/credit" + suffix
			account := suffix == ""
			for _, value := range []string{"", "0", "-1", "+1", " 1", "1 ", "1.0", "1x", "9223372036854775808"} {
				policyErrorCode(t, request(f.h, "GET", base+"?customer_id="+url.QueryEscape(value), nil, f.staff, ""), 400, "invalid_customer")
			}
			policyErrorCode(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&customer_id=%d", base, f.aid, f.aid), nil, f.staff, ""), 400, "invalid_customer")
			for _, id := range []int64{f.bid, f.staffID, 9223372036854775807} {
				policyErrorCode(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d", base, id), nil, f.staff, ""), 404, "not_found")
			}
			assertAdminCreditReadPage(t, request(f.h, "GET", base, nil, f.staff, ""), 2, 2, 0, account)
			total := 2
			if account {
				total = 1
			}
			assertAdminCreditReadPage(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&limit=1", base, f.aid), nil, f.staff, ""), total, 1, f.aid, account)
			assertAdminCreditReadPage(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&limit=1&offset=99", base, f.aid), nil, f.staff, ""), total, 0, f.aid, account)
			emptyTotal := 0
			if account {
				// A customer with no credit transactions still owns a real zero account.
				emptyTotal = 1
			}
			assertAdminCreditReadPage(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d", base, f.eid), nil, f.staff, ""), emptyTotal, emptyTotal, f.eid, account)
			assertAdminCreditReadPage(t, request(f.h, "GET", base, nil, f.root, ""), 3, 3, 0, account)
			expect(t, request(f.h, "GET", base, nil, "", ""), 401)
			expect(t, request(f.h, "GET", base, nil, f.a, ""), 401)
		})
	}
}

func TestAdminCreditReadsPreserveDatesFiltersAndFields(t *testing.T) {
	f := newAdminCreditReadFixture(t)
	// Shanghai midnight is UTC 16:00. Repayment creation deliberately differs
	// from receipt time so filtering must follow the actual cash receipt.
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE credit_invoice_payments SET created_at=CASE WHEN invoice_id=$1 THEN '2026-10-19T15:59:59Z'::timestamptz ELSE '2026-10-19T16:00:00Z'::timestamptz END WHERE user_id=$2`, f.firstInvoice, f.aid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE credit_repayments SET received_at=CASE WHEN invoice_id=$1 THEN '2026-10-19T16:00:00Z'::timestamptz ELSE '2026-10-19T15:59:59Z'::timestamptz END,created_at='2026-10-21T00:00:00Z' WHERE user_id=$2`, f.firstInvoice, f.aid); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("/api/v1/admin/credit/consumptions?customer_id=%d", f.aid)
	page := assertAdminCreditReadPage(t, request(f.h, "GET", base+"&date_from=2026-10-20&date_to=2026-10-20", nil, f.staff, ""), 1, 1, f.aid, false)
	row := page["items"].([]any)[0].(map[string]any)
	if row["id"] != float64(f.secondInvoice) || row["status"] != "repaid" || row["amount_cents"] != float64(3000) || row["remaining_cents"] != float64(0) {
		t.Fatal("consumption Shanghai day or existing fields changed", row)
	}
	creditPortalWhitelist(t, row, "id", "user_id", "customer_name", "invoice_no", "amount_cents", "remaining_cents", "status", "version", "created_at")
	assertAdminCreditReadPage(t, request(f.h, "GET", base+"&status=outstanding", nil, f.staff, ""), 1, 1, f.aid, false)
	assertAdminCreditReadPage(t, request(f.h, "GET", base+"&q=not-present", nil, f.staff, ""), 0, 0, f.aid, false)
	var firstInvoiceNo string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT invoice_no FROM invoices WHERE id=$1", f.firstInvoice).Scan(&firstInvoiceNo); err != nil {
		t.Fatal(err)
	}
	assertAdminCreditReadPage(t, request(f.h, "GET", base+"&q="+url.QueryEscape(firstInvoiceNo), nil, f.staff, ""), 1, 1, f.aid, false)
	base = fmt.Sprintf("/api/v1/admin/credit/repayments?customer_id=%d", f.aid)
	page = assertAdminCreditReadPage(t, request(f.h, "GET", base+"&date_from=2026-10-20&date_to=2026-10-20", nil, f.staff, ""), 1, 1, f.aid, false)
	row = page["items"].([]any)[0].(map[string]any)
	if row["invoice_id"] != float64(f.firstInvoice) || row["amount_cents"] != float64(1000) || row["receipt"] != "private-receipt-admin-credit-first-repay" || row["note"] != "private-internal-note" {
		t.Fatal("repayment receipt day or existing fields changed", row)
	}
	creditPortalWhitelist(t, row, "id", "invoice_id", "user_id", "customer_name", "invoice_no", "amount_cents", "receipt", "received_at", "note", "created_at")
	assertAdminCreditReadPage(t, request(f.h, "GET", base+"&q=admin-credit-second-repay", nil, f.staff, ""), 1, 1, f.aid, false)
	policyErrorCode(t, request(f.h, "GET", base+"&status=repaid", nil, f.staff, ""), 400, "invalid_repayment_status")
	for _, suffix := range []string{"/consumptions", "/repayments"} {
		base = "/api/v1/admin/credit" + suffix
		policyErrorCode(t, request(f.h, "GET", base+"?date_from=2026-99-01", nil, f.staff, ""), 400, "invalid_date")
		policyErrorCode(t, request(f.h, "GET", base+"?date_from=2026-10-20&date_to=2026-10-19", nil, f.staff, ""), 400, "invalid_date_range")
		policyErrorCode(t, request(f.h, "GET", base+"?status=invalid", nil, f.staff, ""), 400, "invalid_credit_status")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", f.aid); err != nil {
		t.Fatal(err)
	}
	base = fmt.Sprintf("/api/v1/admin/credit?customer_id=%d", f.aid)
	page = assertAdminCreditReadPage(t, request(f.h, "GET", base+"&status=disabled&q=credit-portal-a", nil, f.staff, ""), 1, 1, f.aid, true)
	row = page["items"].([]any)[0].(map[string]any)
	creditPortalWhitelist(t, row, "id", "username", "display_name", "email", "status", "limit_cents", "used_cents", "available_cents", "version", "updated_at", "frozen")
	assertAdminCreditReadPage(t, request(f.h, "GET", base+"&status=active", nil, f.staff, ""), 0, 0, f.aid, true)
	policyErrorCode(t, request(f.h, "GET", base+"&status=invalid", nil, f.staff, ""), 400, "invalid_status")
}

func TestAdminCreditReadsRecheckCurrentAuthority(t *testing.T) {
	f := newAdminCreditReadFixture(t)
	filters := url.Values{"customer_id": {fmt.Sprint(f.aid)}}
	reads := map[string]func() (map[string]any, error){
		"accounts": func() (map[string]any, error) {
			return f.db.AdminCreditAccountPage(t.Context(), f.staffID, filters, 25, 0)
		},
		"consumptions": func() (map[string]any, error) {
			return f.db.AdminCreditConsumptionsPage(t.Context(), f.staffID, filters, 25, 0)
		},
		"repayments": func() (map[string]any, error) {
			return f.db.AdminCreditRepaymentsPage(t.Context(), f.staffID, filters, 25, 0)
		},
	}
	for _, read := range reads {
		if _, err := read(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=NULL WHERE id=$1", f.aid); err != nil {
		t.Fatal(err)
	}
	for name, read := range reads {
		_, err := read()
		var failure *store.AccountBusinessError
		if !errors.As(err, &failure) || failure.Status != 404 || failure.Code != "not_found" {
			t.Fatalf("%s retained withdrawn customer: %v", name, err)
		}
	}
	contentRole(t, f.db, f.staffID, "customers.view")
	for name, read := range reads {
		_, err := read()
		var failure *store.AccountBusinessError
		if !errors.As(err, &failure) || failure.Status != 403 || failure.Code != "permission_denied" {
			t.Fatalf("%s retained withdrawn credit permission: %v", name, err)
		}
	}
	for _, suffix := range []string{"", "/consumptions", "/repayments"} {
		expect(t, request(f.h, "GET", "/api/v1/admin/credit"+suffix, nil, f.staff, ""), 403)
	}
}
