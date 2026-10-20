package httpapi

import (
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestAdminCustomerCustomFieldConcurrentIdempotency(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "custom-fields-idempotency-root", "super_admin")
	body := map[string]any{"field_key": "concurrent_field", "label": "并发字段", "field_type": "text", "active": true}
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	for range 2 {
		go func() {
			defer workers.Done()
			<-start
			responses <- request(h, "POST", "/api/v1/admin/customer-fields", body, root, "custom-field-concurrent-key")
		}()
	}
	close(start)
	workers.Wait()
	close(responses)
	ids := map[float64]bool{}
	codes := map[int]int{}
	for response := range responses {
		codes[response.Code]++
		if response.Code != 200 && response.Code != 201 {
			t.Fatalf("concurrent idempotent create status=%d body=%s", response.Code, response.Body.String())
		}
		ids[data(t, response)["id"].(float64)] = true
	}
	if len(ids) != 1 || codes[200] != 1 || codes[201] != 1 {
		t.Fatalf("concurrent idempotent replay mismatch: ids=%v codes=%v", ids, codes)
	}
}

func TestAdminCustomerCustomFieldsVersionedCRUDAndPermission(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "custom-fields-root", "super_admin")
	staffID, staff := user(t, db, h, "custom-fields-staff", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	if response := request(h, "GET", "/api/v1/admin/customer-fields", nil, staff, ""); response.Code != 403 {
		t.Fatalf("staff without permission status=%d body=%s", response.Code, response.Body.String())
	}
	body := map[string]any{"field_key": "company_code", "label": "公司代码", "field_type": "select", "description": "客户内部编号", "choices": []string{"A", "B"}, "sort_order": 2, "required": true, "show_on_register": true, "active": true}
	created := request(h, "POST", "/api/v1/admin/customer-fields", body, root, "custom-field-create")
	expect(t, created, 201)
	replayedCreate := request(h, "POST", "/api/v1/admin/customer-fields", body, root, "custom-field-create")
	expect(t, replayedCreate, 200)
	if data(t, replayedCreate)["id"] != data(t, created)["id"] {
		t.Fatalf("idempotent create returned a different id: %s", replayedCreate.Body.String())
	}
	duplicate := request(h, "POST", "/api/v1/admin/customer-fields", body, root, "custom-field-duplicate")
	expect(t, duplicate, 409)
	id := int64(data(t, created)["id"].(float64))
	page := request(h, "GET", "/api/v1/admin/customer-fields?q=company_code&field_type=select&active=true&limit=1", nil, root, "")
	expect(t, page, 200)
	result := data(t, page)
	if result["total"] != float64(1) || len(result["items"].([]any)) != 1 {
		t.Fatalf("unexpected custom field page: %s", page.Body.String())
	}
	path := fmt.Sprintf("/api/v1/admin/customer-fields/%d", id)
	updated := map[string]any{"field_key": "company_code", "label": "公司代码（更新）", "field_type": "select", "description": "更新描述", "choices": []string{"A", "B", "C"}, "sort_order": 1, "required": false, "show_on_register": true, "active": true, "expected_version": 1}
	expect(t, request(h, "PATCH", path, updated, root, "custom-field-update"), 200)
	expect(t, request(h, "PATCH", path, updated, root, "custom-field-update"), 200)
	expect(t, request(h, "PATCH", path, updated, root, "custom-field-stale"), 409)
	expect(t, request(h, "DELETE", path, map[string]any{"expected_version": 1}, root, "custom-field-delete-stale"), 409)
	expect(t, request(h, "DELETE", path, map[string]any{"expected_version": 2}, root, "custom-field-delete"), 200)
	bad := map[string]any{"field_key": "bad_regex", "label": "坏字段", "field_type": "text", "validation_pattern": "[", "active": true}
	expect(t, request(h, "POST", "/api/v1/admin/customer-fields", bad, root, "custom-field-invalid"), 400)
}
