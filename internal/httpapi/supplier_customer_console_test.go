package httpapi

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSupplierCustomerConsole(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm")
	sid, _ := f.purchase(t, f.pid, "customer-console")
	j := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/services/%d", sid)
	w := request(f.h, "GET", path, nil, f.customer, "")
	expect(t, w, 200)
	caps := data(t, w)["supplier_capabilities"].(map[string]any)
	if caps["vnc"] != true || caps["power"] != true || caps["create"] != false || caps["sync"] != false {
		t.Fatal("incorrect customer capabilities")
	}
	expect(t, request(f.h, "POST", path+"/vnc?admin=true&actor=1&user_id=1", map[string]any{"admin": true, "user_id": 1}, f.other, ""), 404)
	if f.consoleCalls.Load() != 0 {
		t.Fatal("non-owner reached upstream")
	}
	w = request(f.h, "POST", path+"/vnc", map[string]any{}, f.customer, "")
	expect(t, w, 200)
	if w.Header().Get("Cache-Control") != "no-store" || data(t, w)["url"] != "https://console.example/vnc?token=private-console-token" || strings.Contains(w.Body.String(), "private-console-password") {
		t.Fatal("unsafe console response")
	}
	for _, action := range []string{"create", "sync"} {
		expect(t, request(f.h, "POST", path+"/actions", map[string]any{"action": action}, f.customer, "customer-denied-"+action), 403)
	}
	f.consoleURL.Store("javascript:alert(1)")
	expect(t, request(f.h, "POST", path+"/vnc", map[string]any{}, f.customer, ""), 409)
	f.consoleUnknown.Store(true)
	before := f.consoleCalls.Load()
	w = request(f.h, "POST", path+"/vnc", map[string]any{}, f.customer, "")
	if w.Code == 200 || f.consoleCalls.Load() != before+1 {
		t.Fatal("ambiguous console request retried or accepted")
	}
	var leaked bool
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM audit_events WHERE payload::text LIKE '%private-console%')`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("temporary credentials persisted")
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE users SET status='disabled' WHERE id=$1`, f.uid); err != nil {
		t.Fatal(err)
	}
	w = request(f.h, "POST", path+"/vnc", map[string]any{}, f.customer, "")
	if w.Code == 200 {
		t.Fatal("disabled customer accepted")
	}
}

func TestSupplierCustomerConsoleFencesChanges(t *testing.T) {
	for _, change := range []string{"service_version", "supplier_version", "binding", "role", "disabled", "supplier_disabled", "ownership", "admin_permission"} {
		t.Run(change, func(t *testing.T) {
			f := supplierSetup(t)
			f.publish(t, f.pid, "local-vm")
			sid, _ := f.purchase(t, f.pid, "customer-console-fence")
			j := f.claim(t)
			if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
				t.Fatal(err)
			}
			f.consoleEntered = make(chan struct{})
			f.consoleBlocked = make(chan struct{})
			response := make(chan *httptest.ResponseRecorder, 1)
			token, route := f.customer, fmt.Sprintf("/api/v1/services/%d/vnc", sid)
			var adminID int64
			if change == "admin_permission" {
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT id FROM users WHERE username='supplier-reader'`).Scan(&adminID); err != nil {
					t.Fatal(err)
				}
				contentRole(t, f.db, adminID, "services.view", "services.manage")
				token, route = f.reader, fmt.Sprintf("/api/v1/admin/services/%d/vnc", sid)
			}
			go func() { response <- request(f.h, "POST", route, map[string]any{}, token, "") }()
			<-f.consoleEntered
			var query string
			var id int64
			switch change {
			case "admin_permission":
				query = `UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)`
				id = adminID
			case "supplier_disabled":
				query = `UPDATE supplier_accounts SET active=false WHERE id=$1`
				id = f.supplierID
			case "ownership":
				query = `UPDATE services SET user_id=(SELECT id FROM users WHERE username='supplier-other') WHERE id=$1`
				id = sid
			case "service_version":
				query = `UPDATE services SET version=version+1 WHERE id=$1`
				id = sid
			case "supplier_version":
				query = `UPDATE supplier_accounts SET version=version+1 WHERE id=$1`
				id = f.supplierID
			case "binding":
				query = `UPDATE services SET upstream_id='43' WHERE id=$1`
				id = sid
			case "role":
				query = `UPDATE users SET role='admin',admin_role_id=(SELECT id FROM admin_roles LIMIT 1) WHERE id=$1`
				id = f.uid
			case "disabled":
				query = `UPDATE users SET status='disabled' WHERE id=$1`
				id = f.uid
			}
			_, err := f.db.Pool.Exec(t.Context(), query, id)
			close(f.consoleBlocked)
			w := <-response
			if err != nil {
				t.Fatal(err)
			}
			if w.Code == 200 || strings.Contains(w.Body.String(), "private-console") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("changed authorization/resource released console credentials")
			}
			if f.consoleCalls.Load() != 1 {
				t.Fatal("console request retried")
			}
		})
	}
}

// A read-only service detail must not depend on decrypting operational credentials.
func TestSupplierCapabilitiesWithoutCredentials(t *testing.T) {
	for _, change := range []string{"missing_key", "invalid_ciphertext", "unknown_provider"} {
		t.Run(change, func(t *testing.T) {
			f := supplierSetup(t)
			f.publish(t, f.pid, "local-vm")
			sid, _ := f.purchase(t, f.pid, "capability-secret-independent")
			j := f.claim(t)
			if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "missing_key":
				f.db.ProvisionEncryptionKey = nil
			case "invalid_ciphertext":
				if _, err := f.db.Pool.Exec(t.Context(), `UPDATE supplier_accounts SET credentials_encrypted='invalid'::bytea WHERE id=$1`, f.supplierID); err != nil {
					t.Fatal(err)
				}
			case "unknown_provider":
				supplierFixtureIdentityWrite(t, f.db, `UPDATE supplier_accounts SET provider='unknown-compiled-provider' WHERE id=$1`, f.supplierID)
			}
			path := fmt.Sprintf("/api/v1/services/%d", sid)
			for _, caller := range []struct{ path, token string }{{path, f.customer}, {fmt.Sprintf("/api/v1/admin/services/%d", sid), f.root}} {
				w := request(f.h, "GET", caller.path, nil, caller.token, "")
				expect(t, w, 200)
				caps := data(t, w)["supplier_capabilities"].(map[string]any)
				if caps["power"] != (change != "unknown_provider") {
					t.Fatal("incorrect public capabilities")
				}
				if caller.token == f.customer && (caps["create"] != false || caps["sync"] != false) {
					t.Fatal("customer administrator capabilities exposed")
				}
			}
			// Operational console access must still reject unavailable secrets/provider.
			w := request(f.h, "POST", path+"/vnc", map[string]any{}, f.customer, "")
			if w.Code == 200 || f.consoleCalls.Load() != 0 {
				t.Fatal("invalid operational credentials reached upstream")
			}
			// Password writes retain encryption-key validation; do not enqueue with no key.
			if change == "missing_key" {
				expect(t, request(f.h, "POST", path+"/reset-password", map[string]any{"password": "Valid-Test-Password!"}, f.customer, "capability-missing-key-write"), 409)
			}
		})
	}
}

func TestSupplierCapabilitiesInvalidSnapshot(t *testing.T) {
	for _, field := range []string{"supplier_id", "binding_id"} {
		for _, value := range []string{"not-a-number", "9223372036854775808"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				f := supplierSetup(t)
				f.publish(t, f.pid, "local-vm")
				sid, _ := f.purchase(t, f.pid, "invalid-capability-snapshot")
				j := f.claim(t)
				if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
					t.Fatal(err)
				}
				// Model imported historical corruption within this test-only schema.
				if _, err := f.db.Pool.Exec(t.Context(), `ALTER TABLE services DISABLE TRIGGER services_delivery_snapshot_immutable`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.Pool.Exec(t.Context(), `UPDATE services SET delivery_snapshot=jsonb_set(delivery_snapshot,ARRAY[$2::text],to_jsonb($3::text)) WHERE id=$1`, sid, field, value); err != nil {
					t.Fatal(err)
				}
				path := fmt.Sprintf("/api/v1/services/%d", sid)
				w := request(f.h, "GET", path, nil, f.customer, "")
				expect(t, w, 200)
				for _, enabled := range data(t, w)["supplier_capabilities"].(map[string]any) {
					if enabled != false {
						t.Fatal("invalid snapshot enabled operation")
					}
				}
				w = request(f.h, "POST", path+"/vnc", map[string]any{}, f.customer, "")
				if w.Code == 200 || f.consoleCalls.Load() != 0 {
					t.Fatal("invalid snapshot reached upstream")
				}
			})
		}
	}
}

func TestSupplierCapabilitiesUseServiceSnapshot(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm")
	sid, _ := f.purchase(t, f.pid, "immutable-capability-snapshot")
	j := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	// Simulate an out-of-band product rebind in the isolated schema; ordinary writes are immutable.
	if _, err := f.db.Pool.Exec(t.Context(), `ALTER TABLE products DISABLE TRIGGER products_supplier_source`); err != nil {
		t.Fatal(err)
	}
	// Product metadata may change; the purchased service retains its original binding.
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE products SET delivery_config='{"supplier_id":9223372036854775807,"binding_id":9223372036854775807,"provider":"unknown-provider"}'::jsonb WHERE id=$1`, f.pid); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/services/%d", sid)
	w := request(f.h, "GET", path, nil, f.customer, "")
	expect(t, w, 200)
	if data(t, w)["supplier_capabilities"].(map[string]any)["vnc"] != true {
		t.Fatal("current product replaced immutable service capability binding")
	}
	expect(t, request(f.h, "POST", path+"/vnc", map[string]any{}, f.customer, ""), 200)
	if f.consoleCalls.Load() != 1 {
		t.Fatal("original service resource was not used")
	}
}
