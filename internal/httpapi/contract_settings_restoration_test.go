package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestContractSettingsRestorationTemplateSearchAndPermissions(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "contract-rest-root", "super_admin")
	uid, reader := user(t, db, h, "contract-rest-reader", "admin")
	var id int64
	for i := 0; i < 35; i++ {
		w := request(h, "POST", "/api/v1/admin/contract-templates", map[string]any{"name": fmt.Sprintf("恢复模板-%02d", i), "content": "合同正文 searchbody {{customer_name}}", "enabled": i%2 == 0}, root, fmt.Sprintf("rest-template-%d", i))
		expect(t, w, 200)
		id = int64(data(t, w)["id"].(float64))
	}
	base := "/api/v1/admin/contract-templates?q=searchbody"
	for status, total := range map[string]int{"all": 35, "enabled": 18, "disabled": 17} {
		w := request(h, "GET", base+"&status="+status+"&limit=10", nil, root, "")
		expect(t, w, 200)
		d := data(t, w)
		if d["total"] != float64(total) || len(d["items"].([]any)) != 10 {
			t.Fatal("filter/count mismatch", w.Body.String())
		}
	}
	seen := map[float64]bool{}
	for offset := 0; offset < 40; offset += 10 {
		w := request(h, "GET", fmt.Sprintf("%s&limit=10&offset=%d", base, offset), nil, root, "")
		expect(t, w, 200)
		d := data(t, w)
		if d["total"] != float64(35) {
			t.Fatal("page total changed")
		}
		for _, raw := range d["items"].([]any) {
			id := raw.(map[string]any)["id"].(float64)
			if seen[id] {
				t.Fatal("repeated page row")
			}
			seen[id] = true
		}
	}
	if len(seen) != 35 {
		t.Fatal("rows lost across pages")
	}
	for _, suffix := range []string{"?q=absent-search", "?q=searchbody&offset=1000"} {
		w := request(h, "GET", "/api/v1/admin/contract-templates"+suffix, nil, root, "")
		expect(t, w, 200)
		if len(data(t, w)["items"].([]any)) != 0 {
			t.Fatal("empty/overflow rows")
		}
	}
	w := request(h, "GET", "/api/v1/admin/contract-templates?q=恢复模板-34", nil, root, "")
	expect(t, w, 200)
	if data(t, w)["total"] != float64(1) {
		t.Fatal("name search failed")
	}
	detail := fmt.Sprintf("/api/v1/admin/contract-templates/%d", id)
	contentRole(t, db, uid, "contracts.view")
	expect(t, request(h, "GET", detail, nil, reader, ""), 403)
	contentRole(t, db, uid, "settings.view")
	expect(t, request(h, "GET", detail, nil, reader, ""), 200)
	expect(t, request(h, "PUT", detail, map[string]any{"name": "denied", "content": "正文", "enabled": true, "expected_version": 1}, reader, "reader-write"), 403)
	expect(t, request(h, "GET", "/api/v1/admin/contract-templates?status=bad", nil, root, ""), 400)
}

func TestContractSettingsRestorationConcurrencyReplayRollback(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "contract-rest-manager", "admin")
	contentRole(t, db, staffID, "settings.view", "settings.manage")
	w := request(h, "POST", "/api/v1/admin/contract-templates", map[string]any{"name": "并发模板", "content": "旧正文", "enabled": true}, staff, "concurrent-seed")
	expect(t, w, 200)
	id := int64(data(t, w)["id"].(float64))
	for _, kind := range []string{"settings", "template"} {
		t.Run(kind, func(t *testing.T) {
			path, method, action, table := "/api/v1/admin/contract-settings", "PUT", "contract.settings_updated", "contract_settings"
			body := func(v int64, label string) map[string]any {
				return map[string]any{"enabled": label != "different", "expected_version": v}
			}
			if kind == "template" {
				path = fmt.Sprintf("/api/v1/admin/contract-templates/%d", id)
				method = "PUT"
				action = "contract.template_saved"
				table = "contract_templates"
				body = func(v int64, label string) map[string]any {
					return map[string]any{"name": label, "content": "新正文", "enabled": true, "expected_version": v}
				}
			}
			rowID := int64(1)
			if kind == "template" {
				rowID = id
			}
			version := func() int64 {
				var v int64
				if err := db.Pool.QueryRow(t.Context(), "SELECT version FROM "+table+" WHERE id=$1", rowID).Scan(&v); err != nil {
					t.Fatal(err)
				}
				return v
			}
			auditCount := func() int {
				var n int
				if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action=$1", action).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			concurrent := func(same bool) {
				v := version()
				before := auditCount()
				var wg sync.WaitGroup
				codes := make(chan int, 2)
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						key := fmt.Sprintf("%s-edit-%d", kind, i)
						label := fmt.Sprint(i)
						if same {
							key = kind + "-replay"
							label = "same"
						}
						codes <- request(h, method, path, body(v, label), staff, key).Code
					}(i)
				}
				wg.Wait()
				close(codes)
				counts := map[int]int{}
				for c := range codes {
					counts[c]++
				}
				if (same && counts[200] != 2) || (!same && (counts[200] != 1 || counts[409] != 1)) || version() != v+1 || auditCount() != before+1 {
					t.Fatal("concurrency duplicated or overwrote", counts)
				}
			}
			concurrent(false)
			concurrent(true)
			expect(t, request(h, method, path, body(version(), "different"), staff, kind+"-replay"), 409)
			contentRole(t, db, staffID, "settings.view")
			expect(t, request(h, method, path, body(version()-1, "same"), staff, kind+"-replay"), 403)
			contentRole(t, db, staffID, "settings.view", "settings.manage")
			v := version()
			before := auditCount()
			if _, err := db.Pool.Exec(t.Context(), `CREATE OR REPLACE FUNCTION reject_rest_settings_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('contract.settings_updated','contract.template_saved') THEN RAISE EXCEPTION 'fixture audit rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_rest_settings BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_rest_settings_audit()`); err != nil {
				t.Fatal(err)
			}
			expect(t, request(h, method, path, body(v, "rollback"), staff, kind+"-audit-fail"), 500)
			if version() != v || auditCount() != before {
				t.Fatal("audit failure left mutation")
			}
			if _, err := db.Pool.Exec(t.Context(), "DROP TRIGGER reject_rest_settings ON audit_events"); err != nil {
				t.Fatal(err)
			}
			expect(t, request(h, method, path, body(v, "rollback"), staff, kind+"-audit-fail"), 200)
		})
	}
}

func TestContractSettingsRestorationHistoricalBodyAndClosed(t *testing.T) {
	db, h, root, customer, uid, sid, tid := contractTestFixture(t)
	w := request(h, "POST", "/api/v1/contracts", map[string]any{"service_id": sid, "template_id": tid}, customer, "history-create")
	expect(t, w, 200)
	id := int64(data(t, w)["id"].(float64))
	path := fmt.Sprintf("/api/v1/contracts/%d", id)
	before := data(t, request(h, "GET", path, nil, customer, ""))["content"]
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/contract-templates/%d", tid), map[string]any{"name": "已改停用", "content": "替换后的正文", "enabled": false, "expected_version": 1}, root, "history-change"), 200)
	got := request(h, "GET", path, nil, customer, "")
	expect(t, got, 200)
	after := data(t, got)["content"]
	if before == nil || before != after {
		t.Fatal("historical body changed", before, after)
	}
	expect(t, request(h, "PUT", "/api/v1/admin/contract-settings", map[string]any{"enabled": false, "expected_version": 1}, root, "history-close"), 200)
	expect(t, request(h, "POST", path+"/submit", map[string]any{"signer_name": "验收客户", "accepted": true, "expected_version": 1}, customer, "closed-submit"), 409)
	newSID, iid := billingSchedulerFixture(t, db, uid, product(t, db, 5), 779, false)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1", iid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/contracts", map[string]any{"service_id": newSID, "template_id": tid}, customer, "closed-create"), 409)
	expect(t, request(h, "GET", path, nil, customer, ""), 200)
}

func TestContractSettingsRestorationBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "contract-settings-accept-super", func(db *store.Store, h http.Handler) {
		manager, root := user(t, db, h, "contract-settings-manager", "admin")
		contentRole(t, db, manager, "settings.view", "settings.manage")
		reader, _ := user(t, db, h, "contract-settings-reader", "admin")
		contentRole(t, db, reader, "settings.view")
		ids := []int64{}
		for i := 0; i < 35; i++ {
			w := request(h, "POST", "/api/v1/admin/contract-templates", map[string]any{"name": fmt.Sprintf("验收合同模板-%02d", i), "content": fmt.Sprintf("正文检索词-%02d {{customer_name}} {{domain}}", i), "enabled": i%2 == 0}, root, fmt.Sprintf("browser-contract-%d", i))
			expect(t, w, 200)
			ids = append(ids, int64(data(t, w)["id"].(float64)))
		}
		manifest, err := json.Marshal(map[string]any{"manager_id": manager, "reader_id": reader, "template_ids": ids})
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
