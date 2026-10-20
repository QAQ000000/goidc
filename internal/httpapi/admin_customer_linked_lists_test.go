package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type customerLinkedListsFixture struct {
	db                            *store.Store
	h                             http.Handler
	root, staff, customer         string
	rootID, staffID, owner, other int64
	empty                         int64
}

func newCustomerLinkedListsFixture(t *testing.T) customerLinkedListsFixture {
	t.Helper()
	db, h := testServer(t)
	f := customerLinkedListsFixture{db: db, h: h}
	f.rootID, f.root = user(t, db, h, "linked-lists-root", "super_admin")
	f.staffID, f.staff = user(t, db, h, "linked-lists-staff", "admin")
	f.owner, f.customer = user(t, db, h, "linked-lists-owner", "customer")
	f.other, _ = user(t, db, h, "linked-lists-other", "customer")
	f.empty, _ = user(t, db, h, "linked-lists-empty", "customer")
	// A page's own read permission suffices; these readers have no customers.view.
	contentRole(t, db, f.staffID, "orders.view", "invoices.view", "services.view", "tickets.view", "customer_files.view", "suppliers.view")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=ANY($2::bigint[])", f.staffID, []int64{f.owner, f.empty}); err != nil {
		t.Fatal(err)
	}
	pid := product(t, db, 10)
	for n, owner := range []int64{f.owner, f.other} {
		for index := range 2 {
			var oid, item, sid, iid int64
			if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash) VALUES('LINK-'||gen_random_uuid()::text,$1,1234,$2,'fixture') RETURNING id`, owner, fmt.Sprintf("linked-order-%d-%d", n, index)).Scan(&oid); err != nil {
				t.Fatal(err)
			}
			if err := db.Pool.QueryRow(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'关联商品','monthly',1,1234,0,1234) RETURNING id`, oid, pid).Scan(&item); err != nil {
				t.Fatal(err)
			}
			if err := db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,next_due_at,provision_mode,unit_number,recurring_price_cents,delivery_snapshot) VALUES($1,$2,$3,$4,'关联服务',$5,'monthly','active',now()+interval '1 month','supplier',1,1234,'{"supplier_id":"123"}') RETURNING id`, owner, oid, item, pid, fmt.Sprintf("linked-%d-%d.example", n, index)).Scan(&sid); err != nil {
				t.Fatal(err)
			}
			if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,amount_cents) VALUES('LINK-'||gen_random_uuid()::text,$1,$2,1234) RETURNING id`, owner, oid).Scan(&iid); err != nil {
				t.Fatal(err)
			}
			for _, statement := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) VALUES($1,'关联续费','renew',1234,$2)`, []any{iid, sid}},
				{`INSERT INTO tickets(ticket_no,user_id,subject,body) VALUES('LINK-'||gen_random_uuid()::text,$1,'关联工单','正文')`, []any{owner}},
				{`INSERT INTO customer_files(customer_id,title,filename,mime_type,size_bytes,sha256,content,uploader_id) VALUES($1,'关联文件','linked.txt','text/plain',1,repeat('a',64),'x'::bytea,$2)`, []any{owner, f.rootID}},
			} {
				if _, err := db.Pool.Exec(t.Context(), statement.sql, statement.args...); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return f
}

var adminCustomerLinkedListPaths = []string{
	"orders", "orders/page", "supplier-orders", "renewal-orders", "supplier-renewals",
	"invoices", "invoices/page", "services", "services/page", "tickets", "tickets/page", "customer-files",
}

func assertLinkedListResult(t *testing.T, w *httptest.ResponseRecorder, total, count int, owner int64) {
	t.Helper()
	expect(t, w, http.StatusOK)
	var response struct {
		Data any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var rows []any
	if page, ok := response.Data.(map[string]any); ok {
		if page["total"] != float64(total) {
			t.Fatalf("total: %s", w.Body.String())
		}
		rows = page["items"].([]any)
	} else {
		rows = response.Data.([]any)
	}
	if len(rows) != count {
		t.Fatalf("row count: %s", w.Body.String())
	}
	for _, raw := range rows {
		row := raw.(map[string]any)
		id := row["user_id"]
		if id == nil {
			id = row["customer_id"]
		}
		if owner != 0 && id != float64(owner) {
			t.Fatalf("foreign customer record: %s", w.Body.String())
		}
	}
}

func TestAdminCustomerLinkedListsStrictContext(t *testing.T) {
	f := newCustomerLinkedListsFixture(t)
	for _, path := range adminCustomerLinkedListPaths {
		t.Run(path, func(t *testing.T) {
			base := "/api/v1/admin/" + path
			for _, value := range []string{"", "0", "-1", "+1", " 1", "1 ", "1.0", "1x", "9223372036854775808"} {
				policyErrorCode(t, request(f.h, "GET", base+"?customer_id="+url.QueryEscape(value), nil, f.staff, ""), 400, "invalid_customer")
			}
			policyErrorCode(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&customer_id=%d", base, f.owner, f.other), nil, f.staff, ""), 400, "invalid_customer")
			for _, id := range []int64{f.other, f.staffID, 9223372036854775807} {
				policyErrorCode(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d", base, id), nil, f.staff, ""), 404, "not_found")
			}
			if path == "tickets" {
				// The legacy array's omitted-filter contract is department-wide;
				// explicit customer context is the new scope-narrowing boundary.
				assertLinkedListResult(t, request(f.h, "GET", base, nil, f.staff, ""), 4, 4, 0)
			} else {
				assertLinkedListResult(t, request(f.h, "GET", base, nil, f.staff, ""), 2, 2, f.owner)
			}
			assertLinkedListResult(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&limit=1&offset=1", base, f.owner), nil, f.staff, ""), 2, 1, f.owner)
			assertLinkedListResult(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&offset=999", base, f.owner), nil, f.staff, ""), 2, 0, f.owner)
			assertLinkedListResult(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d", base, f.empty), nil, f.staff, ""), 0, 0, f.empty)
			assertLinkedListResult(t, request(f.h, "GET", base, nil, f.root, ""), 4, 4, 0)
			expect(t, request(f.h, "GET", base, nil, f.customer, ""), 401)
		})
	}
	// Customer list routes retain their original ownership behavior: irrelevant
	// admin filter parameters do not select another customer's commercial data.
	for _, path := range []string{"orders", "invoices", "services", "services/page", "tickets"} {
		assertLinkedListResult(t, request(f.h, "GET", "/api/v1/"+path+"?customer_id=&customer_id=invalid", nil, f.customer, ""), 2, 2, f.owner)
	}
	// The customer file route already supports a narrowing customer filter.
	assertLinkedListResult(t, request(f.h, "GET", fmt.Sprintf("/api/v1/customer-files?customer_id=%d", f.other), nil, f.customer, ""), 0, 0, f.owner)
	contentRole(t, f.db, f.staffID, "customers.view")
	for _, path := range adminCustomerLinkedListPaths {
		expect(t, request(f.h, "GET", "/api/v1/admin/"+path, nil, f.staff, ""), 403)
	}
}

func TestAdminCustomerLinkedListsRecheckResolvedScope(t *testing.T) {
	f := newCustomerLinkedListsFixture(t)
	ctx := t.Context()
	resolved, err := f.db.AdminCustomerFilter(ctx, f.staffID, url.Values{"customer_id": {fmt.Sprint(f.owner)}})
	if err != nil || resolved == nil || *resolved != f.owner {
		t.Fatal("resolve customer context", err)
	}
	if _, err = f.db.Pool.Exec(ctx, "UPDATE users SET sales_owner_id=NULL WHERE id=$1", f.owner); err != nil {
		t.Fatal(err)
	}
	// Simulate scope withdrawal after context resolution. The list's own final
	// SQL must still reject the now inaccessible customer, including legacy APIs.
	reads := map[string]func() (any, error){
		"orders": func() (any, error) {
			return f.db.OrderAccountList(ctx, store.OrderAccountListFilter{Actor: f.staffID, Admin: true, CustomerID: resolved, Limit: 25})
		},
		"orders/page": func() (any, error) {
			return f.db.OrderAccountList(ctx, store.OrderAccountListFilter{Actor: f.staffID, Admin: true, Page: true, CustomerID: resolved, Limit: 25})
		},
		"renewals": func() (any, error) {
			return f.db.RenewalAccountList(ctx, store.OrderAccountListFilter{Actor: f.staffID, Admin: true, CustomerID: resolved, Limit: 25})
		},
		"invoices": func() (any, error) {
			return f.db.InvoiceAccountList(ctx, store.InvoiceAccountListFilter{Actor: f.staffID, Admin: true, CustomerID: resolved, Limit: 25})
		},
		"invoices/page": func() (any, error) {
			return f.db.InvoiceAccountList(ctx, store.InvoiceAccountListFilter{Actor: f.staffID, Admin: true, Page: true, CustomerID: resolved, Limit: 25})
		},
		"services": func() (any, error) {
			return f.db.ServiceAccountList(ctx, store.ServiceAccountListFilter{Actor: f.staffID, Admin: true, CustomerID: resolved, Limit: 25})
		},
		"services/page": func() (any, error) {
			return f.db.ServiceAccountList(ctx, store.ServiceAccountListFilter{Actor: f.staffID, Admin: true, Page: true, CustomerID: resolved, Limit: 25})
		},
		"tickets": func() (any, error) { return f.db.TicketList(ctx, f.staffID, true, 25, 0, false, nil, "", resolved) },
		"tickets/page": func() (any, error) {
			return f.db.AdminTicketAccountPage(ctx, f.staffID, "", "", "", resolved, false, 0, 25, 0)
		},
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			result, err := read()
			if err != nil {
				t.Fatal(err)
			}
			assertEmptyLinkedListValue(t, result)
		})
	}
	r := httptest.NewRequest("GET", "/api/v1/admin/customer-files", nil).WithContext(ctx)
	r.Header.Set("Cookie", f.staff)
	if _, err = f.db.CustomerAccountFilePage(requestFileContext(r), f.staffID, true, resolved, "", 25, 0); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("file list retained resolved scope: %v", err)
	}
	// A new HTTP request reports the inaccessible customer as 404, not as a
	// successful empty customer. This distinguishes withdrawal from empty facts.
	for _, path := range adminCustomerLinkedListPaths {
		policyErrorCode(t, request(f.h, "GET", fmt.Sprintf("/api/v1/admin/%s?customer_id=%d", path, f.owner), nil, f.staff, ""), 404, "not_found")
	}
}

func assertEmptyLinkedListValue(t *testing.T, result any) {
	t.Helper()
	switch value := result.(type) {
	case []map[string]any:
		if len(value) != 0 {
			t.Fatal("withdrawn customer records returned", value)
		}
	case map[string]any:
		if value["total"] != int64(0) {
			t.Fatal("withdrawn customer total returned", value)
		}
	default:
		t.Fatalf("unexpected list type %T", result)
	}
}
