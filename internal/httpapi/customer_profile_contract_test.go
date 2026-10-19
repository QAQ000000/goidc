package httpapi

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func customerProfileVersion(t *testing.T, db *store.Store, id int64) int64 {
	t.Helper()
	var version int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT version FROM customer_profiles WHERE customer_id=$1", id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}
func customerFieldVersion(t *testing.T, db *store.Store) int64 {
	t.Helper()
	var version int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT version FROM customer_field_catalogue WHERE id=1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}
func createProfileCustomerBody(name string, values map[string]any, version int64) map[string]any {
	return map[string]any{"username": name, "email": name + "@example.test", "password": "password123", "display_name": "原姓名", "company_name": "保留公司", "phone": "13800138000", "address": "保留地址", "receive_marketing": false, "custom_fields": values, "custom_fields_version": version}
}
func defineProfileFields(t *testing.T, db *store.Store) {
	t.Helper()
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO customer_custom_fields(field_key,label,field_type,required,choices,validation_pattern) VALUES
 ('text_value','文字','text',true,'{}','^ok'),
 ('long_value','段落','textarea',false,'{}',''),
 ('number_value','数字','number',true,'{}',''),
 ('date_value','日期','date',true,'{}',''),
 ('select_value','选择','select',true,ARRAY['A','B'],''),
 ('boolean_value','开关','boolean',true,'{}','')`); err != nil {
		t.Fatal(err)
	}
}
func validProfileFields() map[string]any {
	return map[string]any{"text_value": "okay", "long_value": "段落\n文字", "number_value": 0, "date_value": "2024-02-29", "select_value": "A", "boolean_value": false}
}

func TestCustomerFieldDefinitionsReadOnlyPaginationAndPermissions(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "definitions-root", "super_admin")
	staffID, staff := user(t, db, h, "definitions-staff", "admin")
	_, customer := user(t, db, h, "definitions-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO customer_custom_fields(field_key,label,field_type,active,sort_order) SELECT 'field_'||i,'字段'||i,'text',i<=121,i FROM generate_series(1,122) i`); err != nil {
		t.Fatal(err)
	}
	for _, permission := range []string{"customers.view", "customers.manage"} {
		if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY[$2] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID, permission); err != nil {
			t.Fatal(err)
		}
		first := request(h, "GET", "/api/v1/admin/customer-field-definitions?limit=100", nil, staff, "")
		expect(t, first, 200)
		page := data(t, first)
		if page["total"] != float64(121) || len(page["items"].([]any)) != 100 || page["definitions_version"] != float64(customerFieldVersion(t, db)) {
			t.Fatal("definitions pagination mismatch", page)
		}
		second := data(t, request(h, "GET", "/api/v1/admin/customer-field-definitions?limit=100&offset=100", nil, staff, ""))
		if len(second["items"].([]any)) != 21 || second["definitions_version"] != page["definitions_version"] {
			t.Fatal("definitions second page mismatch", second)
		}
		if permission == "customers.view" {
			expect(t, request(h, "GET", "/api/v1/admin/customer-fields", nil, staff, ""), 403)
		}
	}
	expect(t, request(h, "GET", "/api/v1/admin/customer-field-definitions", nil, customer, ""), 401)
	expect(t, request(h, "GET", "/api/v1/admin/customer-field-definitions", nil, root, ""), 200)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/customer-field-definitions", nil, staff, ""), 403)
}

func TestCustomerProfileFieldValidationAndInactiveHistory(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "field-values-root", "super_admin")
	defineProfileFields(t, db)
	version := customerFieldVersion(t, db)
	body := createProfileCustomerBody("field-values-customer", validProfileFields(), version)
	expect(t, request(h, "POST", "/api/v1/admin/customers", body, root, ""), 400)
	created := request(h, "POST", "/api/v1/admin/customers", body, root, "field-values-create-key")
	expect(t, created, 201)
	id := int64(data(t, created)["id"].(float64))
	path := fmt.Sprintf("/api/v1/admin/customers/%d", id)
	detail := data(t, request(h, "GET", path, nil, root, ""))
	saved := detail["custom_fields"].(map[string]any)
	if saved["number_value"] != float64(0) || saved["boolean_value"] != false || saved["date_value"] != "2024-02-29" {
		t.Fatal("typed zero/false/date not retained", saved)
	}
	cases := []struct {
		key   string
		value any
	}{
		{"text_value", ""}, {"text_value", "   "}, {"text_value", nil}, {"text_value", "bad-pattern"}, {"date_value", "2023-02-29"}, {"date_value", "2024-2-29"}, {"date_value", 7}, {"number_value", "0"}, {"boolean_value", "false"}, {"select_value", "C"}, {"select_value", ""}, {"number_value", nil},
	}
	for i, tc := range cases {
		fields := validProfileFields()
		fields[tc.key] = tc.value
		patch := map[string]any{"expected_version": 1, "profile": map[string]any{"custom_fields_version": version, "custom_fields": fields}}
		expect(t, request(h, "PATCH", path, patch, root, fmt.Sprintf("field-invalid-%d-key", i)), 400)
		if customerProfileVersion(t, db, id) != 1 {
			t.Fatal("invalid field changed profile version")
		}
	}
	for i, fields := range []any{nil, map[string]any{}, map[string]any{"unknown_field": "value"}} {
		bad := createProfileCustomerBody(fmt.Sprintf("field-required-%d", i), nil, version)
		bad["custom_fields"] = fields
		expect(t, request(h, "POST", "/api/v1/admin/customers", bad, root, fmt.Sprintf("field-required-%d-key", i)), 400)
	}
	noValues := createProfileCustomerBody("field-required-omitted", nil, version)
	delete(noValues, "custom_fields")
	expect(t, request(h, "POST", "/api/v1/admin/customers", noValues, root, "field-required-omitted-key"), 400)
	expect(t, request(h, "PATCH", path, map[string]any{"expected_version": 1, "profile": map[string]any{"admin_note": "仅改备注", "custom_fields_version": version}}, root, "field-sparse-update-key"), 200)
	detail = data(t, request(h, "GET", path, nil, root, ""))
	if detail["company_name"] != "保留公司" || detail["address"] != "保留地址" || detail["receive_marketing"] != false || detail["custom_fields"].(map[string]any)["long_value"] != "段落\n文字" {
		t.Fatal("sparse update dropped old profile", detail)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE customer_custom_fields SET active=false WHERE field_key='text_value'"); err != nil {
		t.Fatal(err)
	}
	current := customerFieldVersion(t, db)
	fields := validProfileFields()
	delete(fields, "text_value")
	fields["long_value"] = nil
	patch := map[string]any{"expected_version": 2, "profile": map[string]any{"custom_fields_version": version, "custom_fields": fields}}
	expect(t, request(h, "PATCH", path, patch, root, "field-stale-definitions-key"), 409)
	patch["profile"].(map[string]any)["custom_fields_version"] = current
	expect(t, request(h, "PATCH", path, patch, root, "field-inactive-preserve-key"), 200)
	detail = data(t, request(h, "GET", path, nil, root, ""))
	saved = detail["custom_fields"].(map[string]any)
	if saved["text_value"] != "okay" || saved["long_value"] != nil {
		t.Fatal("inactive history or optional null lost", saved)
	}
	fields["text_value"] = "malicious modification"
	patch["expected_version"] = 3
	expect(t, request(h, "PATCH", path, patch, root, "field-inactive-modify-key"), 400)
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM customer_custom_fields WHERE field_key='text_value'"); err != nil {
		t.Fatal(err)
	}
	delete(fields, "text_value")
	patch["profile"].(map[string]any)["custom_fields_version"] = customerFieldVersion(t, db)
	expect(t, request(h, "PATCH", path, patch, root, "field-deleted-preserve-key"), 200)
	if data(t, request(h, "GET", path, nil, root, ""))["custom_fields"].(map[string]any)["text_value"] != "okay" {
		t.Fatal("deleted definition destroyed history")
	}
	// Disabled required definitions are irrelevant for newly created customers.
	fresh := createProfileCustomerBody("field-active-only-customer", fields, customerFieldVersion(t, db))
	expect(t, request(h, "POST", "/api/v1/admin/customers", fresh, root, "field-active-only-key"), 201)
}

func TestCustomerProfileMutationIdempotencyConcurrencyAndAuditRollback(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "profile-race-root", "super_admin")
	body := createProfileCustomerBody("profile-race-customer", map[string]any{}, customerFieldVersion(t, db))
	start := make(chan struct{})
	created := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			created <- request(h, "POST", "/api/v1/admin/customers", body, root, "profile-race-create-key")
		}()
	}
	close(start)
	id := int64(0)
	codes := map[int]int{}
	for i := 0; i < 2; i++ {
		w := <-created
		codes[w.Code]++
		if w.Code != 200 && w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
		got := int64(data(t, w)["id"].(float64))
		if id != 0 && got != id {
			t.Fatal("duplicate customer created")
		}
		id = got
	}
	if codes[201] != 1 || codes[200] != 1 {
		t.Fatal("concurrent create replay mismatch", codes)
	}
	path := fmt.Sprintf("/api/v1/admin/customers/%d", id)
	body["display_name"] = "different"
	expect(t, request(h, "POST", "/api/v1/admin/customers", body, root, "profile-race-create-key"), 409)
	expect(t, request(h, "PATCH", path, map[string]any{"display_name": "missing version"}, root, "profile-missing-version-key"), 409)
	expect(t, request(h, "PATCH", path, map[string]any{"display_name": "missing key", "expected_version": 1}, root, ""), 400)
	start = make(chan struct{})
	updated := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			updated <- request(h, "PATCH", path, map[string]any{"display_name": fmt.Sprintf("winner-%d", i), "expected_version": 1}, root, fmt.Sprintf("profile-race-update-%d", i))
		}(i)
	}
	close(start)
	codes = map[int]int{}
	for i := 0; i < 2; i++ {
		w := <-updated
		codes[w.Code]++
		if w.Code != 200 && w.Code != 409 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if codes[200] != 1 || codes[409] != 1 || customerProfileVersion(t, db, id) != 2 {
		t.Fatal("same version mutations both committed", codes)
	}
	patch := map[string]any{"expected_version": 2, "profile": map[string]any{"admin_note": "事务备注"}}
	expect(t, request(h, "PATCH", path, patch, root, "profile-idempotent-update-key"), 200)
	replay := request(h, "PATCH", path, patch, root, "profile-idempotent-update-key")
	expect(t, replay, 200)
	if data(t, replay)["replayed"] != true || customerProfileVersion(t, db, id) != 3 {
		t.Fatal("replay changed version")
	}
	patch["profile"].(map[string]any)["admin_note"] = "different"
	expect(t, request(h, "PATCH", path, patch, root, "profile-idempotent-update-key"), 409)
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_profile_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('customer.updated','customer.created') THEN RAISE EXCEPTION 'injected customer profile audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_profile_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_profile_audit()`); err != nil {
		t.Fatal(err)
	}
	rollback := map[string]any{"expected_version": 3, "display_name": "回滚姓名", "status": "disabled", "profile": map[string]any{"admin_note": "回滚备注"}}
	expect(t, request(h, "PATCH", path, rollback, root, "profile-audit-rollback-key"), 500)
	detail := data(t, request(h, "GET", path, nil, root, ""))
	if detail["profile_version"] != float64(3) || detail["admin_note"] != "事务备注" || detail["display_name"] == "回滚姓名" || detail["status"] != "active" {
		t.Fatal("audit failure left partial profile", detail)
	}
	newBody := createProfileCustomerBody("profile-rollback-customer", nil, 0)
	expect(t, request(h, "POST", "/api/v1/admin/customers", newBody, root, "profile-create-rollback-key"), 400) // explicit object null is invalid
	delete(newBody, "custom_fields")
	expect(t, request(h, "POST", "/api/v1/admin/customers", newBody, root, "profile-create-rollback-key"), 500)
	var users, receipts, audits int
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM users WHERE username='profile-rollback-customer'),(SELECT count(*) FROM mutation_operations WHERE scope LIKE 'admin:%' AND idempotency_key IN ('profile-audit-rollback-key','profile-create-rollback-key')),(SELECT count(*) FROM audit_events WHERE entity_type='user' AND entity_id=$1 AND action IN ('customer.created','customer.updated'))`, fmt.Sprint(id)).Scan(&users, &receipts, &audits); err != nil {
		t.Fatal(err)
	}
	if users != 0 || receipts != 0 || audits != 3 {
		t.Fatalf("rollback users=%d receipts=%d audits=%d", users, receipts, audits)
	}
	if _, err := db.Pool.Exec(t.Context(), "DROP TRIGGER reject_profile_audit ON audit_events; DROP FUNCTION reject_profile_audit()"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PATCH", path, rollback, root, "profile-audit-rollback-key"), 200)
	expect(t, request(h, "POST", "/api/v1/admin/customers", newBody, root, "profile-create-rollback-key"), 201)
}

func TestCustomerProfileReplayRechecksPermissionAndSalesScope(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "profile-scope-root", "super_admin")
	staffID, staff := user(t, db, h, "profile-scope-staff", "admin")
	ownerID, _ := user(t, db, h, "profile-scope-owner", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true,customer_scope='owned',customer_ownerless=false WHERE role='admin'"); err != nil {
		t.Fatal(err)
	}
	body := createProfileCustomerBody("profile-scope-customer", nil, 0)
	delete(body, "custom_fields")
	created := request(h, "POST", "/api/v1/admin/customers", body, staff, "profile-scope-create-key")
	expect(t, created, 201)
	id := int64(data(t, created)["id"].(float64))
	path := fmt.Sprintf("/api/v1/admin/customers/%d", id)
	patch := map[string]any{"expected_version": 1, "profile": map[string]any{"admin_note": "范围保护"}}
	expect(t, request(h, "PATCH", path, patch, staff, "profile-scope-update-key"), 200)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['customers.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PATCH", path, patch, staff, "profile-scope-update-key"), 403)
	expect(t, request(h, "POST", "/api/v1/admin/customers", body, staff, "profile-scope-create-key"), 403)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['customers.view','customers.manage'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PUT", path+"/sales-owner", map[string]any{"sales_owner_id": ownerID, "expected_version": 1}, root, ""), 200)
	expect(t, request(h, "PATCH", path, patch, staff, "profile-scope-update-key"), 404)
	expect(t, request(h, "POST", "/api/v1/admin/customers", body, staff, "profile-scope-create-key"), 404)
	if customerProfileVersion(t, db, id) != 2 {
		t.Fatal("scope replay modified profile")
	}
}

func TestCustomerProfileDefinitionConflictWhileWaiting(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "profile-catalogue-root", "super_admin")
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO customer_custom_fields(field_key,label,field_type) VALUES('date_value','日期','date')"); err != nil {
		t.Fatal(err)
	}
	oldVersion := customerFieldVersion(t, db)
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), "UPDATE customer_custom_fields SET required=true WHERE field_key='date_value'"); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	body := createProfileCustomerBody("profile-catalogue-customer", map[string]any{"date_value": "2024-02-29"}, oldVersion)
	go func() {
		done <- request(h, "POST", "/api/v1/admin/customers", body, root, "profile-catalogue-wait-key")
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err = db.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT version FROM customer_field_catalogue%')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("profile request did not wait for definition transaction")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		expect(t, w, 409)
	case <-time.After(8 * time.Second):
		t.Fatal("profile request stuck")
	}
	var count int
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM users WHERE username='profile-catalogue-customer'").Scan(&count); err != nil || count != 0 {
		t.Fatal("stale catalogue created user", count, err)
	}
	body["custom_fields_version"] = customerFieldVersion(t, db)
	expect(t, request(h, "POST", "/api/v1/admin/customers", body, root, "profile-catalogue-wait-key"), 201)
}
