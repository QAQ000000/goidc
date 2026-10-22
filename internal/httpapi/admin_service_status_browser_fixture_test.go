package httpapi

import (
	"fmt"
	"os"
	"testing"
)

// Real lifecycle completions and audits come from the existing API/worker
// fixture. Only its final local projection is deliberately drifted.
func TestAdminServiceStatusBrowserFixture(t *testing.T) {
	browserFixturePaths(t)
	mode, target := os.Getenv("ZJMFGO_STATUS_BROWSER_MODE"), os.Getenv("ZJMFGO_STATUS_BROWSER_TARGET")
	if mode == "" {
		mode = "manual"
	}
	if target == "" {
		target = "suspended"
	}
	if (mode != "manual" && mode != "simulation") || (target != "active" && target != "suspended") {
		t.Fatal("invalid status browser fixture mode/target")
	}
	f, job := statusCorrectionFixture(t, mode == "manual", target)
	contentRole(t, f.db, f.actor, "services.view", "services.status.correct")
	reader, _ := user(t, f.db, f.h, "status-browser-reader", "admin")
	contentRole(t, f.db, reader, "services.view", "services.manage")
	before, version := statusCorrectionPreservedFacts(t, f), f.version(t)
	t.Logf("status browser fixture: service=%d customer=%d source=%d mode=%s target=%s version=%d", f.sid, f.uid, job, mode, target, version)
	adminBrowserFixtureStore(t, "status-browser-super", f.db, nil)
	if after := statusCorrectionPreservedFacts(t, f); after != before {
		t.Fatal("browser correction changed non-projection business facts")
	}
	var status string
	var receipts, audits int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM services WHERE id=$1", f.sid).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != target || f.version(t) != version+1 {
		t.Fatal("browser correction did not change projection exactly once", status, f.version(t))
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mutation_operations WHERE scope='admin:service.status.correct'").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='service.status.corrected' AND entity_id=$1", fmt.Sprint(f.sid)).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || audits != 1 {
		t.Fatal("browser correction duplicated receipts/audits", receipts, audits)
	}
	t.Log("browser postconditions: one correction/version/audit/receipt; all other business facts preserved")
}
