package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func seedMissingSupplier(t *testing.T, db *store.Store) int64 {
	t.Helper()
	var id int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO supplier_accounts(name,provider,base_url,username,credentials_encrypted,active)
VALUES('Missing implementation','removed_compiled_supplier','https://unchanged.invalid/api','unchanged-user',convert_to('preserve-opaque-ciphertext','UTF8'),true) RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func supplierDisableInput(version int64) map[string]any {
	return map[string]any{"disable_only": true, "active": false, "expected_version": version}
}

// Fault injection is restricted to each test's disposable schema. Production
// APIs and SQL must keep the same identity guard enabled at all times.
func supplierFixtureIdentityWrite(t *testing.T, db *store.Store, query string, args ...any) {
	t.Helper()
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), "ALTER TABLE supplier_accounts DISABLE TRIGGER supplier_account_identity_immutable"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), "ALTER TABLE supplier_accounts ENABLE TRIGGER supplier_account_identity_immutable"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func assertMissingSupplierUnchanged(t *testing.T, db *store.Store, id, version int64, active bool) {
	t.Helper()
	var unchanged bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT name='Missing implementation' AND provider='removed_compiled_supplier'
AND base_url='https://unchanged.invalid/api' AND username='unchanged-user'
AND credentials_encrypted=convert_to('preserve-opaque-ciphertext','UTF8') AND version=$2 AND active=$3
FROM supplier_accounts WHERE id=$1`, id, version, active).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("supplier identity, credentials or version changed unexpectedly: %v", err)
	}
}

func TestMissingSupplierDisablePreservesIdentityCredentialsAndCAS(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "missing-supplier-admin", "super_admin")
	id := seedMissingSupplier(t, db)
	path := fmt.Sprintf("/api/v1/admin/suppliers/%d", id)
	full := map[string]any{"name": "Missing implementation", "provider": "removed_compiled_supplier", "base_url": "https://unchanged.invalid/api", "username": "unchanged-user", "active": false, "expected_version": 1}
	// The compatibility editor cannot create, enable or rewrite an unknown
	// implementation. Only the explicit minimal disable request is permitted.
	expect(t, request(h, "POST", "/api/v1/admin/suppliers", full, admin, ""), http.StatusConflict)
	expect(t, request(h, "PUT", path, full, admin, ""), http.StatusConflict)
	for _, extra := range []map[string]any{{"name": "changed"}, {"password": "replacement"}, {"active": true}, {"provider": "other"}, {"base_url": "https://other.invalid"}, {"username": "other"}} {
		input := supplierDisableInput(1)
		for key, value := range extra {
			input[key] = value
		}
		expect(t, request(h, "PUT", path, input, admin, ""), http.StatusConflict)
	}
	expect(t, request(h, "POST", "/api/v1/admin/suppliers", supplierDisableInput(1), admin, ""), http.StatusConflict)
	// Opaque legacy ciphertext and a missing encryption key cannot prevent a
	// safe stop: the path reads neither credentials nor connection fields.
	db.ProvisionEncryptionKey = nil
	response := request(h, "PUT", path, supplierDisableInput(1), admin, "")
	expect(t, response, http.StatusOK)
	if strings.Contains(response.Body.String(), "preserve-opaque-ciphertext") {
		t.Fatal("disable response exposed credential bytes")
	}
	assertMissingSupplierUnchanged(t, db, id, 2, false)
	expect(t, request(h, "PUT", path, supplierDisableInput(1), admin, ""), http.StatusConflict)
	expect(t, request(h, "PUT", path, supplierDisableInput(2), admin, ""), http.StatusConflict)
	full["active"], full["expected_version"] = true, 2
	expect(t, request(h, "PUT", path, full, admin, ""), http.StatusConflict)
	var audits int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE entity_type='supplier' AND entity_id=$1 AND action='supplier.disabled' AND payload->>'provider'='removed_compiled_supplier' AND payload->>'password_changed'='false'`, fmt.Sprint(id)).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("disable audit not atomic or replay wrote again: %d %v", audits, err)
	}
}

func TestMissingSupplierDisableAuditFailureAndPermission(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "missing-supplier-audit-admin", "super_admin")
	readerID, reader := user(t, db, h, "missing-supplier-reader", "admin")
	contentRole(t, db, readerID, "products.view")
	_, customer := user(t, db, h, "missing-supplier-customer", "customer")
	id := seedMissingSupplier(t, db)
	path := fmt.Sprintf("/api/v1/admin/suppliers/%d", id)
	expect(t, request(h, "PUT", path, supplierDisableInput(1), "", ""), http.StatusUnauthorized)
	for _, cookie := range []string{reader, customer} {
		expect(t, request(h, "PUT", path, supplierDisableInput(1), cookie, ""), http.StatusForbidden)
	}
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_supplier_disable_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='supplier.disabled' THEN RAISE EXCEPTION 'injected supplier audit failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_supplier_disable_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_supplier_disable_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PUT", path, supplierDisableInput(1), admin, ""), http.StatusInternalServerError)
	assertMissingSupplierUnchanged(t, db, id, 1, true)
}

func TestMissingSupplierDisableKeepsRunningJobGuard(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm")
	sid, _ := f.purchase(t, f.pid, "missing-supplier-running-job")
	var job int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM provision_jobs WHERE service_id=$1 ORDER BY id LIMIT 1", sid).Scan(&job); err != nil {
		t.Fatal(err)
	}
	supplierFixtureIdentityWrite(t, f.db, "UPDATE supplier_accounts SET provider='removed_compiled_supplier',running_job_id=$2 WHERE id=$1", f.supplierID, job)
	response := request(f.h, "PUT", fmt.Sprintf("/api/v1/admin/suppliers/%d", f.supplierID), supplierDisableInput(1), f.root, "")
	expect(t, response, http.StatusConflict)
	if !strings.Contains(response.Body.String(), "supplier_busy") {
		t.Fatal("missing implementation bypassed running job guard", response.Body.String())
	}
}

func TestMissingSupplierDisableRechecksRevocationAfterLock(t *testing.T) {
	for _, revoke := range []string{"session", "permission"} {
		t.Run(revoke, func(t *testing.T) {
			db, h := testServer(t)
			writerID, writer := user(t, db, h, "supplier-revoked-writer", "admin")
			contentRole(t, db, writerID, "products.view", "products.manage")
			id := seedMissingSupplier(t, db)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			tx, err := db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			var blocker int
			if err = tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			if revoke == "session" {
				_, err = tx.Exec(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", writerID)
			} else {
				_, err = tx.Exec(ctx, "SELECT id FROM admin_roles WHERE id=(SELECT admin_role_id FROM users WHERE id=$1) FOR UPDATE", writerID)
			}
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan int, 1)
			go func() {
				result <- request(h, "PUT", fmt.Sprintf("/api/v1/admin/suppliers/%d", id), supplierDisableInput(1), writer, "").Code
			}()
			for {
				var waiting bool
				if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))", blocker).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("supplier disable did not reach revocation lock")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if revoke == "session" {
				_, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", writerID)
			} else {
				_, err = tx.Exec(ctx, "UPDATE admin_roles SET permissions=ARRAY['products.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", writerID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case status := <-result:
				want := http.StatusForbidden
				if revoke == "session" {
					want = http.StatusUnauthorized
				}
				if status != want {
					t.Fatalf("revoked %s status=%d, want=%d", revoke, status, want)
				}
			case <-ctx.Done():
				t.Fatal("revoked supplier write did not finish")
			}
			assertMissingSupplierUnchanged(t, db, id, 1, true)
		})
	}
}
