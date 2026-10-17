package httpapi

import (
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestGlobalConfigurationAuditRollbackAndRevokedReplay(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "global-safety-editor", "admin")
	_, root := user(t, db, h, "global-safety-root", "super_admin")
	contentRole(t, db, staffID, "products.view", "products.manage")
	path := "/api/v1/admin/global-configuration-groups"
	input := store.GlobalConfigurationInput{Name: "审计原子性验收"}
	expect(t, request(h, "POST", path, input, staff, "global-safety-created"), 200)
	contentRole(t, db, staffID, "products.view")
	expect(t, request(h, "POST", path, input, staff, "global-safety-created"), 403)
	contentRole(t, db, staffID, "products.view", "products.manage")
	expect(t, request(h, "POST", path, input, staff, "global-safety-created"), 200)
	if _, err := db.Pool.Exec(t.Context(), `DELETE FROM sessions WHERE user_id=$1`, staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", path, input, staff, "global-safety-created"), 401)
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_global_configuration_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='global_configuration.save' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_global_configuration_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_global_configuration_audit()`); err != nil {
		t.Fatal(err)
	}
	input.Name = "不能留下部分写入"
	expect(t, request(h, "POST", path, input, root, "global-safety-audit-failure"), 500)
	var groups, receipts int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM global_configuration_groups WHERE name=$1`, input.Name).Scan(&groups); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM mutation_operations WHERE scope LIKE 'admin:%' AND idempotency_key='global-safety-audit-failure'`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if groups != 0 || receipts != 0 {
		t.Fatalf("audit failure left partial state: groups=%d receipts=%d", groups, receipts)
	}
}
