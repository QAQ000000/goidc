package httpapi

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestSingletonSettingsAuditFailureLeavesNoMutationOrReceipt(t *testing.T) {
	db, h := testServer(t)
	actor, root := user(t, db, h, "singleton-rollback-root", "super_admin")
	// One fault exercises every endpoint using the shared commit workflow.
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_singleton_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action LIKE '%.settings_updated' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_singleton_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_singleton_audit()`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, table, operation string
		input                  map[string]any
	}{
		{"sales-settings", "sales_settings", "sales.settings", map[string]any{"order_assignment": "random", "registration_assignment": "none"}},
		{"scheduler-settings", "scheduler_task_settings", "scheduler.settings", map[string]any{"enabled": false, "settings": map[string]any{"expiry_days": []int{3}, "billing_grace_days": 5}}},
		{"certification-settings", "certification_settings", "certification.settings", map[string]any{
			"enabled": true, "require_for_purchase": true, "default_provider_id": "manual", "advanced_enabled": true,
			"auto_update_name": true, "require_phone_match": true, "notify_on_approval": true, "suspend_unverified": true, "allow_file_upload": true,
		}},
		{"auth-settings", "auth_settings", "auth.settings", authSettingsInput(t, h, root)},
		{"security/mfa", "mfa_settings", "mfa.settings", map[string]any{"customer_enabled": false, "admin_enabled": false}},
		{"finance-settings", "finance_settings", "finance_settings", map[string]any{"topup_enabled": false, "min_topup_cents": 123, "max_topup_cents": 456, "max_balance_cents": 0, "require_active_order": true}},
		{"invoice-settings", "invoice_settings", "invoice_settings", map[string]any{"enabled": true, "rate_percent": 3.5}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			path := "/api/v1/admin/" + tc.path
			before := request(h, "GET", path, nil, root, "")
			expect(t, before, 200)
			tc.input["expected_version"] = data(t, before)["version"]
			key := "singleton-rollback-" + tc.path
			// Capture both tables for auth-settings, which updates the site row too.
			snapshot := func() string {
				var state string
				if err := db.Pool.QueryRow(t.Context(), "SELECT row_to_json(t)::text || (SELECT row_to_json(s)::text FROM site_settings s WHERE id=1) FROM "+pgx.Identifier{tc.table}.Sanitize()+" t WHERE id=1").Scan(&state); err != nil {
					t.Fatal(err)
				}
				return state
			}
			original := snapshot()
			expect(t, request(h, "PUT", path, tc.input, root, key), 500)
			if snapshot() != original {
				t.Fatal("audit failure left a partial settings update")
			}
			var receipts int
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mutation_operations WHERE actor_id=$1 AND scope=$2 AND idempotency_key=$3", actor, "admin:"+tc.operation, key).Scan(&receipts); err != nil || receipts != 0 {
				t.Fatalf("receipt survived rollback: %d, %v", receipts, err)
			}
			if _, err := db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER reject_singleton_audit"); err != nil {
				t.Fatal(err)
			}
			saved := request(h, "PUT", path, tc.input, root, key)
			expect(t, saved, 200)
			replay := request(h, "PUT", path, tc.input, root, key)
			expect(t, replay, 200)
			if data(t, replay)["replayed"] != true || fmt.Sprint(data(t, replay)["version"]) != fmt.Sprint(data(t, saved)["version"]) {
				t.Fatal("retry did not return the committed result")
			}
			var audits int
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE actor_id=$1 AND entity_type=$2", actor, tc.table).Scan(&audits); err != nil || audits != 1 {
				t.Fatalf("replay duplicated audit: %d, %v", audits, err)
			}
			if _, err := db.Pool.Exec(t.Context(), "ALTER TABLE audit_events ENABLE TRIGGER reject_singleton_audit"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
