package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func policyErrorCode(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	expect(t, w, status)
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != code {
		t.Fatalf("error code %q want %q", body.Error.Code, code)
	}
}

func TestUpgradePolicyAtomicityAndSessionReplay(t *testing.T) {
	for _, kind := range []string{"targets", "configuration"} {
		t.Run(kind, func(t *testing.T) {
			f := newMutationOrderFixture(t)
			target := product(t, f.db, 5)
			path := fmt.Sprintf("/api/v1/admin/products/%d/upgrade-targets", f.productID)
			action := "product.upgrade_targets.updated"
			body := map[string]any{"targets": []map[string]any{{"product_id": target, "allow_downgrade": true}}}
			if kind == "configuration" {
				path = fmt.Sprintf("/api/v1/admin/products/%d/configuration-upgrade-policy", f.productID)
				action = "product.configuration_upgrade_policy.updated"
				body = map[string]any{"enabled": true, "allow_downgrade": true}
			}
			revision := *configRevision(t, f.h, f.root, f.productID)
			body["expected_revision"] = revision
			key := "upgrade-policy-atomicity"
			if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_policy_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('product.upgrade_targets.updated','product.configuration_upgrade_policy.updated') THEN RAISE EXCEPTION 'isolated policy audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_policy_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_policy_audit()`); err != nil {
				t.Fatal(err)
			}
			policyErrorCode(t, request(f.h, "PUT", path, body, f.root, key), 500, "internal_error")
			var current int64
			var targets, receipts int
			var enabled, downgrade bool
			check := func() {
				t.Helper()
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT revision,config_upgrade_enabled,config_downgrade_enabled,(SELECT count(*) FROM product_upgrade_targets WHERE product_id=$1),(SELECT count(*) FROM mutation_operations WHERE scope LIKE 'admin:%' AND idempotency_key=$2) FROM products WHERE id=$1`, f.productID, key).Scan(&current, &enabled, &downgrade, &targets, &receipts); err != nil {
					t.Fatal(err)
				}
			}
			check()
			if current != revision || enabled || downgrade || targets != 0 || receipts != 0 {
				t.Fatal("failed audit persisted business state or receipt")
			}
			if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER reject_policy_audit ON audit_events"); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			responses := make(chan *httptest.ResponseRecorder, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); responses <- request(f.h, "PUT", path, body, f.root, key) }()
			}
			wg.Wait()
			close(responses)
			for w := range responses {
				expect(t, w, 200)
			}
			check()
			if current != revision+1 || receipts != 1 {
				t.Fatal("concurrent replay advanced revision or duplicated receipt")
			}
			if kind == "targets" && targets != 1 || kind == "configuration" && (!enabled || !downgrade) {
				t.Fatal("successful policy missing")
			}
			var audits int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action=$1 AND entity_id=$2`, action, fmt.Sprint(f.productID)).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if audits != 1 {
				t.Fatal("duplicate audit")
			}
			body["expected_revision"] = revision + 1
			policyErrorCode(t, request(f.h, "PUT", path, body, f.root, key), 409, "idempotency_conflict")
			body["expected_revision"] = revision
			// Revoke after HTTP authentication but while the store transaction is
			// waiting: a handler that omits requestFileContext would replay 200.
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			lock, err := f.db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback(context.Background())
			if _, err = lock.Exec(ctx, "SELECT pg_advisory_xact_lock(70108190)"); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- request(f.h, "PUT", path, body, f.root, key) }()
			for {
				var waiting bool
				if err = f.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query=$1)`, "SELECT pg_advisory_xact_lock_shared(70108190)").Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case w := <-done:
					t.Fatalf("request did not wait: %d", w.Code)
				case <-ctx.Done():
					t.Fatal("lock wait not observed")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if _, err = lock.Exec(ctx, `DELETE FROM sessions WHERE user_id=(SELECT id FROM administrators WHERE username='order-mutation-root')`); err != nil {
				t.Fatal(err)
			}
			if err = lock.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-done:
				policyErrorCode(t, w, 401, "unauthorized")
			case <-ctx.Done():
				t.Fatal("revoked request did not finish")
			}

			check()
			if current != revision+1 || receipts != 1 {
				t.Fatal("revoked replay changed state")
			}
		})
	}
}

func TestUpgradePolicyUnsupportedModesAndCleanup(t *testing.T) {
	for _, mode := range []string{"supplier", "nokvm"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationOrderFixture(t)
			target := product(t, f.db, 5)
			if _, err := f.db.Pool.Exec(t.Context(), `UPDATE products SET provision_mode=$2,config_upgrade_enabled=true,config_downgrade_enabled=true WHERE id=$1`, f.productID, mode); err != nil {
				t.Fatal(err)
			}
			revision := *configRevision(t, f.h, f.root, f.productID)
			targetsPath := fmt.Sprintf("/api/v1/admin/products/%d/upgrade-targets", f.productID)
			policyPath := fmt.Sprintf("/api/v1/admin/products/%d/configuration-upgrade-policy", f.productID)
			policyErrorCode(t, request(f.h, "PUT", targetsPath, map[string]any{"expected_revision": revision, "targets": []map[string]any{{"product_id": target}}}, f.root, "unsupported-targets"), 409, "unsupported_supplier_upgrade")
			policyErrorCode(t, request(f.h, "PUT", policyPath, map[string]any{"expected_revision": revision, "enabled": true, "allow_downgrade": false}, f.root, "unsupported-config"), 409, "unsupported_supplier_upgrade")
			if got := *configRevision(t, f.h, f.root, f.productID); got != revision {
				t.Fatal("rejected mode advanced revision")
			}
			expect(t, request(f.h, "PUT", targetsPath, map[string]any{"expected_revision": revision, "targets": []any{}}, f.root, "unsupported-clear"), 200)
			expect(t, request(f.h, "PUT", policyPath, map[string]any{"expected_revision": revision + 1, "enabled": false, "allow_downgrade": false}, f.root, "unsupported-disable"), 200)
			var enabled, downgrade bool
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT config_upgrade_enabled,config_downgrade_enabled FROM products WHERE id=$1`, f.productID).Scan(&enabled, &downgrade); err != nil {
				t.Fatal(err)
			}
			if enabled || downgrade {
				t.Fatal("cleanup did not disable legacy policy")
			}
		})
	}
}
