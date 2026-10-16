package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

const fieldAdminPath = "/api/v1/admin/customer-fields"

func fieldAdminSchema(t *testing.T, db *store.Store) {
	t.Helper()
	var schema string
	if err := db.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated_schema=%s", schema)
}
func fieldAdminBody(key string) map[string]any {
	return map[string]any{"field_key": key, "label": "字段 " + key, "field_type": "text", "description": "管理恢复测试", "choices": []string{}, "sort_order": 0, "admin_only": false, "required": false, "show_on_register": false, "active": true}
}
func fieldAdminCreate(t *testing.T, h http.Handler, cookie, key string, body map[string]any) int64 {
	t.Helper()
	w := request(h, "POST", fieldAdminPath, body, cookie, key)
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}
func fieldAdminDTO(t *testing.T, m map[string]any) {
	t.Helper()
	allowed := strings.Fields("id field_key label field_type description validation_pattern choices sort_order admin_only required show_on_register active version created_at updated_at")
	if len(m) != len(allowed) {
		t.Fatalf("wrong DTO fields: %v", m)
	}
	for _, k := range allowed {
		if _, ok := m[k]; !ok {
			t.Fatal("missing DTO field", k)
		}
	}
	if m["id"].(float64) < 1 || m["version"].(float64) < 1 || m["choices"] == nil {
		t.Fatal("invalid DTO", m)
	}
}
func TestCustomerFieldAdminReadPermissionDTOAndFilters(t *testing.T) {
	db, h := testServer(t)
	fieldAdminSchema(t, db)
	_, root := user(t, db, h, "field-admin-root", "super_admin")
	staffID, staff := user(t, db, h, "field-admin-manager", "admin")
	contentRole(t, db, staffID, "customers.manage")
	viewID, viewer := user(t, db, h, "field-admin-viewer", "admin")
	contentRole(t, db, viewID, "customers.view")
	fixtures := []string{"中文%百分", "中文_下划线", `中文\反斜杠`, "普通字段", strings.Repeat("界", 100)}
	ids := []int64{}
	for i, label := range fixtures {
		b := fieldAdminBody(fmt.Sprintf("search_%d", i))
		b["label"] = label
		ids = append(ids, fieldAdminCreate(t, h, root, fmt.Sprintf("search-create-%d", i), b))
	}
	path := fmt.Sprintf("%s/%d", fieldAdminPath, ids[0])
	for _, p := range []string{fieldAdminPath, path} {
		expect(t, request(h, "GET", p, nil, "", ""), 401)
		expect(t, request(h, "GET", p, nil, viewer, ""), 403)
	}
	expect(t, request(h, "POST", fieldAdminPath, fieldAdminBody("denied_field"), viewer, "denied-field-create"), 403)
	expect(t, request(h, "GET", "/api/v1/admin/customer-field-definitions", nil, viewer, ""), 200)
	w := request(h, "GET", path, nil, staff, "")
	expect(t, w, 200)
	fieldAdminDTO(t, data(t, w))
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("detail cache policy")
	}
	for _, id := range []string{"0", "-1", "abc"} {
		expect(t, request(h, "GET", fieldAdminPath+"/"+id, nil, staff, ""), 400)
	}
	expect(t, request(h, "GET", fieldAdminPath+"/999999999", nil, staff, ""), 404)
	for _, q := range []string{"中文%", "中文_", `中文\`, "SEARCH_0", strings.Repeat("界", 100)} {
		w = request(h, "GET", fieldAdminPath+"?q="+url.QueryEscape("  "+q+"  "), nil, staff, "")
		expect(t, w, 200)
		d := data(t, w)
		if d["total"] != float64(1) {
			t.Fatal("search is not literal unicode substring", q, w.Body.String())
		}
		for _, r := range d["items"].([]any) {
			fieldAdminDTO(t, r.(map[string]any))
		}
	}
	for _, q := range []string{"q=" + url.QueryEscape(strings.Repeat("界", 101)), "field_type=unknown", "field_type=%20text%20", "active=TRUE", "page=1", "unexpected=x", "q=x&q=y", "q=%FF", "q=%00"} {
		expect(t, request(h, "GET", fieldAdminPath+"?"+q, nil, staff, ""), 400)
	}
	w = request(h, "GET", fieldAdminPath+"?limit=bad&offset=-2", nil, staff, "")
	expect(t, w, 200)
	d := data(t, w)
	if d["limit"] != float64(25) || d["offset"] != float64(0) {
		t.Fatal("pagination fallback", d)
	}
	rows := d["items"].([]any)
	for i, r := range rows {
		if int64(r.(map[string]any)["id"].(float64)) != ids[i] {
			t.Fatal("unstable equal-sort ordering")
		}
	}
	for _, q := range []string{"field_type=text&active=true", "q=not_found", "limit=1&offset=999"} {
		expect(t, request(h, "GET", fieldAdminPath+"?"+q, nil, staff, ""), 200)
	}
	contentRole(t, db, staffID, "customers.view")
	expect(t, request(h, "GET", path, nil, staff, ""), 403)
}

func TestCustomerFieldAdminImmutableReplayHistoryAndRevocation(t *testing.T) {
	db, h := testServer(t)
	fieldAdminSchema(t, db)
	_, root := user(t, db, h, "field-replay-root", "super_admin")
	sid, staff := user(t, db, h, "field-replay-manager", "admin")
	contentRole(t, db, sid, "customers.manage")
	uid, _ := user(t, db, h, "field-history-customer", "customer")
	body := fieldAdminBody("history_field")
	id := fieldAdminCreate(t, h, staff, "history-create-key", body)
	path := fmt.Sprintf("%s/%d", fieldAdminPath, id)
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO customer_profiles(customer_id,custom_fields) VALUES($1,'{"history_field":false,"old_number":0}')`, uid); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		var s string
		if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_array((SELECT to_jsonb(f) FROM customer_custom_fields f WHERE id=$1),(SELECT version FROM customer_field_catalogue WHERE id=1),(SELECT count(*) FROM audit_events WHERE entity_type='customer_custom_field' AND entity_id=$1::bigint::text),(SELECT custom_fields FROM customer_profiles WHERE customer_id=$2))::text`, id, uid).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	update := fieldAdminBody("renamed_field")
	update["expected_version"] = 1
	before := snapshot()
	registrationFieldsError(t, request(h, "PATCH", path, update, staff, "immutable-field-key"), 409, "field_key_immutable")
	if snapshot() != before {
		t.Fatal("rename changed definition/catalogue/audit/profile")
	}
	update = fieldAdminBody("history_field")
	update["expected_version"] = 1
	update["active"] = false
	updated := request(h, "PATCH", path, update, staff, "history-update-key")
	expect(t, updated, 200)
	expect(t, request(h, "PATCH", path, update, root, "history-stale-key"), 409)
	changed := fieldAdminBody("history_field")
	changed["expected_version"] = 1
	changed["label"] = "异参"
	expect(t, request(h, "PATCH", path, changed, staff, "history-update-key"), 409)
	deletion := map[string]any{"expected_version": 2}
	deleted := request(h, "DELETE", path, deletion, staff, "history-delete-key")
	expect(t, deleted, 200)
	expect(t, request(h, "GET", path, nil, staff, ""), 404)
	for _, op := range []struct {
		method, path, key string
		body              any
		want              map[string]any
	}{{"POST", fieldAdminPath, "history-create-key", body, map[string]any{"id": float64(id), "version": float64(1)}}, {"PATCH", path, "history-update-key", update, data(t, updated)}, {"DELETE", path, "history-delete-key", deletion, data(t, deleted)}} {
		before = snapshot()
		w := request(h, op.method, op.path, op.body, staff, op.key)
		expect(t, w, 200)
		actual := data(t, w)
		delete(actual, "replayed")
		a, _ := json.Marshal(actual)
		b, _ := json.Marshal(op.want)
		if string(a) != string(b) || snapshot() != before {
			t.Fatal("deleted-target replay changed receipt or facts", op.method, w.Body.String())
		}
	}
	changedCreate := fieldAdminBody("history_field")
	changedCreate["label"] = "异参新增"
	expect(t, request(h, "POST", fieldAdminPath, changedCreate, staff, "history-create-key"), 409)
	expect(t, request(h, "DELETE", path, map[string]any{"expected_version": 1}, staff, "history-delete-key"), 409)

	var profile string
	var audits int
	if err := db.Pool.QueryRow(t.Context(), `SELECT custom_fields::text,(SELECT count(*) FROM audit_events WHERE entity_type='customer_custom_field' AND entity_id=$2::bigint::text) FROM customer_profiles WHERE customer_id=$1`, uid, id).Scan(&profile, &audits); err != nil {
		t.Fatal(err)
	}
	if profile != `{"old_number": 0, "history_field": false}` || audits != 3 {
		t.Fatal("history or unique audit altered", profile, audits)
	}
	contentRole(t, db, sid, "customers.view")
	expect(t, request(h, "DELETE", path, deletion, staff, "history-delete-key"), 403)
	contentRole(t, db, sid, "customers.manage")
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", sid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", fieldAdminPath, body, staff, "history-create-key"), 401)
}

func TestCustomerFieldAdminConcurrentUpdateAndDeleteReceipts(t *testing.T) {
	db, h := testServer(t)
	fieldAdminSchema(t, db)
	_, root := user(t, db, h, "field-concurrent-root", "super_admin")
	body := fieldAdminBody("concurrent_restoration")
	id := fieldAdminCreate(t, h, root, "concurrent-restoration-create", body)
	path := fmt.Sprintf("%s/%d", fieldAdminPath, id)
	body["expected_version"] = 1
	body["label"] = "并发更新"
	for _, op := range []struct {
		method, key string
		body        any
	}{{"PATCH", "concurrent-restoration-update", body}, {"DELETE", "concurrent-restoration-delete", map[string]any{"expected_version": 2}}} {
		start := make(chan struct{})
		results := make(chan *httptest.ResponseRecorder, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; results <- request(h, op.method, path, op.body, root, op.key) }()
		}
		close(start)
		wg.Wait()
		close(results)
		var receipt string
		for w := range results {
			expect(t, w, 200)
			actual := data(t, w)
			delete(actual, "replayed")
			raw, _ := json.Marshal(actual)
			if receipt != "" && receipt != string(raw) {
				t.Fatal("concurrent receipt mismatch")
			}
			receipt = string(raw)
		}
	}
	var audits int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE entity_type='customer_custom_field' AND entity_id=$1::bigint::text", id).Scan(&audits); err != nil || audits != 3 {
		t.Fatal("duplicate concurrent audit", audits, err)
	}
}

func TestCustomerFieldAdminValidationAndCompetingVersions(t *testing.T) {
	db, h := testServer(t)
	fieldAdminSchema(t, db)
	_, root := user(t, db, h, "field-validation-root", "super_admin")
	for name, change := range map[string]func(map[string]any){
		"duplicate-options":    func(b map[string]any) { b["field_type"] = "select"; b["choices"] = []string{" A ", "A"} },
		"empty-option":         func(b map[string]any) { b["field_type"] = "select"; b["choices"] = []string{" "} },
		"missing-options":      func(b map[string]any) { b["field_type"] = "select" },
		"non-select-options":   func(b map[string]any) { b["choices"] = []string{"A"} },
		"go-regexp":            func(b map[string]any) { b["validation_pattern"] = "(?=a)" },
		"label-boundary":       func(b map[string]any) { b["label"] = strings.Repeat("界", 121) },
		"description-boundary": func(b map[string]any) { b["description"] = strings.Repeat("界", 501) },
	} {
		b := fieldAdminBody("validation_field")
		change(b)
		expect(t, request(h, "POST", fieldAdminPath, b, root, "invalid-field-"+name), 400)
	}
	body := fieldAdminBody("competing_field")
	body["label"] = strings.Repeat("界", 120)
	body["description"] = strings.Repeat("界", 500)
	id := fieldAdminCreate(t, h, root, "competing-field-create", body)
	path := fmt.Sprintf("%s/%d", fieldAdminPath, id)
	start := make(chan struct{})
	result := make(chan *httptest.ResponseRecorder, 2)
	for i := range 2 {
		go func(i int) {
			<-start
			b := fieldAdminBody("competing_field")
			b["expected_version"] = 1
			b["label"] = fmt.Sprintf("竞争%d", i)
			result <- request(h, "PATCH", path, b, root, fmt.Sprintf("competing-field-%d", i))
		}(i)
	}
	close(start)
	codes := map[int]int{}
	for range 2 {
		w := <-result
		codes[w.Code]++
	}
	if codes[200] != 1 || codes[409] != 1 {
		t.Fatal("competing version result", codes)
	}
}

func TestCustomerFieldAdminListSnapshotUnderChurn(t *testing.T) {
	db, h := testServer(t)
	fieldAdminSchema(t, db)
	_, root := user(t, db, h, "field-snapshot-root", "super_admin")
	fieldAdminCreate(t, h, root, "snapshot-baseline-create", fieldAdminBody("snapshot_baseline"))
	var commits atomic.Int64
	sampled := map[int]int{}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			if _, err := db.Pool.Exec(t.Context(), `INSERT INTO customer_custom_fields(field_key,label,field_type) SELECT 'snapshot_'||n,'snapshot','text' FROM generate_series(1,20)n`); err != nil {
				done <- err
				return
			}
			commits.Add(1)
			if _, err := db.Pool.Exec(t.Context(), `DELETE FROM customer_custom_fields WHERE field_key<>'snapshot_baseline'`); err != nil {
				done <- err
				return
			}
			commits.Add(1)
		}
	}()
	defer func() {
		close(stop)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	for range 25 {
		w := request(h, "GET", fieldAdminPath+"?limit=100", nil, root, "")
		expect(t, w, 200)
		d := data(t, w)
		sampled[int(d["total"].(float64))]++
		if d["total"] != float64(len(d["items"].([]any))) {
			t.Fatal("count/items snapshot mismatch", w.Body.String())
		}
	}
	if commits.Load() < 2 {
		t.Fatal("churn did not commit an insert/delete cycle")
	}
	t.Logf("snapshot samples=25 observed_totals=%v concurrent_commits=%d", sampled, commits.Load())
}

func TestCustomerFieldAdminBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" {
		t.Skip("opt-in browser fixture")
	}
	adminBrowserFixture(t, "field-admin-super", func(db *store.Store, h http.Handler) {
		mid, manager := user(t, db, h, "field-admin-manager", "admin")
		contentRole(t, db, mid, "customers.manage", "customers.view")
		vid, _ := user(t, db, h, "field-admin-viewer", "admin")
		contentRole(t, db, vid, "customers.view")
		ids := registrationFieldsSeed(t, db, h, manager)
		for i := 0; i < 53; i++ {
			body := fieldAdminBody(fmt.Sprintf("page_field_%02d", i))
			body["label"] = fmt.Sprintf("分页字段 %02d", i)
			body["sort_order"] = 20 + i
			fieldAdminCreate(t, h, manager, fmt.Sprintf("page-field-create-%02d", i), body)
		}
		uid, _ := user(t, db, h, "field-history-customer", "customer")
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO customer_profiles(customer_id,custom_fields) VALUES($1,'{"register_boolean":false,"register_number":0,"register_text":"ABC"}')`, uid); err != nil {
			t.Fatal(err)
		}
		p := os.Getenv("ZJMFGO_BROWSER_READY_FILE") + ".fixture.json"
		manifest, _ := json.Marshal(map[string]any{"manager_id": mid, "viewer_id": vid, "customer_id": uid, "field_ids": ids, "field_count": 62})
		if err := os.WriteFile(p, manifest, 0600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(p) })
	})
}
