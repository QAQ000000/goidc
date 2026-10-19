package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func promoCustomerGroup(t *testing.T, f mutationOrderFixture, name string) int64 {
	t.Helper()
	w := request(f.h, "POST", "/api/v1/admin/customer-groups", map[string]any{"name": name, "color": "#123456", "discount_percent": 0}, f.root, "promo-test-group-"+name)
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}

func expectPromoCustomerGroups(t *testing.T, result map[string]any, ids ...int64) {
	t.Helper()
	groups, ok := result["applies_customer_group_ids"].([]any)
	if !ok || len(groups) != len(ids) {
		t.Fatal("incorrect customer group restriction", result)
	}
	for i, id := range ids {
		if groups[i] != float64(id) {
			t.Fatal("customer groups were not canonical", groups, ids)
		}
	}
}

func TestPromoCustomerGroupsAdminCanonicalVersionedLifecycle(t *testing.T) {
	f := newMutationOrderFixture(t)
	a := promoCustomerGroup(t, f, "Promo group A")
	b := promoCustomerGroup(t, f, "Promo group B")
	in := map[string]any{"code": "CUSTOMER-GROUPS", "discount_type": "percent", "value": 10, "applies_customer_group_ids": []int64{b, a, a}}
	w := request(f.h, "POST", "/api/v1/admin/promo-codes", in, f.root, "promo-groups-create")
	expect(t, w, 201)
	out := data(t, w)
	id := int64(out["id"].(float64))
	expectPromoCustomerGroups(t, out, a, b)
	in["applies_customer_group_ids"] = []int64{a, b}
	replay := request(f.h, "POST", "/api/v1/admin/promo-codes", in, f.root, "promo-groups-create")
	expect(t, replay, 201)
	if data(t, replay)["id"] != out["id"] {
		t.Fatal("canonical request did not replay")
	}
	in["applies_customer_group_ids"] = []int64{b}
	conflict := request(f.h, "POST", "/api/v1/admin/promo-codes", in, f.root, "promo-groups-create")
	expect(t, conflict, 409)
	if !strings.Contains(conflict.Body.String(), "idempotency_conflict") {
		t.Fatal("changed restrictions bypassed receipt binding", conflict.Body.String())
	}
	page := request(f.h, "GET", "/api/v1/admin/promo-codes/page?q=CUSTOMER-GROUPS", nil, f.root, "")
	expect(t, page, 200)
	expectPromoCustomerGroups(t, data(t, page)["items"].([]any)[0].(map[string]any), a, b)
	list := request(f.h, "GET", "/api/v1/admin/promo-codes", nil, f.root, "")
	expect(t, list, 200)
	var listed struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || len(listed.Data) != 1 {
		t.Fatal("list did not include the created promo", err, list.Body.String())
	}
	expectPromoCustomerGroups(t, listed.Data[0], a, b)
	path := fmt.Sprintf("/api/v1/admin/promo-codes/%d", id)
	w = request(f.h, "PATCH", path, map[string]any{"expected_version": 1, "notes": "leave groups alone"}, f.root, "promo-groups-omit")
	expect(t, w, 200)
	expectPromoCustomerGroups(t, data(t, w), a, b)
	expect(t, request(f.h, "PATCH", path, map[string]any{"applies_customer_group_ids": []int64{}}, f.root, "promo-groups-no-version"), 409)
	expect(t, request(f.h, "PATCH", path, map[string]any{"expected_version": 1, "applies_customer_group_ids": []int64{}}, f.root, "promo-groups-stale"), 409)
	clear := map[string]any{"expected_version": 2, "applies_customer_group_ids": []int64{}}
	w = request(f.h, "PATCH", path, clear, f.root, "promo-groups-clear")
	expect(t, w, 200)
	expectPromoCustomerGroups(t, data(t, w))
	expect(t, request(f.h, "PATCH", path, clear, f.root, "promo-groups-clear"), 200)
	var mappings, audits, version int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM promo_customer_groups WHERE promo_id=$1),(SELECT count(*) FROM audit_events WHERE action='promo_code.updated' AND entity_id=$1::text),version FROM promo_codes WHERE id=$1`, id).Scan(&mappings, &audits, &version); err != nil || mappings != 0 || audits != 2 || version != 3 {
		t.Fatal("versioned mapping mutation or replay changed facts", mappings, audits, version, err)
	}
}

func TestPromoCustomerGroupsValidationPermissionsAndOptions(t *testing.T) {
	f := newMutationOrderFixture(t)
	a := promoCustomerGroup(t, f, "Promo selectable A")
	promoCustomerGroup(t, f, "Other selectable B")
	for name, groups := range map[string][]int64{"NEGATIVE": {-1}, "MISSING": {999999999}, "EXCESSIVE": make([]int64, 101)} {
		w := request(f.h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "GROUP-" + name, "discount_type": "percent", "value": 10, "applies_customer_group_ids": groups}, f.root, "group-invalid-"+name)
		expect(t, w, 409)
	}
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM promo_codes").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid group restriction created a promo", count, err)
	}
	contentRole(t, f.db, f.staffID, "promos.view")
	path := "/api/v1/admin/promo-codes/customer-groups?q=Promo&limit=1"
	w := request(f.h, "GET", path, nil, f.staff, "")
	expect(t, w, 200)
	page := data(t, w)
	if page["total"] != float64(1) || len(page["items"].([]any)) != 1 || strings.Contains(w.Body.String(), "member_count") || strings.Contains(w.Body.String(), "discount_percent") {
		t.Fatal("options exposed unrelated customer metadata", w.Body.String())
	}
	row := page["items"].([]any)[0].(map[string]any)
	if len(row) != 2 || row["id"] != float64(a) || row["name"] != "Promo selectable A" {
		t.Fatal("unexpected option projection", row)
	}
	expect(t, request(f.h, "GET", "/api/v1/admin/customer-groups", nil, f.staff, ""), 403)
	expect(t, request(f.h, "GET", path+"&offset=5", nil, f.staff, ""), 200)
	expect(t, request(f.h, "GET", path, nil, f.customer, ""), 401)
	expect(t, request(f.h, "GET", path, nil, "", ""), 401)
	in := map[string]any{"code": "GROUP-STAFF", "discount_type": "percent", "value": 10, "applies_customer_group_ids": []int64{a}}
	expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", in, f.staff, "promo-group-staff"), 403)
	contentRole(t, f.db, f.staffID, "promos.view", "promos.manage")
	w = request(f.h, "POST", "/api/v1/admin/promo-codes", in, f.staff, "promo-group-staff")
	expect(t, w, 201)
	id := int64(data(t, w)["id"].(float64))
	contentRole(t, f.db, f.staffID, "promos.view")
	expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", in, f.staff, "promo-group-staff"), 403)
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/admin/promo-codes/%d", id), map[string]any{"expected_version": 1, "applies_customer_group_ids": []int64{}}, f.staff, "promo-group-revoked"), 403)
}

func TestPromoCustomerGroupsAuditRollback(t *testing.T) {
	f := newMutationOrderFixture(t)
	a := promoCustomerGroup(t, f, "Promo audited A")
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_promo_group_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('promo_code.created','promo_code.updated') THEN RAISE EXCEPTION 'injected promo group audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_promo_group_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_promo_group_audit()`); err != nil {
		t.Fatal(err)
	}
	in := map[string]any{"code": "GROUP-AUDIT", "discount_type": "percent", "value": 10, "applies_customer_group_ids": []int64{a}}
	expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", in, f.root, "promo-group-audit"), 500)
	var promos, mappings, receipts int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM promo_codes),(SELECT count(*) FROM promo_customer_groups),(SELECT count(*) FROM mutation_operations WHERE scope='admin:promo_code.create')`).Scan(&promos, &mappings, &receipts); err != nil || promos+mappings+receipts != 0 {
		t.Fatal("creation audit failure escaped its transaction", promos, mappings, receipts, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER reject_promo_group_audit"); err != nil {
		t.Fatal(err)
	}
	w := request(f.h, "POST", "/api/v1/admin/promo-codes", in, f.root, "promo-group-audit")
	expect(t, w, 201)
	id := int64(data(t, w)["id"].(float64))
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events ENABLE TRIGGER reject_promo_group_audit"); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/promo-codes/%d", id)
	expect(t, request(f.h, "PATCH", path, map[string]any{"expected_version": 1, "applies_customer_group_ids": []int64{}}, f.root, "promo-group-audit-patch"), 500)
	var version int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT version,(SELECT count(*) FROM promo_customer_groups WHERE promo_id=$1),(SELECT count(*) FROM mutation_operations WHERE scope=$2) FROM promo_codes WHERE id=$1`, id, fmt.Sprintf("admin:promo_code.update.%d", id)).Scan(&version, &mappings, &receipts); err != nil || version != 1 || mappings != 1 || receipts != 0 {
		t.Fatal("update audit failure changed version, mappings or receipt", version, mappings, receipts, err)
	}
}

func TestPromoCustomerGroupsLegacyCreateAndPatchReplayFingerprints(t *testing.T) {
	f := newMutationOrderFixture(t)
	in := map[string]any{"code": "LEGACY-GROUP-HASH", "discount_type": "percent", "value": 10}
	w := request(f.h, "POST", "/api/v1/admin/promo-codes", in, f.root, "legacy-promo-groups")
	expect(t, w, 201)
	id := int64(data(t, w)["id"].(float64))
	// Exact pre-extension JSON, including the original empty-array normalization.
	legacy := `{"code":"LEGACY-GROUP-HASH","discount_type":"percent","value":10,"cycles":[],"applies_product_ids":[],"requires_product_ids":[],"requires_existing":false,"starts_at":"","expires_at":"","max_uses":0,"one_time":false,"only_new_customer":false,"only_old_customer":false,"once_per_customer":false,"recurring":false,"recurring_limit":0,"upgrade_only":false,"notes":""}`
	sum := sha256.Sum256([]byte(legacy))
	var hash string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT request_hash FROM mutation_operations WHERE scope='admin:promo_code.create' AND idempotency_key='legacy-promo-groups'`).Scan(&hash); err != nil || hash != hex.EncodeToString(sum[:]) {
		t.Fatal("legacy POST fingerprint changed", hash, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE mutation_operations SET result=result-'applies_customer_group_ids' WHERE scope='admin:promo_code.create' AND idempotency_key='legacy-promo-groups'`); err != nil {
		t.Fatal(err)
	}
	in["applies_customer_group_ids"] = []int64{}
	w = request(f.h, "POST", "/api/v1/admin/promo-codes", in, f.root, "legacy-promo-groups")
	expect(t, w, 201)
	if data(t, w)["id"] != float64(id) || data(t, w)["applies_customer_group_ids"] != nil {
		t.Fatal("historical receipt response was replaced by current fields", w.Body.String())
	}
	patch := map[string]any{"expected_version": 1, "notes": "legacy note"}
	path := fmt.Sprintf("/api/v1/admin/promo-codes/%d", id)
	expect(t, request(f.h, "PATCH", path, patch, f.root, "legacy-promo-patch"), 200)
	legacy = `{"active":null,"starts_at":{"Present":false,"Value":null,"Invalid":false},"expires_at":{"Present":false,"Value":null,"Invalid":false},"max_uses":null,"notes":"legacy note","expected_version":1}`
	sum = sha256.Sum256([]byte(legacy))
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT request_hash FROM mutation_operations WHERE scope=$1 AND idempotency_key='legacy-promo-patch'`, fmt.Sprintf("admin:promo_code.update.%d", id)).Scan(&hash); err != nil || hash != hex.EncodeToString(sum[:]) {
		t.Fatal("legacy PATCH fingerprint changed", hash, err)
	}
	expect(t, request(f.h, "PATCH", path, patch, f.root, "legacy-promo-patch"), 200)
}
