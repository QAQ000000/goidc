package httpapi

import (
	"context"
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestContractPlaceholderValidation(t *testing.T) {
	if _, err := store.RenderContract("{{unknown}}", store.ContractValues()); err == nil {
		t.Fatal("unknown placeholder accepted")
	}
	if _, err := store.RenderContract("{{bad", store.ContractValues()); err == nil {
		t.Fatal("malformed accepted")
	}
	v := store.ContractValues()
	v["customer_name"] = "客户<script>"
	out, err := store.RenderContract("Hello {{customer_name}}", v)
	if err != nil || out != "Hello 客户<script>" {
		t.Fatal(out, err)
	}
}
func TestContractLifecycleSecurityAndSnapshots(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "contract-root", "super_admin")
	uid, customer := user(t, db, h, "contract-customer", "customer")
	_, other := user(t, db, h, "contract-other", "customer")
	pid := product(t, db, 10)
	sid, iid := billingSchedulerFixture(t, db, uid, pid, 711, false)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1", iid); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"name": "安全模板", "content": "客户 {{customer_name}} / {{domain}} <script>alert(1)</script>", "enabled": true}
	w := request(h, "POST", "/api/v1/admin/contract-templates", body, root, "contract-template-key")
	expect(t, w, 200)
	tid := int64(data(t, w)["id"].(float64))
	create := map[string]any{"service_id": sid, "template_id": tid}
	expect(t, request(h, "POST", "/api/v1/contracts", create, customer, "contract-create-key"), 409)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE contract_settings SET enabled=true"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/contracts", create, other, "contract-other-key"), 404)
	w = request(h, "POST", "/api/v1/contracts", create, customer, "contract-create-key")
	expect(t, w, 200)
	id := int64(data(t, w)["id"].(float64))
	path := fmt.Sprintf("/api/v1/contracts/%d", id)
	admin := fmt.Sprintf("/api/v1/admin/contracts/%d", id)
	expect(t, request(h, "POST", "/api/v1/contracts", create, customer, "contract-create-key"), 200)
	expect(t, request(h, "GET", path, nil, other, ""), 404)
	submit := map[string]any{"signer_name": "真实客户", "accepted": true, "expected_version": 1}
	expect(t, request(h, "POST", path+"/submit", submit, other, "contract-submit-other"), 404)
	expect(t, request(h, "POST", path+"/submit", submit, customer, "contract-submit-key"), 200)
	expect(t, request(h, "POST", admin+"/review", map[string]any{"decision": "reject", "expected_version": 2}, root, "contract-reject-no-note"), 409)
	expect(t, request(h, "POST", admin+"/review", map[string]any{"decision": "approve", "expected_version": 2}, root, "contract-review-key"), 200)
	shipping := map[string]any{"carrier": "快递", "tracking_number": "TRACK", "recipient": "私人姓名", "address": "私人地址", "phone": "13800138000", "expected_version": 3}
	expect(t, request(h, "PUT", admin+"/shipping", shipping, root, "contract-shipping-key"), 200)
	doc := request(h, "GET", path+"/document", nil, customer, "")
	expect(t, doc, 200)
	if strings.Contains(doc.Body.String(), "<script>") || !strings.Contains(doc.Body.String(), "&lt;script&gt;") || !strings.Contains(doc.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("unsafe document", doc.Body.String())
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE contract_settings SET enabled=false"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", path, nil, customer, ""), 200)
	expect(t, request(h, "POST", admin+"/void", map[string]any{"expected_version": 4, "reason": "终止"}, root, "contract-void-key"), 200)
	var docs int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM contract_documents WHERE contract_id=$1", id).Scan(&docs); err != nil || docs != 5 {
		t.Fatal("history snapshots", docs, err)
	}
	var leaked bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM audit_events WHERE payload::text LIKE '%私人地址%')").Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("private shipping leaked into audit")
	}
}
func TestContractPermissionsBulkRollbackAndRefund(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "contract-security-root", "super_admin")
	staffID, staff := user(t, db, h, "contract-security-staff", "admin")
	uid, customer := user(t, db, h, "contract-security-customer", "customer")
	pid := product(t, db, 10)
	sid, iid := billingSchedulerFixture(t, db, uid, pid, 712, false)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1", iid); err != nil {
		t.Fatal(err)
	}
	db.Pool.Exec(t.Context(), "UPDATE contract_settings SET enabled=true")
	w := request(h, "POST", "/api/v1/admin/contract-templates", map[string]any{"name": "合同", "content": "{{domain}}", "enabled": true}, root, "contract-security-template")
	expect(t, w, 200)
	tid := int64(data(t, w)["id"].(float64))
	w = request(h, "POST", "/api/v1/contracts", map[string]any{"service_id": sid, "template_id": tid}, customer, "contract-security-create")
	expect(t, w, 200)
	id := int64(data(t, w)["id"].(float64))
	path := fmt.Sprintf("/api/v1/admin/contracts/%d", id)
	db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['contracts.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID)
	expect(t, request(h, "GET", "/api/v1/admin/contracts/available-templates", nil, staff, ""), 200)
	expect(t, request(h, "GET", "/api/v1/admin/contract-templates", nil, staff, ""), 403)
	expect(t, request(h, "POST", path+"/void", map[string]any{"reason": "测试", "expected_version": 1}, staff, "contract-security-denied"), 403)
	expect(t, request(h, "POST", path+"/void", map[string]any{"reason": "测试", "expected_version": 1, "items": []any{}}, root, "contract-security-extra"), 400)
	expect(t, request(h, "POST", "/api/v1/admin/contracts/bulk-void", map[string]any{"reason": "测试", "items": []any{map[string]any{"id": id, "expected_version": 1}, map[string]any{"id": id, "expected_version": 1}}}, root, "contract-security-bulk"), 400)
	var status string
	db.Pool.QueryRow(t.Context(), "SELECT status FROM contracts WHERE id=$1", id).Scan(&status)
	if status != "draft" {
		t.Fatal("bulk not atomic")
	}
	db.Pool.Exec(t.Context(), "UPDATE invoices SET status='refunded' WHERE id=$1", iid)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/contracts/%d/submit", id), map[string]any{"signer_name": "客户", "accepted": true, "expected_version": 1}, customer, "contract-refunded-submit"), 404)
	db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['contracts.view','contracts.manage'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID)
	expect(t, request(h, "POST", path+"/void", map[string]any{"reason": "测试", "expected_version": 1}, staff, "contract-security-replay"), 200)
	db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['contracts.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID)
	expect(t, request(h, "POST", path+"/void", map[string]any{"reason": "测试", "expected_version": 1}, staff, "contract-security-replay"), 403)
}

func TestContractConcurrentCreateAndPaidDateBoundary(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "contract-race-root", "super_admin")
	uid, customer := user(t, db, h, "contract-race-user", "customer")
	pid := product(t, db, 10)
	sid, iid := billingSchedulerFixture(t, db, uid, pid, 713, false)
	db.Pool.Exec(t.Context(), "UPDATE invoices SET status='paid',paid_at='2026-10-11 16:30:00+00' WHERE id=$1", iid)
	db.Pool.Exec(t.Context(), "UPDATE contract_settings SET enabled=true")
	w := request(h, "POST", "/api/v1/admin/contract-templates", map[string]any{"name": "多行合同", "content": "第一段\n{{domain}}", "enabled": true}, root, "contract-race-template")
	expect(t, w, 200)
	tid := int64(data(t, w)["id"].(float64))
	create := map[string]any{"service_id": sid, "template_id": tid}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			codes <- request(h, "POST", "/api/v1/contracts", create, customer, fmt.Sprintf("contract-race-create-%d", n)).Code
		}(n)
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("concurrent create", counts)
	}
	for _, day := range []string{"2026-10-11", "2026-10-12"} {
		w = request(h, "GET", "/api/v1/admin/contracts?from="+day+"&to="+day, nil, root, "")
		expect(t, w, 200)
		wanted := float64(0)
		if day == "2026-10-12" {
			wanted = 1
		}
		if data(t, w)["total"] != wanted {
			t.Fatal("paid Shanghai boundary", day, data(t, w))
		}
	}
	expect(t, request(h, "GET", "/api/v1/admin/contracts?from=2026-10-13&to=2026-10-12", nil, root, ""), 400)
}
func contractTestFixture(t *testing.T) (*store.Store, http.Handler, string, string, int64, int64, int64) {
	t.Helper()
	db, h := testServer(t)
	_, root := user(t, db, h, "contract-extra-root", "super_admin")
	uid, customer := user(t, db, h, "contract-extra-customer", "customer")
	sid, iid := billingSchedulerFixture(t, db, uid, product(t, db, 10), 714, false)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1", iid); err != nil {
		t.Fatal(err)
	}
	db.Pool.Exec(t.Context(), "UPDATE contract_settings SET enabled=true")
	w := request(h, "POST", "/api/v1/admin/contract-templates", map[string]any{"name": "专项合同", "content": "{{domain}}", "enabled": true}, root, "contract-extra-template")
	expect(t, w, 200)
	return db, h, root, customer, uid, sid, int64(data(t, w)["id"].(float64))
}
func TestContractAuditRollbackAndBulkStaleRollback(t *testing.T) {
	db, h, root, customer, uid, sid, tid := contractTestFixture(t)
	create := map[string]any{"service_id": sid, "template_id": tid}
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_contract_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.entity_type='contract' THEN RAISE EXCEPTION 'contract audit rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_contract BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_contract_audit();`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/contracts", create, customer, "contract-audit-create"), 500)
	for _, table := range []string{"contracts", "contract_history", "contract_documents", "contract_receipts"} {
		var count int
		db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count)
		wanted := 0
		if table == "contract_receipts" {
			wanted = 1
		}
		if count != wanted {
			t.Fatal("create audit partial write", table, count)
		}
	}
	db.Pool.Exec(t.Context(), "DROP TRIGGER reject_contract ON audit_events")
	w := request(h, "POST", "/api/v1/contracts", create, customer, "contract-audit-create")
	expect(t, w, 200)
	id := int64(data(t, w)["id"].(float64))
	path := fmt.Sprintf("/api/v1/admin/contracts/%d/void", id)
	db.Pool.Exec(t.Context(), "CREATE TRIGGER reject_contract BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_contract_audit()")
	expect(t, request(h, "POST", path, map[string]any{"expected_version": 1, "reason": "审计失败"}, root, "contract-audit-void"), 500)
	var version int
	var status string
	db.Pool.QueryRow(t.Context(), "SELECT version,status FROM contracts WHERE id=$1", id).Scan(&version, &status)
	if version != 1 || status != "draft" {
		t.Fatal("audit mutation survived", version, status)
	}
	for _, table := range []string{"contract_history", "contract_documents", "contract_receipts"} {
		var count int
		db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count)
		wanted := 1
		if table == "contract_receipts" {
			wanted = 2
		}
		if count != wanted {
			t.Fatal("transition audit partial write", table, count)
		}
	}
	db.Pool.Exec(t.Context(), "DROP TRIGGER reject_contract ON audit_events")
	sid2, iid2 := billingSchedulerFixture(t, db, uid, product(t, db, 10), 715, false)
	db.Pool.Exec(t.Context(), "UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1", iid2)
	w = request(h, "POST", "/api/v1/contracts", map[string]any{"service_id": sid2, "template_id": tid}, customer, "contract-second-create")
	expect(t, w, 200)
	id2 := int64(data(t, w)["id"].(float64))
	expect(t, request(h, "POST", "/api/v1/admin/contracts/bulk-void", map[string]any{"reason": "批量", "items": []any{map[string]any{"id": id, "expected_version": 1}, map[string]any{"id": id2, "expected_version": 99}}}, root, "contract-bulk-stale"), 409)
	var unchanged int
	db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM contracts WHERE status='draft' AND version=1").Scan(&unchanged)
	if unchanged != 2 {
		t.Fatal("bulk stale changed first", unchanged)
	}
	for _, table := range []string{"contract_history", "contract_documents", "contract_receipts"} {
		var count int
		db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count)
		wanted := 2
		if table == "contract_receipts" {
			wanted = 3
		}
		if count != wanted {
			t.Fatal("bulk stale partial write", table, count)
		}
	}
}
func TestContractWaitRechecksSessionAndRoleIncludingReplay(t *testing.T) {
	for _, kind := range []string{"session", "role"} {
		for _, replay := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replay-%t", kind, replay), func(t *testing.T) {
				db, h, root, customer, _, sid, tid := contractTestFixture(t)
				staffID, staff := user(t, db, h, "contract-wait-staff", "admin")
				contentRole(t, db, staffID, "contracts.view", "contracts.manage")
				w := request(h, "POST", "/api/v1/contracts", map[string]any{"service_id": sid, "template_id": tid}, customer, "contract-wait-create")
				expect(t, w, 200)
				id := int64(data(t, w)["id"].(float64))
				path := fmt.Sprintf("/api/v1/admin/contracts/%d/void", id)
				body := map[string]any{"expected_version": 1, "reason": "等待锁"}
				key := "contract-wait-operation"
				if replay {
					expect(t, request(h, "POST", path, body, staff, key), 200)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				tx, err := db.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108190)"); err != nil {
					t.Fatal(err)
				}
				done := make(chan int, 1)
				go func() { done <- request(h, "POST", path, body, staff, key).Code }()
				for {
					var waiting bool
					if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query=$1)", "SELECT pg_advisory_xact_lock_shared(70108190)").Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					select {
					case code := <-done:
						t.Fatal("request did not wait", code)
					case <-ctx.Done():
						t.Fatal("no observed lock wait")
					case <-time.After(10 * time.Millisecond):
					}
				}
				want := 403
				if kind == "session" {
					want = 401
					_, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", staffID)
				} else {
					_, err = tx.Exec(ctx, "UPDATE admin_roles SET permissions=ARRAY['contracts.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case code := <-done:
					if code != want {
						t.Fatal("wait authorization", code, want)
					}
				case <-ctx.Done():
					t.Fatal("request did not complete")
				}
				var version int
				db.Pool.QueryRow(t.Context(), "SELECT version FROM contracts WHERE id=$1", id).Scan(&version)
				wanted := 1
				if replay {
					wanted = 2
				}
				if version != wanted {
					t.Fatal("denied operation wrote", version)
				}
				expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/contracts/%d", id), nil, root, ""), 200)
			})
		}
	}
}
func TestContractOwnedAssignedScopesAndReceiptReplay(t *testing.T) {
	db, h, root, customer, uid, sid, tid := contractTestFixture(t)
	staffID, staff := user(t, db, h, "contract-scope-staff", "admin")
	otherID, _ := user(t, db, h, "contract-scope-other-admin", "admin")
	contentRole(t, db, staffID, "contracts.view", "contracts.manage")
	db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID)
	db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, uid)
	w := request(h, "POST", "/api/v1/contracts", map[string]any{"service_id": sid, "template_id": tid}, customer, "contract-scope-create")
	expect(t, w, 200)
	id := int64(data(t, w)["id"].(float64))
	path := fmt.Sprintf("/api/v1/admin/contracts/%d", id)
	expect(t, request(h, "GET", path, nil, staff, ""), 200)
	body := map[string]any{"expected_version": 1, "reason": "所属客户"}
	expect(t, request(h, "POST", path+"/void", body, staff, "contract-scope-void"), 200)
	db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=$2", otherID, uid)
	expect(t, request(h, "GET", path, nil, staff, ""), 404)
	expect(t, request(h, "POST", path+"/void", body, staff, "contract-scope-void"), 404)
	w = request(h, "GET", "/api/v1/admin/contracts", nil, staff, "")
	expect(t, w, 200)
	if data(t, w)["total"] != float64(0) {
		t.Fatal("owned list leaked")
	}
	db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='assigned' WHERE id=$1", staffID)
	expect(t, request(h, "GET", path, nil, staff, ""), 200)
	expect(t, request(h, "POST", path+"/void", body, staff, "contract-scope-void"), 200)
	db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=NULL WHERE id=$1", uid)
	expect(t, request(h, "GET", path, nil, staff, ""), 404)
	expect(t, request(h, "POST", path+"/void", body, staff, "contract-scope-void"), 404)
	w = request(h, "GET", "/api/v1/admin/contracts", nil, staff, "")
	expect(t, w, 200)
	if data(t, w)["total"] != float64(0) {
		t.Fatal("ownerless list leaked")
	}
	expect(t, request(h, "GET", path, nil, root, ""), 200)
}
func TestContractTemplateListReads(t *testing.T) {
	_, h, root, customer, _, _, _ := contractTestFixture(t)
	expect(t, request(h, "GET", "/api/v1/admin/contract-templates", nil, root, ""), 200)
	expect(t, request(h, "GET", "/api/v1/contract-templates", nil, customer, ""), 200)
}
func TestContractSettingsWaitAuthorizationAndReplay(t *testing.T) {
	for _, kind := range []string{"session", "role"} {
		for _, replay := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replay-%t", kind, replay), func(t *testing.T) {
				db, h, _, _, _, _, _ := contractTestFixture(t)
				staffID, staff := user(t, db, h, "contract-settings-wait-staff", "admin")
				contentRole(t, db, staffID, "settings.view", "settings.manage")
				body := map[string]any{"expected_version": 1, "enabled": false}
				key := "contract-settings-wait"
				if replay {
					expect(t, request(h, "PUT", "/api/v1/admin/contract-settings", body, staff, key), 200)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				tx, err := db.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108190)"); err != nil {
					t.Fatal(err)
				}
				done := make(chan int, 1)
				go func() { done <- request(h, "PUT", "/api/v1/admin/contract-settings", body, staff, key).Code }()
				for {
					var waiting bool
					if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query=$1)", "SELECT pg_advisory_xact_lock_shared(70108190)").Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					select {
					case code := <-done:
						t.Fatal("settings did not wait", code)
					case <-ctx.Done():
						t.Fatal("no settings lock wait")
					case <-time.After(10 * time.Millisecond):
					}
				}
				want := 403
				if kind == "session" {
					want = 401
					_, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", staffID)
				} else {
					_, err = tx.Exec(ctx, "UPDATE admin_roles SET permissions=ARRAY['settings.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case code := <-done:
					if code != want {
						t.Fatal("settings authorization", code, want)
					}
				case <-ctx.Done():
					t.Fatal("settings did not complete")
				}
				var version int
				db.Pool.QueryRow(t.Context(), "SELECT version FROM contract_settings").Scan(&version)
				wanted := 1
				if replay {
					wanted = 2
				}
				if version != wanted {
					t.Fatal("denied settings wrote", version)
				}
			})
		}
	}
}
