package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func directoryCustomQuery(t *testing.T, values map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return "custom_fields=" + url.QueryEscape(string(raw))
}

func TestCustomerDirectoryFilterInput(t *testing.T) {
	for _, query := range []string{
		"", "q=", "group_id=all&sales_owner_id=none", "group_id=none&sales_owner_id=15&status=disabled",
		"company=" + url.QueryEscape("  示例公司  ") + "&phone=138&qq=600&certification_status=approved&level_id=1",
		`custom_fields=%7B%22number_value%22%3A0%2C%22boolean_value%22%3Afalse%7D`,
		"q=" + strings.Repeat("中", 120),
		"limit=bad&offset=-1", // Existing pagination fallback remains compatible.
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/?"+query, nil)
		in, ok := customerDirectoryInput(w, r)
		if !ok {
			t.Fatalf("valid query %q rejected: %s", query, w.Body.String())
		}
		if strings.Contains(query, "company=") && in.Company != "示例公司" {
			t.Fatal("company filter was not trimmed", in)
		}
		if strings.Contains(query, "custom_fields=") && (in.CustomFields["number_value"] != float64(0) || in.CustomFields["boolean_value"] != false) {
			t.Fatal("typed zero or false filter lost", in)
		}
	}
	tooMany := map[string]any{}
	for n := 0; n < 21; n++ {
		tooMany[fmt.Sprintf("field_%d", n)] = "value"
	}
	invalid := []string{
		"unknown=x", "q=one&q=two", "company=x&company=y", "custom_fields=%7B%7D&custom_fields=%7B%7D", "q=%zz", "q=a;b",
		"company=", "phone=%20", "qq=", "company=%00", "company=%ff", "q=%ff", "q=" + url.QueryEscape(strings.Repeat("中", 121)),
		"company=" + strings.Repeat("x", 201), "phone=" + strings.Repeat("x", 81), "qq=" + strings.Repeat("x", 41),
		"group_id=-1", "sales_owner_id=0", "status=deleted", "certification_status=", "certification_status=unknown",
		"level_id=", "level_id=0", "level_id=-1", "level_id=all", "level_id=%2B1", "level_id=1.2", "level_id=9223372036854775808",
		directoryCustomQuery(t, tooMany), "custom_fields=" + url.QueryEscape(strings.Repeat(" ", 16385)),
	}
	for _, raw := range []string{`null`, `[]`, `true`, `{`, `{} {}`, `{"text_value":"a","text_value":"b"}`, `{"bad-key":"a"}`, `{"x":null}`, `{"x":[]}`, `{"x":{}}`, `{"x":""}`, `{"x":"  "}`, `{"x":"\u0000"}`, `{"x":1e999}`, "{\"x\":\"" + string([]byte{255}) + "\"}"} {
		invalid = append(invalid, "custom_fields="+url.QueryEscape(raw))
	}
	for _, query := range invalid {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.URL.RawQuery = query
		if _, ok := customerDirectoryInput(w, r); ok || w.Code != http.StatusBadRequest {
			t.Fatalf("invalid query %q accepted: status=%d body=%s", query, w.Code, w.Body.String())
		}
	}
}

func TestCustomerDirectoryAdvancedProfileFilters(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "directory-filter-root", "super_admin")
	staffID, staff := user(t, db, h, "directory-filter-staff", "admin")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID)
	defineProfileFields(t, db)
	ids := make([]int64, 7)
	for n := range ids {
		ids[n], _ = user(t, db, h, fmt.Sprintf("directory-filter-customer-%d", n), "customer")
		if n < 6 {
			exec("UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, ids[n])
		}
	}
	values, err := json.Marshal(validProfileFields())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{ids[0], ids[6]} {
		exec(`INSERT INTO customer_profiles(customer_id,company_name,phone,qq,custom_fields) VALUES($1,'上海示例公司','profile-138','600001',$2::jsonb)`, id, string(values))
	}
	exec(`INSERT INTO customer_profiles(customer_id,company_name,phone,qq,custom_fields) VALUES($1,'literal%_\company','literal%_\phone','literal%_\qq','{"text_value":"ok%_\\value"}')`, ids[5])
	// A missing profile falls back to the first nonempty contact, exactly as
	// the directory projection does. An existing blank profile does not.
	exec(`INSERT INTO customer_contacts(customer_id,email,company_name,phone_number) VALUES($1,'directory-fallback@example.test','联系公司','contact-139'),($2,'directory-blank@example.test','联系公司','contact-139')`, ids[1], ids[2])
	exec(`INSERT INTO customer_profiles(customer_id) VALUES($1)`, ids[2])
	const loginPhone = "+8613900177777"
	exec("UPDATE users SET phone=$2 WHERE id=$1", ids[0], loginPhone)
	for n, status := range []string{"approved", "pending", "rejected", "unverified"} {
		exec("INSERT INTO certification_profiles(user_id,status) VALUES($1,$2)", ids[n], status)
	}
	var before string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object('profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY customer_id) FROM customer_profiles p),'fields',(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM customer_custom_fields f),'contacts',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM customer_contacts c),'audits',(SELECT count(*) FROM audit_events),'operations',(SELECT count(*) FROM mutation_operations))::text`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	check := func(query, cookie string, total int, wantIDs ...int64) {
		t.Helper()
		w := request(h, "GET", "/api/v1/admin/customers/page?"+query, nil, cookie, "")
		expect(t, w, 200)
		page := data(t, w)
		if page["total"] != float64(total) || len(page["items"].([]any)) != len(wantIDs) {
			t.Fatalf("%s: %s", query, w.Body.String())
		}
		for n, row := range page["items"].([]any) {
			if row.(map[string]any)["id"] != float64(wantIDs[n]) {
				t.Fatalf("%s returned unexpected customer: %s", query, w.Body.String())
			}
		}
	}
	check("company="+url.QueryEscape("示例"), root, 2, ids[6], ids[0])
	check("company="+url.QueryEscape("示例"), staff, 1, ids[0])
	check("company="+url.QueryEscape("联系"), root, 1, ids[1])
	check("phone=contact-139", root, 1, ids[1])
	check("phone=profile-138", staff, 1, ids[0])
	check("phone="+url.QueryEscape(loginPhone), root, 0)
	check("qq=600001&company="+url.QueryEscape("示例")+"&phone=profile", staff, 1, ids[0])
	check("qq=600001&company="+url.QueryEscape("联系"), root, 0)
	for _, field := range []string{"company", "phone", "qq"} {
		check(field+"="+url.QueryEscape(`%_\`), root, 1, ids[5])
	}
	check(directoryCustomQuery(t, map[string]any{"text_value": `%_\`}), root, 1, ids[5])
	check("q=%25", staff, 6, ids[5], ids[4], ids[3], ids[2], ids[1], ids[0]) // Original q wildcard compatibility.
	check("certification_status=approved", staff, 1, ids[0])
	check("certification_status=pending", root, 1, ids[1])
	check("certification_status=rejected", root, 1, ids[2])
	check("certification_status=unverified", root, 4, ids[6], ids[5], ids[4], ids[3])
	check("qq=600001&limit=1&offset=100", root, 2)
	for _, fields := range []map[string]any{
		{"text_value": "kay"}, // Partial search intentionally ignores full-value ^ok regex.
		{"long_value": "文字"}, {"number_value": 0}, {"boolean_value": false}, {"date_value": "2024-02-29"}, {"select_value": "A"},
		{"number_value": 0, "boolean_value": false, "text_value": "okay"},
	} {
		check(directoryCustomQuery(t, fields), staff, 1, ids[0])
	}
	check(directoryCustomQuery(t, map[string]any{"text_value": "missing"}), root, 0)
	check(directoryCustomQuery(t, map[string]any{"select_value": "B"}), root, 0)
	check(directoryCustomQuery(t, map[string]any{"number_value": 0, "boolean_value": true}), root, 0)
	for _, fields := range []map[string]any{
		{"unknown_value": "x"}, {"number_value": "0"}, {"boolean_value": "false"}, {"date_value": "2023-02-29"}, {"date_value": "2024-2-29"}, {"select_value": "C"},
	} {
		expect(t, request(h, "GET", "/api/v1/admin/customers/page?"+directoryCustomQuery(t, fields), nil, root, ""), 400)
	}
	var after string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object('profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY customer_id) FROM customer_profiles p),'fields',(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM customer_custom_fields f),'contacts',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM customer_contacts c),'audits',(SELECT count(*) FROM audit_events),'operations',(SELECT count(*) FROM mutation_operations))::text`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("read-only customer filters changed profiles, definitions, contacts, audits or receipts")
	}
	exec("UPDATE customer_custom_fields SET active=false WHERE field_key='text_value'")
	expect(t, request(h, "GET", "/api/v1/admin/customers/page?"+directoryCustomQuery(t, map[string]any{"text_value": "okay"}), nil, root, ""), 400)
	exec("UPDATE admin_roles SET permissions=ARRAY['customers.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID)
	check(directoryCustomQuery(t, map[string]any{"number_value": 0}), staff, 1, ids[0])
	exec("UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID)
	expect(t, request(h, "GET", "/api/v1/admin/customers/page?company=x", nil, staff, ""), 403)
}

func TestCustomerDirectoryLevelFilterMatchesExistingEligibility(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "directory-level-root", "super_admin")
	staffID, staff := user(t, db, h, "directory-level-staff", "admin")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID)
	pid := product(t, db, 100)
	var level int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO customer_levels(name,min_income_cents,min_purchase_count,min_login_count,min_recent_login_count,min_renewal_count,min_recent_renewal_count) VALUES('directory-six-thresholds',1000,2,2,1,2,1) RETURNING id`).Scan(&level); err != nil {
		t.Fatal(err)
	}
	var eligible, hidden int64
	for n, variant := range []string{"eligible", "income", "purchase", "login", "recent-login", "renew", "recent-renew", "disabled", "hidden"} {
		uid, _ := user(t, db, h, "directory-level-"+variant, "customer")
		if variant == "eligible" {
			eligible = uid
		}
		if variant == "hidden" {
			hidden = uid
		} else {
			exec("UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, uid)
		}
		if variant == "disabled" {
			exec("UPDATE users SET status='disabled' WHERE id=$1", uid)
		}
		income, quantity, logins, renewals := 1000, 2, 2, 2
		if variant == "income" {
			income = 999
		}
		if variant == "purchase" {
			quantity = 1
		}
		if variant == "login" {
			logins = 1
		}
		if variant == "renew" {
			renewals = 1
		}
		exec("DELETE FROM audit_events WHERE actor_id=$1 AND action='auth.login_succeeded'", uid)
		for j := 0; j < logins; j++ {
			age := 60
			if variant != "recent-login" && j == 0 {
				age = 1
			}
			exec(`INSERT INTO audit_events(actor_id,action,entity_type,entity_id,created_at) VALUES($1,'auth.login_succeeded','user',$2,now()-$3::int*interval '1 day')`, uid, fmt.Sprint(uid), age)
		}
		var order, invoice int64
		key := fmt.Sprintf("directory-level-order-%d", n)
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES($1,$2,'paid',$3,$1,'fixture') RETURNING id`, key, uid, income).Scan(&order); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'level fixture','monthly',$3,100,0,100)`, order, pid, quantity)
		age := 1
		if variant == "recent-renew" {
			age = 60
		}
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,paid_at) VALUES($1,$2,$3,'paid',$4,now()-$5::int*interval '1 day') RETURNING id`, key, uid, order, income, age).Scan(&invoice); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < renewals; j++ {
			exec(`INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents) VALUES($1,'level fixture renewal','renew',100)`, invoice)
		}
	}
	for _, tc := range []struct {
		cookie string
		ids    []int64
	}{{root, []int64{hidden, eligible}}, {staff, []int64{eligible}}} {
		response := request(h, "GET", fmt.Sprintf("/api/v1/admin/customers/page?level_id=%d", level), nil, tc.cookie, "")
		expect(t, response, 200)
		page := data(t, response)
		if page["total"] != float64(len(tc.ids)) || len(page["items"].([]any)) != len(tc.ids) {
			t.Fatalf("six AND thresholds or scope mismatch: %s", response.Body.String())
		}
		for n, row := range page["items"].([]any) {
			if row.(map[string]any)["id"] != float64(tc.ids[n]) {
				t.Fatal("ineligible customer matched", row)
			}
		}
		response = request(h, "GET", "/api/v1/admin/customer-levels?q=directory-six-thresholds", nil, tc.cookie, "")
		expect(t, response, 200)
		rule := data(t, response)["items"].([]any)[0].(map[string]any)
		if rule["matching_customer_count"] != page["total"] || data(t, response)["recent_window_days"] != float64(30) {
			t.Fatal("level page and customer filter eligibility differ", rule, page)
		}
	}
	exec("UPDATE admin_roles SET permissions=ARRAY['customers.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/customers/page?level_id=%d", level), nil, staff, ""), 200)
	expect(t, request(h, "GET", "/api/v1/admin/customer-levels", nil, staff, ""), 403)
	exec("UPDATE admin_roles SET permissions=ARRAY['customer_levels.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID)
	expect(t, request(h, "GET", "/api/v1/admin/customer-levels", nil, staff, ""), 200)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/customers/page?level_id=%d", level), nil, staff, ""), 403)
	exec("UPDATE customer_levels SET active=false WHERE id=$1", level)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/customers/page?level_id=%d", level), nil, root, ""), 400)
	expect(t, request(h, "GET", "/api/v1/admin/customers/page?level_id=9223372036854775807", nil, root, ""), 400)
}
