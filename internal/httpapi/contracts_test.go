package httpapi

import (
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"strings"
	"sync"
	"testing"
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
