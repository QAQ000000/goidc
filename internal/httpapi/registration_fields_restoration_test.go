package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func registrationFieldsSeed(t *testing.T, db *store.Store, h http.Handler, root string) map[string]int64 {
	t.Helper()
	ids := map[string]int64{}
	for i, typ := range []string{"text", "textarea", "number", "date", "select", "boolean"} {
		body := map[string]any{"field_key": "register_" + typ, "label": "注册" + typ, "field_type": typ, "description": "公开测试字段", "choices": []string{}, "sort_order": i, "required": true, "show_on_register": true, "active": true}
		if typ == "select" {
			body["choices"] = []string{"A", "B"}
		}
		if typ == "text" {
			body["validation_pattern"] = "^[A-Z]+$"
		}
		w := request(h, "POST", "/api/v1/admin/customer-fields", body, root, "registration-seed-"+typ)
		expect(t, w, 201)
		ids[typ] = int64(data(t, w)["id"].(float64))
	}
	for _, name := range []string{"internal", "hidden", "disabled"} {
		body := map[string]any{"field_key": "register_" + name, "label": "private-" + name, "field_type": "text", "choices": []string{}, "required": true, "show_on_register": name != "hidden", "admin_only": name == "internal", "active": name != "disabled"}
		w := request(h, "POST", "/api/v1/admin/customer-fields", body, root, "registration-seed-"+name)
		expect(t, w, 201)
		ids[name] = int64(data(t, w)["id"].(float64))
	}
	return ids
}
func registrationFieldsValues() map[string]any {
	return map[string]any{"register_text": "ABC", "register_textarea": "多行\n内容", "register_number": 0, "register_date": "2024-02-29", "register_select": "A", "register_boolean": false}
}
func registrationFieldsBody(name, version string, values map[string]any) map[string]any {
	return map[string]any{"username": name, "email": name + "@example.test", "password": "integration-password", "display_name": "字段测试", "custom_fields_version": version, "custom_fields": values}
}
func registrationFieldsError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	expect(t, w, status)
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || payload.Error.Code != code {
		t.Fatal("wrong error", w.Body.String(), err)
	}
}

func TestRegistrationFieldsSchemaValidationAndPersistence(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "registration-fields-root", "super_admin")
	registrationFieldsSeed(t, db, h, root)
	w := request(h, "GET", "/api/v1/auth/registration-fields", nil, "", "")
	expect(t, w, 200)
	d := data(t, w)
	v, ok := d["definitions_version"].(string)
	if !ok || v == "" {
		t.Fatal("missing opaque version")
	}
	rows := d["items"].([]any)
	if len(rows) != 6 {
		t.Fatal("public scope leaked", w.Body.String())
	}
	allowed := map[string]bool{"field_key": true, "label": true, "field_type": true, "description": true, "required": true, "choices": true}
	for _, raw := range rows {
		for key := range raw.(map[string]any) {
			if !allowed[key] {
				t.Fatal("public metadata leaked", key)
			}
		}
	}
	for name, change := range map[string]func(map[string]any){"missing": func(m map[string]any) { delete(m, "register_text") }, "date": func(m map[string]any) { m["register_date"] = "2025-02-29" }, "regex": func(m map[string]any) { m["register_text"] = "abc" }, "select": func(m map[string]any) { m["register_select"] = "C" }, "internal": func(m map[string]any) { m["register_internal"] = "inject" }, "hidden": func(m map[string]any) { m["register_hidden"] = "inject" }, "disabled": func(m map[string]any) { m["register_disabled"] = "inject" }, "unknown": func(m map[string]any) { m["missing_key"] = "inject" }} {
		values := registrationFieldsValues()
		change(values)
		registrationFieldsError(t, requestFrom(h, "POST", "/api/v1/auth/register", registrationFieldsBody("bad-"+name, v, values), "192.0.2."+fmt.Sprint(len(name))+":1234", "", nil), 400, "invalid_custom_fields")
	}
	good := request(h, "POST", "/api/v1/auth/register", registrationFieldsBody("valid-fields", v, registrationFieldsValues()), "", "")
	expect(t, good, 201)
	id := int64(data(t, good)["id"].(float64))
	var saved []byte
	if err := db.Pool.QueryRow(t.Context(), "SELECT custom_fields FROM customer_profiles WHERE customer_id=$1", id).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal(saved, &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 6 || values["register_boolean"] != false || values["register_number"] != float64(0) {
		t.Fatal("typed false/zero lost", string(saved))
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE customer_custom_fields SET label=label||'新版' WHERE field_key='register_text'; UPDATE customer_field_catalogue SET version=version+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	registrationFieldsError(t, request(h, "POST", "/api/v1/auth/register", registrationFieldsBody("stale-fields", v, registrationFieldsValues()), "", ""), 409, "registration_fields_changed")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET registration_enabled=false"); err != nil {
		t.Fatal(err)
	}
	registrationFieldsError(t, request(h, "GET", "/api/v1/auth/registration-fields", nil, "", ""), 403, "registration_disabled")
	registrationFieldsError(t, request(h, "POST", "/api/v1/auth/register", registrationFieldsBody("closed-fields", v, registrationFieldsValues()), "", ""), 403, "registration_disabled")
}

func TestRegistrationFieldsRollbackConcurrencyAndLegacy(t *testing.T) {
	db, h := testServer(t)
	// No public fields: old payload remains valid.
	old := map[string]any{"username": "legacy-register", "email": "legacy@example.test", "password": "integration-password", "display_name": "旧请求"}
	expect(t, request(h, "POST", "/api/v1/auth/register", old, "", ""), 201)
	_, root := user(t, db, h, "registration-atomic-root", "super_admin")
	registrationFieldsSeed(t, db, h, root)
	schema := data(t, request(h, "GET", "/api/v1/auth/registration-fields", nil, "", ""))
	v := schema["definitions_version"].(string)
	for _, target := range []string{"audit", "profile"} {
		t.Run(target, func(t *testing.T) {
			table, condition := "audit_events", "NEW.action='customer.registered'"
			if target == "profile" {
				table = "customer_profiles"
				condition = "true"
			}
			sql := fmt.Sprintf(`CREATE OR REPLACE FUNCTION reject_registration_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF %s THEN RAISE EXCEPTION 'fixture registration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_registration BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION reject_registration_write()`, condition, table)
			if _, err := db.Pool.Exec(t.Context(), sql); err != nil {
				t.Fatal(err)
			}
			body := registrationFieldsBody("rollback-"+target, v, registrationFieldsValues())
			expect(t, request(h, "POST", "/api/v1/auth/register", body, "", ""), 500)
			var n int
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM users WHERE username=$1", body["username"]).Scan(&n); err != nil || n != 0 {
				t.Fatal("partial registered user", n, err)
			}
			if _, err := db.Pool.Exec(t.Context(), "DROP TRIGGER reject_registration ON "+table); err != nil {
				t.Fatal(err)
			}
			expect(t, request(h, "POST", "/api/v1/auth/register", body, "", ""), 201)
		})
	}
	body := registrationFieldsBody("concurrent-fields", v, registrationFieldsValues())
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- request(h, "POST", "/api/v1/auth/register", body, "", "").Code }()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for c := range codes {
		counts[c]++
	}
	if counts[201] != 1 || counts[409] != 1 {
		t.Fatal("duplicate email race", counts)
	}
}

func TestRegistrationFieldsBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "registration-fields-super", func(db *store.Store, h http.Handler) {
		manager, root := user(t, db, h, "registration-fields-manager", "admin")
		contentRole(t, db, manager, "customers.view", "customers.manage", "settings.view", "settings.manage")
		reader, _ := user(t, db, h, "registration-fields-reader", "admin")
		contentRole(t, db, reader, "customers.view", "settings.view")
		ids := registrationFieldsSeed(t, db, h, root)
		if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET registration_enabled=true,email_verification_required=false,auth_captcha_enabled=false; UPDATE auth_settings SET registration_email_enabled=true,captcha_auto_enabled=false"); err != nil {
			t.Fatal(err)
		}
		manifest, err := json.Marshal(map[string]any{"manager_id": manager, "reader_id": reader, "field_ids": ids})
		if err != nil {
			t.Fatal(err)
		}
		path := os.Getenv("ZJMFGO_BROWSER_READY_FILE") + ".fixture.json"
		if err = os.WriteFile(path, manifest, 0600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(path) })
	})
}

func TestRegistrationFieldsDefinitionEditSerializesRegistration(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "registration-lock-root", "super_admin")
	ids := registrationFieldsSeed(t, db, h, root)
	v := data(t, request(h, "GET", "/api/v1/auth/registration-fields", nil, "", ""))["definitions_version"].(string)
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), "SELECT version FROM customer_field_catalogue WHERE id=1 FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- request(h, "POST", "/api/v1/auth/register", registrationFieldsBody("definition-race", v, registrationFieldsValues()), "", "")
	}()
	// Change a definition under the same catalogue guard used by field writes;
	// the old public form must not commit against the newer definition.
	if _, err = tx.Exec(t.Context(), "UPDATE customer_custom_fields SET choices=ARRAY['B'] WHERE id=$1", ids["select"]); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), "UPDATE customer_field_catalogue SET version=version+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	registrationFieldsError(t, <-done, 409, "registration_fields_changed")
	var n int
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM users WHERE username='definition-race'").Scan(&n); err != nil || n != 0 {
		t.Fatal("stale registration committed", n, err)
	}
}

func TestRegistrationFieldsCatalogueFailsClosed(t *testing.T) {
	for _, limit := range []string{"count", "bytes"} {
		t.Run(limit, func(t *testing.T) {
			db, h := testServer(t)
			if limit == "count" {
				// A supported 100-field catalogue is complete, including its last field.
				if _, err := db.Pool.Exec(t.Context(), `INSERT INTO customer_custom_fields(field_key,label,field_type,show_on_register,active) SELECT 'bounded_'||n,'公开字段'||n,'text',true,true FROM generate_series(1,100) n`); err != nil {
					t.Fatal(err)
				}
				w := request(h, "GET", "/api/v1/auth/registration-fields", nil, "", "")
				expect(t, w, 200)
				if len(data(t, w)["items"].([]any)) != 100 {
					t.Fatal("100 field catalogue truncated")
				}
				if _, err := db.Pool.Exec(t.Context(), `INSERT INTO customer_custom_fields(field_key,label,field_type,show_on_register,active) VALUES('bounded_overflow','超限字段','text',true,true)`); err != nil {
					t.Fatal(err)
				}
			} else {
				// Legal field definitions with many individually bounded choices exceed
				// the aggregate limit while staying well below the field-count ceiling.
				if _, err := db.Pool.Exec(t.Context(), `INSERT INTO customer_custom_fields(field_key,label,field_type,choices,show_on_register,active) SELECT 'large_'||n,'大选项字段'||n,'select',ARRAY(SELECT lpad(i::text,100,'x') FROM generate_series(1,1000) i),true,true FROM generate_series(1,3) n`); err != nil {
					t.Fatal(err)
				}
			}
			registrationFieldsError(t, request(h, "GET", "/api/v1/auth/registration-fields", nil, "", ""), 503, "registration_fields_unavailable")
			body := registrationFieldsBody("bounded-register", "stale", map[string]any{})
			registrationFieldsError(t, request(h, "POST", "/api/v1/auth/register", body, "", ""), 503, "registration_fields_unavailable")
			var count int
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM users WHERE username='bounded-register'").Scan(&count); err != nil || count != 0 {
				t.Fatal("overflow accepted partial registration", count, err)
			}
			if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET registration_enabled=false"); err != nil {
				t.Fatal(err)
			}
			registrationFieldsError(t, request(h, "GET", "/api/v1/auth/registration-fields", nil, "", ""), 403, "registration_disabled")
		})
	}
}
