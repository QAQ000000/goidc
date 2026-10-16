package httpapi

import (
	"fmt"
	"testing"
)

func TestUpgradePolicyIdempotentWrites(t *testing.T) {
	f := newMutationOrderFixture(t)
	paths := []struct {
		path, action string
		body         map[string]any
	}{
		{fmt.Sprintf("/api/v1/admin/products/%d/upgrade-targets", f.productID), "product.upgrade_targets.updated", map[string]any{"targets": []any{}}},
		{fmt.Sprintf("/api/v1/admin/products/%d/configuration-upgrade-policy", f.productID), "product.configuration_upgrade_policy.updated", map[string]any{"enabled": true, "allow_downgrade": false}},
	}
	for i, entry := range paths {
		revision := *configRevision(t, f.h, f.root, f.productID)
		entry.body["expected_revision"] = revision
		key := fmt.Sprintf("upgrade-policy-idempotent-%d", i)
		expect(t, request(f.h, "PUT", entry.path, entry.body, f.root, key), 200)
		expect(t, request(f.h, "PUT", entry.path, entry.body, f.root, key), 200)
		if got := *configRevision(t, f.h, f.root, f.productID); got != revision+1 {
			t.Fatal("replay advanced revision")
		}
		changed := map[string]any{}
		for k, v := range entry.body {
			changed[k] = v
		}
		changed["expected_revision"] = revision + 1
		expect(t, request(f.h, "PUT", entry.path, changed, f.root, key), 409)
		var count int
		if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action=$1 AND entity_id=$2`, entry.action, fmt.Sprint(f.productID)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("audit count %d", count)
		}
	}
}
