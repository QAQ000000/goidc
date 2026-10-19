package httpapi

import (
	"crypto/aes"
	"crypto/cipher"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestCartAdmissionAvailabilityAndRecovery(t *testing.T) {
	db, h := testServer(t)
	uid, customer := user(t, db, h, "cart-admission-customer", "customer")
	blockedProduct := product(t, db, 10)
	allowedProduct := product(t, db, 10)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET require_verified_phone=true WHERE id=$1", blockedProduct); err != nil {
		t.Fatal(err)
	}
	add(t, h, customer, blockedProduct, 1)
	add(t, h, customer, allowedProduct, 1)
	check := func(blocked bool) {
		t.Helper()
		response := request(h, "GET", "/api/v1/cart", nil, customer, "")
		expect(t, response, 200)
		items := data(t, response)["items"].([]any)
		if len(items) != 2 {
			t.Fatalf("cart lost items: %v", items)
		}
		for _, value := range items {
			item := value.(map[string]any)
			denied := blocked && int64(item["product_id"].(float64)) == blockedProduct
			if item["active"] != !denied || item["current"] != true || item["options_valid"] != true || item["fields_valid"] != true {
				t.Fatalf("incorrect cart availability: %v", item)
			}
			if denied {
				if item["admission_code"] != "order_phone_required" || item["admission_error"] == "" {
					t.Fatalf("missing admission error: %v", item)
				}
			} else if _, exists := item["admission_code"]; exists {
				t.Fatalf("eligible item retained admission error: %v", item)
			}
		}
	}
	check(true)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone='+12025550126' WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone_verified=true WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestOrderSettingsAdmissionAndReceipts(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "order-settings-root", "super_admin")
	customerID, customer := user(t, db, h, "order-settings-customer", "customer")
	input := map[string]any{"require_verified_phone": true, "require_certification": false, "expected_version": 1}
	expect(t, request(h, "GET", "/api/v1/admin/order-settings", nil, customer, ""), 401)
	expect(t, request(h, "PUT", "/api/v1/admin/order-settings", input, root, ""), 409)
	expect(t, request(h, "PUT", "/api/v1/admin/order-settings", input, root, "order-settings-update-one"), 200)
	expect(t, request(h, "PUT", "/api/v1/admin/order-settings", input, root, "order-settings-update-one"), 200)
	expect(t, request(h, "PUT", "/api/v1/admin/order-settings", input, root, "order-settings-update-stale"), 409)
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	var productID int64
	if err = tx.QueryRow(t.Context(), "INSERT INTO products(name,slug,product_type,billing_cycle,price_cents) VALUES('Admission','admission-order','hosting','monthly',100) RETURNING id").Scan(&productID); err != nil {
		t.Fatal(err)
	}
	check := func(code string) {
		t.Helper()
		err := db.CheckOrderAdmission(t.Context(), tx, customerID, productID, true)
		if code == "" {
			if err != nil {
				t.Fatal(err)
			}
			return
		}
		conflict, ok := err.(*store.Conflict)
		if !ok || conflict.Code != code {
			t.Fatalf("expected %s, got %v", code, err)
		}
	}
	check("order_phone_required")
	if _, err = tx.Exec(t.Context(), "UPDATE users SET phone='+12025550123' WHERE id=$1", customerID); err != nil {
		t.Fatal(err)
	}
	check("order_phone_required")
	if _, err = tx.Exec(t.Context(), "UPDATE users SET phone_verified=true WHERE id=$1", customerID); err != nil {
		t.Fatal(err)
	}
	check("")
	if _, err = tx.Exec(t.Context(), "UPDATE products SET require_certification=true WHERE id=$1", productID); err != nil {
		t.Fatal(err)
	}
	check("order_certification_required")
	var profileID int64
	if err = tx.QueryRow(t.Context(), "INSERT INTO certification_profiles(user_id,status) VALUES($1,'approved') RETURNING id", customerID).Scan(&profileID); err != nil {
		t.Fatal(err)
	}
	check("order_certification_required")
	block, _ := aes.NewCipher(db.ProvisionEncryptionKey)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	payload := gcm.Seal(nonce, nonce, []byte(`{"name":"Fixture","id_number":"TEST-ID"}`), []byte("fixture"))
	if _, err = tx.Exec(t.Context(), "INSERT INTO certification_submissions(profile_id,user_id,certification_type,provider_id,status,payload_encrypted,payload_aad,reviewed_at) VALUES($1,$2,'individual','manual','approved',$3,'fixture',now())", profileID, customerID, payload); err != nil {
		t.Fatal(err)
	}
	check("")
	if _, err = tx.Exec(t.Context(), "UPDATE certification_profiles SET status='pending' WHERE id=$1", profileID); err != nil {
		t.Fatal(err)
	}
	check("order_certification_required")
	if _, err = tx.Exec(t.Context(), "UPDATE certification_settings SET enabled=false"); err != nil {
		t.Fatal(err)
	}
	check("order_certification_unavailable")
	changed := map[string]any{"require_verified_phone": false, "require_certification": false, "expected_version": 1}
	expect(t, request(h, "PUT", "/api/v1/admin/order-settings", changed, root, "order-settings-update-one"), 409)
	out := data(t, request(h, "GET", "/api/v1/admin/order-settings", nil, root, ""))
	if out["version"] != float64(2) {
		t.Fatalf("unexpected version %v", out["version"])
	}
}

func TestOrderAdmissionCheckoutAndReplay(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "admission-http-root", "super_admin")
	uid, customer := user(t, db, h, "admission-http-customer", "customer")
	pid := product(t, db, 10)
	version, itemID := add(t, h, customer, pid, 1)
	body := store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}
	setting := map[string]any{"require_verified_phone": true, "require_certification": false, "expected_version": 1}
	expect(t, request(h, "PUT", "/api/v1/admin/order-settings", setting, root, "http-order-settings-one"), 200)
	cartResponse := request(h, "GET", "/api/v1/cart", nil, customer, "")
	expect(t, cartResponse, 200)
	row := data(t, cartResponse)["items"].([]any)[0].(map[string]any)
	if row["admission_code"] != "order_phone_required" {
		t.Fatalf("missing admission failure %v", row["admission_code"])
	}
	denied := request(h, "POST", "/api/v1/orders", body, customer, "order-admission-http-key")
	expect(t, denied, 409)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone='+12025550124' WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/orders", body, customer, "order-admission-http-key"), 409)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone_verified=true WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/orders", body, customer, "order-admission-http-key"), 201)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone_verified=false WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	replay := request(h, "POST", "/api/v1/orders", body, customer, "order-admission-http-key")
	expect(t, replay, 200)
	if data(t, replay)["replayed"] != true {
		t.Fatal("successful receipt did not replay")
	}
	version, itemID = add(t, h, customer, pid, 1)
	body = store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}
	expect(t, request(h, "POST", "/api/v1/orders", body, customer, "order-admission-http-new"), 409)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE order_settings SET require_verified_phone=false; UPDATE certification_settings SET require_for_purchase=true"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/orders", body, customer, "order-admission-http-cert"), 409)
}

func TestAdminOrderAdmission(t *testing.T) {
	f := newMutationOrderFixture(t)
	in := adminOrderQuoted(t, f, adminOrderDraft(t, f), f.root)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE order_settings SET require_verified_phone=true"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/orders/quote", in, f.root, ""), 409)
	expect(t, request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-admission-rule-change"), 409)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET phone='+12025550125' WHERE id=$1", f.customerID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET phone_verified=true WHERE id=$1", f.customerID); err != nil {
		t.Fatal(err)
	}
	in = adminOrderQuoted(t, f, adminOrderDraft(t, f), f.root)
	expect(t, request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-admission-success"), 201)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET phone_verified=false WHERE id=$1", f.customerID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-admission-success"), 200)
	expect(t, request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "admin-admission-new"), 409)
}

func TestOrderSettingsPermissionsAndAuditRollback(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "order-settings-audit-root", "super_admin")
	staffID, staff := user(t, db, h, "order-settings-staff", "admin")
	contentRole(t, db, staffID, "settings.view")
	input := map[string]any{"require_verified_phone": true, "require_certification": false, "expected_version": 1}
	expect(t, request(h, "GET", "/api/v1/admin/order-settings", nil, staff, ""), 200)
	expect(t, request(h, "PUT", "/api/v1/admin/order-settings", input, staff, "order-settings-staff-one"), 403)
	contentRole(t, db, staffID, "settings.view", "settings.manage")
	expect(t, request(h, "PUT", "/api/v1/admin/order-settings", input, staff, "order-settings-staff-one"), 200)
	contentRole(t, db, staffID, "settings.view")
	expect(t, request(h, "PUT", "/api/v1/admin/order-settings", input, staff, "order-settings-staff-one"), 403)
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_order_settings_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='order.settings_updated' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_order_settings_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_order_settings_audit()`); err != nil {
		t.Fatal(err)
	}
	input["expected_version"] = 2
	input["require_verified_phone"] = false
	expect(t, request(h, "PUT", "/api/v1/admin/order-settings", input, root, "order-settings-audit-fail"), 500)
	current := data(t, request(h, "GET", "/api/v1/admin/order-settings", nil, root, ""))
	if current["version"] != float64(2) || current["require_verified_phone"] != true {
		t.Fatal("audit failure escaped rollback")
	}
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM order_settings_receipts WHERE idempotency_key='order-settings-audit-fail'").Scan(&count); err != nil || count != 0 {
		t.Fatal("audit failure left receipt", err)
	}
}
