package httpapi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestSupplierConsolePasswordRescueAndReinstall(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm")
	sid, _ := f.purchase(t, f.pid, "supplier-console-purchase")
	j := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/services/%d", sid)
	// The real supplier may not have supplied an initial password yet.
	expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.root, ""), 409)
	expect(t, request(f.h, "POST", path+"/vnc", map[string]any{}, f.reader, ""), 403)
	expect(t, request(f.h, "POST", path+"/vnc", map[string]any{}, f.other, ""), 401)
	w := request(f.h, "POST", path+"/vnc", map[string]any{}, f.root, "")
	expect(t, w, 200)
	if w.Header().Get("Cache-Control") != "no-store" || data(t, w)["url"] != "https://console.example/vnc?token=private-console-token" || strings.Contains(w.Body.String(), "private-console-password") {
		t.Fatal("unsafe console response")
	}
	f.consoleURL.Store("javascript:alert(1)")
	expect(t, request(f.h, "POST", path+"/vnc", map[string]any{}, f.root, ""), 409)
	f.consoleURL.Store("https://console.example/vnc?token=private-console-token")
	password := "supplier-fixture-NewPassword123!"
	body := map[string]any{"password": password}
	w = request(f.h, "POST", path+"/reset-password", body, f.root, "supplier-detail-password")
	expect(t, w, 202)
	expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.root, ""), 409)
	expect(t, request(f.h, "POST", path+"/reset-password", body, f.root, "supplier-detail-password"), 200)
	expect(t, request(f.h, "POST", path+"/reset-password", map[string]any{"password": "different-fixture-password!"}, f.root, "supplier-detail-password"), 409)
	j = f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	state, _, running := f.state(t, j)
	if state != "waiting_verification" || running == nil {
		t.Fatal("unconfirmed password cleared account lease")
	}
	verify := store.SupplierVerificationInput{JobID: j.ID, ExpectedAttempts: j.Attempts, UpstreamID: 42, Outcome: "delivered", Note: "checked fixture console response"}
	expect(t, request(f.h, "POST", path+"/supplier-verification", verify, f.root, ""), 409)
	f.systemPassword.Store(password)
	expect(t, request(f.h, "POST", path+"/supplier-verification", verify, f.root, ""), 200)
	w = request(f.h, "POST", path+"/credentials", map[string]any{}, f.root, "")
	expect(t, w, 200)
	if data(t, w)["password"] != password {
		t.Fatal("verified supplier credentials missing")
	}
	rescue := map[string]any{"action": "rescue_system", "system": 1, "temp_password": "supplier-fixture-Rescue123!"}
	expect(t, request(f.h, "POST", path+"/actions", rescue, f.reader, "supplier-detail-rescue-denied"), 403)
	expect(t, request(f.h, "POST", path+"/actions", rescue, f.root, "supplier-detail-rescue"), 202)
	expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.root, ""), 409)
	j = f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/actions", map[string]any{"action": "exit_rescue_system"}, f.root, "supplier-detail-exit"), 202)
	j = f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	w = request(f.h, "GET", path+"/reinstall-templates", nil, f.root, "")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), "Debian 13") || !strings.Contains(w.Body.String(), "administrator") {
		t.Fatal("supplier reinstall catalogue missing")
	}
	reinstall := map[string]any{"template_id": 17, "data_loss_acknowledged": true}
	expect(t, request(f.h, "POST", path+"/reinstall", reinstall, f.reader, "supplier-detail-reinstall-denied"), 403)
	expect(t, request(f.h, "POST", path+"/reinstall", map[string]any{"template_id": 17}, f.root, "supplier-detail-reinstall-no-ack"), 409)
	expect(t, request(f.h, "POST", path+"/reinstall", map[string]any{"template_id": 999, "data_loss_acknowledged": true}, f.root, "supplier-detail-reinstall-invalid"), 409)
	expect(t, request(f.h, "POST", path+"/reinstall", reinstall, f.root, "supplier-detail-reinstall"), 202)
	expect(t, request(f.h, "POST", path+"/reinstall", reinstall, f.root, "supplier-detail-reinstall"), 200)
	j = f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	state, _, running = f.state(t, j)
	if state != "waiting_verification" || running == nil {
		t.Fatal("reinstall acceptance treated as completed")
	}
	verify.JobID, verify.ExpectedAttempts = j.ID, j.Attempts
	expect(t, request(f.h, "POST", path+"/supplier-verification", verify, f.root, ""), 409)
	f.systemOS.Store("Debian 13")
	expect(t, request(f.h, "POST", path+"/supplier-verification", verify, f.root, ""), 200)
	var leaked bool
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM audit_events WHERE payload::text LIKE '%private-console%' OR payload::text LIKE '%supplier-fixture-%') OR EXISTS(SELECT 1 FROM provision_jobs WHERE result::text LIKE '%supplier-fixture-%' OR request_payload::text LIKE '%supplier-fixture-%')`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked || f.actions.Load() != 4 {
		t.Fatal("unexpected write count or leaked secrets")
	}
}

func TestSupplierReadonlySyncFailureReleasesAccount(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm")
	sid, _ := f.purchase(t, f.pid, "supplier-sync-release-purchase")
	j := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	f.syncFailure.Store(true)
	path := fmt.Sprintf("/api/v1/admin/services/%d/actions", sid)
	expect(t, request(f.h, "POST", path, map[string]any{"action": "sync"}, f.root, "supplier-sync-release-failure"), 202)
	j = f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	state, svc, running := f.state(t, j)
	if state != "failed" || svc != "active" || running != nil {
		t.Fatal("read-only sync failure froze supplier account")
	}
	if f.actions.Load() != 0 {
		t.Fatal("read-only sync sent supplier write")
	}
}
