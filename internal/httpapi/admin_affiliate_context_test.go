package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type adminAffiliateReadFixture struct {
	affiliateSettlementFixture
	staffID, emptyID, outsideID int64
	staff, empty                string
}

func newAdminAffiliateReadFixture(t *testing.T) adminAffiliateReadFixture {
	t.Helper()
	f := adminAffiliateReadFixture{affiliateSettlementFixture: settlementFixture(t)}
	f.staffID, f.staff = user(t, f.db, f.h, "affiliate-read-staff", "admin")
	// Affiliate pages have no customers.view prerequisite.
	contentRole(t, f.db, f.staffID, "affiliates.view")
	f.emptyID, f.empty = user(t, f.db, f.h, "affiliate-read-empty", "customer")
	f.outsideID, _ = user(t, f.db, f.h, "affiliate-read-outside", "customer")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=ANY($2::bigint[])", f.staffID, []int64{f.ownerID, f.customerID, f.emptyID}); err != nil {
		t.Fatal(err)
	}
	return f
}

func assertAdminAffiliatePage(t *testing.T, response *httptest.ResponseRecorder, total, count int) map[string]any {
	t.Helper()
	expect(t, response, http.StatusOK)
	page := data(t, response)
	if page["total"] != float64(total) || len(page["items"].([]any)) != count {
		t.Fatalf("affiliate page total/rows: %s", response.Body.String())
	}
	return page
}

func TestAdminAffiliatePagesStrictCustomerContext(t *testing.T) {
	f := newAdminAffiliateReadFixture(t)
	f.purchase(t, 1)
	if _, err := f.db.SettleAffiliateCommissions(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	f.withdraw(t, 50, "affiliate-read-withdraw")
	for _, kind := range []string{"accounts", "attributions", "withdrawals"} {
		t.Run(kind, func(t *testing.T) {
			base := "/api/v1/admin/affiliate/" + kind + "/page"
			for _, query := range []string{"customer_id=%zz", "customer_id=1;foo", "customer_id=%", "customer_role=%zz"} {
				policyErrorCode(t, request(f.h, "GET", base+"?"+query, nil, f.staff, ""), 400, "invalid_affiliate_filter")
			}
			for _, value := range []string{"", "0", "-1", "+1", " 1", "1 ", "1.0", "1x", "9223372036854775808"} {
				policyErrorCode(t, request(f.h, "GET", base+"?customer_id="+url.QueryEscape(value), nil, f.staff, ""), 400, "invalid_customer")
			}
			policyErrorCode(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&customer_id=%d", base, f.ownerID, f.ownerID), nil, f.staff, ""), 400, "invalid_customer")
			for _, id := range []int64{f.outsideID, f.staffID, 9223372036854775807} {
				policyErrorCode(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d", base, id), nil, f.staff, ""), 404, "not_found")
			}
			assertAdminAffiliatePage(t, request(f.h, "GET", base, nil, f.staff, ""), 1, 1)
			assertAdminAffiliatePage(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&limit=1", base, f.ownerID), nil, f.staff, ""), 1, 1)
			assertAdminAffiliatePage(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&limit=1&offset=99", base, f.ownerID), nil, f.staff, ""), 1, 0)
			page := assertAdminAffiliatePage(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d", base, f.emptyID), nil, f.staff, ""), 0, 0)
			if kind == "withdrawals" && page["pending_count"] != float64(0) {
				t.Fatal("pending badge escaped exact customer", page)
			}
			expect(t, request(f.h, "GET", base, nil, "", ""), 401)
			expect(t, request(f.h, "GET", base, nil, f.empty, ""), 401)
			if kind != "attributions" {
				policyErrorCode(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&customer_role=promoter", base, f.ownerID), nil, f.staff, ""), 400, "invalid_customer_role")
			}
		})
	}
	base := fmt.Sprintf("/api/v1/admin/affiliate/withdrawals/page?customer_id=%d&search=missing&status=paid", f.ownerID)
	page := assertAdminAffiliatePage(t, request(f.h, "GET", base, nil, f.staff, ""), 0, 0)
	if page["pending_count"] != float64(1) {
		t.Fatal("pending badge lost existing search/status independence", page)
	}
}

func TestAdminAffiliateAttributionsCustomerRoleAndCounterpartyScope(t *testing.T) {
	f := newAdminAffiliateReadFixture(t)
	base := "/api/v1/admin/affiliate/attributions/page"
	for _, tc := range []struct {
		id    int64
		role  string
		total int
	}{
		{f.ownerID, "promoter", 1}, {f.ownerID, "referred", 0}, {f.ownerID, "either", 1},
		{f.customerID, "promoter", 0}, {f.customerID, "referred", 1}, {f.customerID, "either", 1},
	} {
		assertAdminAffiliatePage(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&customer_role=%s", base, tc.id, tc.role), nil, f.staff, ""), tc.total, tc.total)
	}
	for _, query := range []string{"customer_role=either", fmt.Sprintf("customer_id=%d&customer_role=", f.customerID), fmt.Sprintf("customer_id=%d&customer_role=either&customer_role=either", f.customerID), fmt.Sprintf("customer_id=%d&customer_role=unknown", f.customerID)} {
		policyErrorCode(t, request(f.h, "GET", base+"?"+query, nil, f.staff, ""), 400, "invalid_customer_role")
	}
	// A visible referred customer does not expose an out-of-scope promoter.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=NULL WHERE id=$1", f.ownerID); err != nil {
		t.Fatal(err)
	}
	response := request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&customer_role=referred", base, f.customerID), nil, f.staff, "")
	page := assertAdminAffiliatePage(t, response, 1, 1)
	row := page["items"].([]any)[0].(map[string]any)
	for _, key := range []string{"account_id", "affiliate_user_id", "affiliate_username", "link_id"} {
		if row[key] != nil {
			t.Fatalf("hidden promoter leaked %s: %s", key, response.Body.String())
		}
	}
	if strings.Contains(response.Body.String(), "affiliate-referrer") {
		t.Fatal("hidden promoter name leaked", response.Body.String())
	}
	assertAdminAffiliatePage(t, request(f.h, "GET", base+"?search=affiliate-referrer", nil, f.staff, ""), 0, 0)
	// Conversely, owning the promoter cannot expose an invisible referred row.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=CASE WHEN id=$1::bigint THEN $2::bigint ELSE NULL::bigint END WHERE id=ANY($3::bigint[])", f.ownerID, f.staffID, []int64{f.ownerID, f.customerID}); err != nil {
		t.Fatal(err)
	}
	assertAdminAffiliatePage(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&customer_role=promoter", base, f.ownerID), nil, f.staff, ""), 0, 0)
	assertAdminAffiliatePage(t, request(f.h, "GET", base+"?search=affiliate-referred", nil, f.staff, ""), 0, 0)
}

func TestAdminCustomerAffiliatePageCompatibilityAndInvoiceScope(t *testing.T) {
	f := newAdminAffiliateReadFixture(t)
	invoice, _ := f.purchase(t, 1)
	contentRole(t, f.db, f.staffID, "customers.view")
	base := fmt.Sprintf("/api/v1/admin/customers/%d/affiliate", f.ownerID)
	legacy := request(f.h, "GET", base, nil, f.staff, "")
	expect(t, legacy, 200)
	items := listData(t, legacy)
	page := assertAdminAffiliatePage(t, request(f.h, "GET", base+"/page?limit=1", nil, f.staff, ""), 3, 1)
	if len(items) != 3 || page["limit"] != float64(1) || page["offset"] != float64(0) {
		t.Fatal("legacy array or new page envelope changed", items, page)
	}
	assertAdminAffiliatePage(t, request(f.h, "GET", base+"/page?offset=99", nil, f.staff, ""), 3, 0)
	emptyBase := fmt.Sprintf("/api/v1/admin/customers/%d/affiliate", f.emptyID)
	assertAdminAffiliatePage(t, request(f.h, "GET", emptyBase+"/page", nil, f.staff, ""), 0, 0)
	for _, suffix := range []string{"", "/page"} {
		policyErrorCode(t, request(f.h, "GET", fmt.Sprintf("/api/v1/admin/customers/%d/affiliate%s", f.outsideID, suffix), nil, f.staff, ""), 404, "not_found")
		policyErrorCode(t, request(f.h, "GET", fmt.Sprintf("/api/v1/admin/customers/%d/affiliate%s", f.staffID, suffix), nil, f.staff, ""), 404, "not_found")
	}
	var invoiceNo string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT invoice_no FROM invoices WHERE id=$1", invoice).Scan(&invoiceNo); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(legacy.Body.String(), invoiceNo) {
		t.Fatal("visible invoice identifier missing")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=NULL WHERE id=$1", f.customerID); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/page"} {
		response := request(f.h, "GET", base+suffix, nil, f.staff, "")
		expect(t, response, 200)
		if strings.Contains(response.Body.String(), invoiceNo) || strings.Contains(response.Body.String(), "affiliate-referred") {
			t.Fatal("customer activity leaked out-of-scope counterparty", response.Body.String())
		}
	}
}

func TestAdminAffiliateReadsKeepEntryPermissionsSeparateAndCurrent(t *testing.T) {
	f := newAdminAffiliateReadFixture(t)
	customerPath := fmt.Sprintf("/api/v1/admin/customers/%d/affiliate", f.ownerID)
	for _, suffix := range []string{"", "/page"} {
		expect(t, request(f.h, "GET", customerPath+suffix, nil, f.staff, ""), 403)
	}
	contentRole(t, f.db, f.staffID, "customers.view")
	for _, kind := range []string{"accounts", "attributions", "withdrawals"} {
		expect(t, request(f.h, "GET", "/api/v1/admin/affiliate/"+kind+"/page", nil, f.staff, ""), 403)
	}
	for _, suffix := range []string{"", "/page"} {
		expect(t, request(f.h, "GET", customerPath+suffix, nil, f.staff, ""), 200)
	}
	assertDenied := func(err error, status int, code string) {
		t.Helper()
		var failure *store.AccountBusinessError
		if !errors.As(err, &failure) || failure.Status != status || failure.Code != code {
			t.Fatalf("store retained withdrawn authority: %v", err)
		}
	}
	_, err := f.db.AdminAffiliatePageWithFilters(t.Context(), f.staffID, "accounts", nil, 25, 0)
	assertDenied(err, 403, "permission_denied")
	contentRole(t, f.db, f.staffID, "affiliates.view")
	_, err = f.db.AdminCustomerAffiliatePage(t.Context(), f.staffID, f.ownerID, 25, 0)
	assertDenied(err, 403, "permission_denied")
	contentRole(t, f.db, f.staffID, "affiliates.view", "customers.view")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=NULL WHERE id=$1", f.ownerID); err != nil {
		t.Fatal(err)
	}
	_, err = f.db.AdminAffiliatePageWithFilters(t.Context(), f.staffID, "accounts", url.Values{"customer_id": {fmt.Sprint(f.ownerID)}}, 25, 0)
	assertDenied(err, 404, "not_found")
	_, err = f.db.AdminCustomerAffiliatePage(t.Context(), f.staffID, f.ownerID, 25, 0)
	assertDenied(err, 404, "not_found")
}

// Change customer ownership after the total query has completed, before rows
// are read. Both results must retain one snapshot; the next read must deny it.
type affiliatePageSnapshotTrace struct {
	once atomic.Bool
	move func()
}

type affiliatePageSnapshotTraceKey struct{}

func (trace *affiliatePageSnapshotTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, query pgx.TraceQueryStartData) context.Context {
	count := strings.HasPrefix(query.SQL, "SELECT count(*) FROM affiliate_") || strings.HasSuffix(query.SQL, "SELECT count(*) FROM activity")
	return context.WithValue(ctx, affiliatePageSnapshotTraceKey{}, count)
}

func (trace *affiliatePageSnapshotTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if count, _ := ctx.Value(affiliatePageSnapshotTraceKey{}).(bool); count && trace.once.CompareAndSwap(false, true) {
		trace.move()
	}
}

func TestAdminAffiliateReadsUseOneSnapshotForTotalAndRows(t *testing.T) {
	f := newAdminAffiliateReadFixture(t)
	f.purchase(t, 1)
	if _, err := f.db.SettleAffiliateCommissions(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	f.withdraw(t, 50, "affiliate-read-snapshot-withdraw")
	contentRole(t, f.db, f.staffID, "affiliates.view", "customers.view")
	for _, kind := range []string{"accounts", "attributions", "withdrawals", "customer-page", "customer-legacy"} {
		t.Run(kind, func(t *testing.T) {
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=$2", f.staffID, f.ownerID); err != nil {
				t.Fatal(err)
			}
			trace := &affiliatePageSnapshotTrace{move: func() {
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=NULL WHERE id=$1", f.ownerID); err != nil {
					t.Fatal(err)
				}
			}}
			config := f.db.Pool.Config()
			config.ConnConfig.Tracer = trace
			pool, err := pgxpool.NewWithConfig(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			db := &store.Store{Pool: pool}
			read := func() (int64, int, error) {
				if kind == "customer-page" {
					page, err := db.AdminCustomerAffiliatePage(t.Context(), f.staffID, f.ownerID, 25, 0)
					return page.Total, len(page.Items), err
				}
				if kind == "customer-legacy" {
					items, err := db.AdminCustomerAffiliate(t.Context(), f.staffID, f.ownerID, 25, 0)
					return int64(len(items)), len(items), err
				}
				page, err := db.AdminAffiliatePageWithFilters(t.Context(), f.staffID, kind, url.Values{"customer_id": {fmt.Sprint(f.ownerID)}}, 25, 0)
				return page.Total, len(page.Items), err
			}
			total, count, err := read()
			want := 1
			if strings.HasPrefix(kind, "customer-") {
				want = 4 // promoter account, attribution, commission, withdrawal.
			}
			if err != nil || total != int64(want) || count != want || !trace.once.Load() {
				t.Fatalf("snapshot changed across queries: total=%d rows=%d trace=%v error=%v", total, count, trace.once.Load(), err)
			}
			_, _, err = read()
			var failure *store.AccountBusinessError
			if !errors.As(err, &failure) || failure.Status != 404 || failure.Code != "not_found" {
				t.Fatalf("next snapshot retained former customer: %v", err)
			}
		})
	}
}
