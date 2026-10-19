package httpapi

import (
	"fmt"
	"testing"
)

func TestNotificationLifecycleCatalogFilteringAndScope(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "lifecycle-catalog-admin", "super_admin")
	staffID, staff := user(t, db, h, "lifecycle-catalog-staff", "admin")
	uid, customer := user(t, db, h, "lifecycle-catalog-customer", "customer")
	otherID, _ := user(t, db, h, "lifecycle-catalog-other", "customer")
	for _, statement := range []string{
		fmt.Sprintf(`UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=%d`, staffID),
		fmt.Sprintf(`UPDATE users SET sales_owner_id=%d WHERE id=%d`, staffID, uid),
	} {
		if _, err := db.Pool.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	initial := request(h, "GET", "/api/v1/admin/notification-center", nil, admin, "")
	expect(t, initial, 200)
	catalog := data(t, initial)["events"].([]any)
	if len(catalog) != 11 || len(data(t, initial)["rules"].([]any)) != 3*len(catalog) {
		t.Fatal("incomplete lifecycle catalog or routes")
	}
	for _, raw := range catalog {
		event := raw.(map[string]any)
		if event["name"] == "" || len(event["variables"].([]any)) == 0 {
			t.Fatal("catalog omits display name or template variables")
		}
	}
	for _, owner := range []int64{uid, uid, otherID} {
		var orderID int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES('CAT-'||gen_random_uuid()::text,$1,'pending',1000,gen_random_uuid()::text,'fixture') RETURNING id`, owner).Scan(&orderID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `WITH e AS (INSERT INTO notification_events(kind,dedupe_key,user_id,order_id,payload) VALUES('order.created','order.created:'||$1::bigint::text,$2,$1::bigint,'{}') RETURNING id) INSERT INTO notification_deliveries(event_id,route_key,channel,status) SELECT id,'filter-fixture','inbox','skipped' FROM e`, orderID, owner); err != nil {
			t.Fatal(err)
		}
	}
	expect(t, request(h, "PATCH", "/api/v1/admin/notification-center/rules/invoice-paid-inbox", map[string]any{"enabled": true, "template_id": "invoice-paid-inbox", "expected_version": 1}, admin, ""), 200)
	centerInvoice(t, db, uid, "CAT-PAID")
	first := request(h, "GET", "/api/v1/admin/notification-center?event_kind=order.created&limit=1", nil, staff, "")
	expect(t, first, 200)
	firstData := data(t, first)
	firstRows := firstData["deliveries"].([]any)
	if len(firstRows) != 1 || firstData["has_more"] != true || firstRows[0].(map[string]any)["user_id"] != float64(uid) || firstRows[0].(map[string]any)["event_kind"] != "order.created" {
		t.Fatal("event filter bypassed customer scope or pagination", first.Body.String())
	}
	second := request(h, "GET", "/api/v1/admin/notification-center?event_kind=order.created&limit=1&offset=1", nil, staff, "")
	expect(t, second, 200)
	secondData := data(t, second)
	secondRows := secondData["deliveries"].([]any)
	if len(secondRows) != 1 || secondData["has_more"] != false || secondRows[0].(map[string]any)["id"] == firstRows[0].(map[string]any)["id"] {
		t.Fatal("filtered pagination omitted or duplicated a record", second.Body.String())
	}
	paid := request(h, "GET", "/api/v1/admin/notification-center?event_kind=invoice.paid", nil, staff, "")
	expect(t, paid, 200)
	if len(data(t, paid)["deliveries"].([]any)) != 1 {
		t.Fatal("payment deliveries leaked into another event or disappeared")
	}
	expect(t, request(h, "GET", "/api/v1/admin/notification-center?event_kind=unknown", nil, admin, ""), 400)
	expect(t, request(h, "GET", "/api/v1/admin/notification-center?event_kind=order.created", nil, customer, ""), 401)
}

func TestNotificationLifecycleExplicitConsentAndAuditRollback(t *testing.T) {
	db, h := testServer(t)
	uid, customer := user(t, db, h, "lifecycle-consent-customer", "customer")
	path := "/api/v1/me/notification-preferences"
	old := request(h, "PATCH", path, map[string]any{"channel": "inbox", "enabled": true, "expected_version": 0}, customer, "")
	expect(t, old, 200)
	if data(t, old)["event_scope"] != "invoice.paid" {
		t.Fatal("old client silently subscribed to additional events")
	}
	expect(t, request(h, "PATCH", path, map[string]any{"channel": "inbox", "enabled": true, "expected_version": 1, "event_scope": "all"}, customer, ""), 400)
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_lifecycle_consent_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='notification.preference.updated' THEN RAISE EXCEPTION 'reject consent audit'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_lifecycle_consent_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_lifecycle_consent_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PATCH", path, map[string]any{"channel": "inbox", "enabled": true, "expected_version": 1, "event_scope": "business"}, customer, ""), 500)
	var scope string
	var version int
	if err := db.Pool.QueryRow(t.Context(), `SELECT event_scope,version FROM user_notification_preferences WHERE user_id=$1 AND channel='inbox'`, uid).Scan(&scope, &version); err != nil || scope != "invoice.paid" || version != 1 {
		t.Fatal("failed audit did not rollback widened consent", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `DROP TRIGGER reject_lifecycle_consent_audit ON audit_events`); err != nil {
		t.Fatal(err)
	}
	business := request(h, "PATCH", path, map[string]any{"channel": "inbox", "enabled": true, "expected_version": 1, "event_scope": "business"}, customer, "")
	expect(t, business, 200)
	if data(t, business)["event_scope"] != "business" || data(t, business)["version"] != float64(2) {
		t.Fatal("explicit consent expansion not persisted")
	}
	expect(t, request(h, "PATCH", path, map[string]any{"channel": "inbox", "enabled": false, "expected_version": 1}, customer, ""), 409)
	retained := request(h, "PATCH", path, map[string]any{"channel": "inbox", "enabled": false, "expected_version": 2}, customer, "")
	expect(t, retained, 200)
	if data(t, retained)["event_scope"] != "business" {
		t.Fatal("old client changed existing explicit scope")
	}
}
