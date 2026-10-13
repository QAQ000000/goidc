package httpapi

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestAdminEntryCreationIdempotencyAndPermissions(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "entry-create-root", "super_admin")
	staffID, staff := user(t, db, h, "entry-create-staff", "admin")
	contentRole(t, db, staffID, "products.view", "products.manage", "notifications.view", "notifications.manage")
	sid := catalogGroupID(t, h, root, "section", catalogGroupInput("entry-parent", nil))
	cases := []struct {
		name, path, action, table string
		body                      map[string]any
	}{
		{"section", "/api/v1/admin/catalog-groups/section", "catalog_group.created", "product_sections", map[string]any{"name": "独立一级分组", "slug": "entry-section", "active": true}},
		{"category", "/api/v1/admin/catalog-groups/category", "catalog_group.created", "product_categories", map[string]any{"name": "独立二级分组", "slug": "entry-category", "section_id": sid, "headline": "标题", "tagline": "标语", "active": false}},
		{"sms", "/api/v1/admin/notification-templates", "notification_template.created", "notification_templates", map[string]any{"channel": "sms", "kind": "entry_sms", "name": "独立短信模板", "body": "您好 {{customer_name}}", "provider_template_id": "provider-1", "active": false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := "entry-concurrent-" + tc.name
			start := make(chan struct{})
			responses := make(chan *httptest.ResponseRecorder, 2)
			var workers sync.WaitGroup
			for range 2 {
				workers.Add(1)
				go func() { defer workers.Done(); <-start; responses <- request(h, "POST", tc.path, tc.body, staff, key) }()
			}
			close(start)
			workers.Wait()
			close(responses)
			var id float64
			for response := range responses {
				expect(t, response, 201)
				got := data(t, response)["id"].(float64)
				if id != 0 && got != id {
					t.Fatalf("concurrent requests created different rows: %v / %v", id, got)
				}
				id = got
			}
			changed := make(map[string]any)
			for k, v := range tc.body {
				changed[k] = v
			}
			changed["name"] = "请求内容已变化"
			conflicted := request(h, "POST", tc.path, changed, staff, key)
			expect(t, conflicted, 409)
			if !strings.Contains(conflicted.Body.String(), `"code":"idempotency_conflict"`) {
				t.Fatalf("unexpected conflict: %s", conflicted.Body.String())
			}
			var count int
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action=$2 AND entity_id=$3", staffID, tc.action, fmt.Sprint(int64(id))).Scan(&count); err != nil || count != 1 {
				t.Fatalf("expected one audit, got %d: %v", count, err)
			}
			var version int64
			if err := db.Pool.QueryRow(t.Context(), "SELECT version FROM "+tc.table+" WHERE id=$1", int64(id)).Scan(&version); err != nil || version != 1 {
				t.Fatalf("creation version %d: %v", version, err)
			}
			contentRole(t, db, staffID, "products.view", "notifications.view")
			expect(t, request(h, "POST", tc.path, tc.body, staff, key), 403)
			contentRole(t, db, staffID, "products.view", "products.manage", "notifications.view", "notifications.manage")
			expect(t, request(h, "POST", tc.path, tc.body, staff, key), 201)
		})
	}
}

func TestAdminEntryCreationCredentialRecheckAndAuditRollback(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "entry-create-rollback-root", "super_admin")
	in := catalogGroupInput("entry-session", nil)
	created := request(h, "POST", "/api/v1/admin/catalog-groups/section", in, root, "entry-session-key")
	expect(t, created, 201)
	credential := tokenHash(strings.SplitN(root, "=", 2)[1])
	ctx := store.WithStorageCredential(t.Context(), credential, false)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE sessions SET expires_at=now()-interval '1 minute' WHERE user_id=$1", rootID); err != nil {
		t.Fatal(err)
	}
	_, err := db.SaveCatalogGroup(ctx, rootID, 0, "section", in, "entry-session-key")
	var denied *store.Conflict
	if !errors.As(err, &denied) || denied.Code != "unauthorized" {
		t.Fatalf("replay accepted expired credential: %v", err)
	}
	_, root = user(t, db, h, "entry-create-audit-root", "super_admin")
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_entry_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('catalog_group.created','notification_template.created') THEN RAISE EXCEPTION 'injected entry audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_entry_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_entry_audit();`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path, table, where string
		body               any
	}{
		{"/api/v1/admin/catalog-groups/section", "product_sections", "slug='entry-audit'", catalogGroupInput("entry-audit", nil)},
		{"/api/v1/admin/notification-templates", "notification_templates", "kind='entry_audit'", map[string]any{"channel": "sms", "kind": "entry_audit", "name": "失败回滚", "body": "测试", "active": false}},
	}
	for i, tc := range cases {
		key := fmt.Sprintf("entry-audit-key-%d", i)
		expect(t, request(h, "POST", tc.path, tc.body, root, key), 500)
		var count int
		if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM "+tc.table+" WHERE "+tc.where).Scan(&count); err != nil || count != 0 {
			t.Fatalf("audit failure left row: %d %v", count, err)
		}
		if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM admin_setting_operations WHERE idempotency_key=$1", key).Scan(&count); err != nil || count != 0 {
			t.Fatalf("audit failure left receipt: %d %v", count, err)
		}
	}
}
