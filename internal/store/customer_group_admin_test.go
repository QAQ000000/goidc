package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func TestCustomerGroupAdminLocksBeforeGroupRows(t *testing.T) {
	db := isolatedTestStore(t)
	actor, _ := topUpFixture(t, db)
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete-%t", remove), func(t *testing.T) {
			out, replay, err := db.CreateAdminCustomerGroup(t.Context(), actor, fmt.Sprintf("group-lock-create-%t", remove), CustomerGroupCreateInput{Name: fmt.Sprintf("Lock group %t", remove), Color: "#123456"})
			if err != nil || replay {
				t.Fatal(out, replay, err)
			}
			id := out["id"].(int64)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			blocker, err := db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(ctx)
			var pid int32
			if err = blocker.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if err = GroupPricingWriteLock(ctx, blocker); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if remove {
					_, e := db.DeleteAdminCustomerGroup(ctx, actor, id, "group-lock-delete", CustomerGroupDeleteInput{ExpectedVersion: 1})
					done <- e
				} else {
					percent := 25
					_, e := db.UpdateAdminCustomerGroup(ctx, actor, id, "group-lock-update", CustomerGroupPatchInput{DiscountPercent: &percent, ExpectedVersion: 1})
					done <- e
				}
			}()
			purchasePricingWaitForBlocked(t, ctx, db, pid)
			// A writer blocked on the pricing lock must not already own this row.
			// Taking it NOWAIT also reproduces the other half of the old cycle.
			probe, err := db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var version int64
			err = probe.QueryRow(ctx, "SELECT version FROM customer_groups WHERE id=$1 FOR UPDATE NOWAIT", id).Scan(&version)
			probe.Rollback(ctx)
			if err != nil || version != 1 {
				t.Fatal("group writer locked a row before the global pricing lock", version, err)
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				if err != nil {
					t.Fatal("writer failed after lock release", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestCustomerGroupAdminPreservesLegacyReceiptsAndAuditRollback(t *testing.T) {
	db := isolatedTestStore(t)
	actor, _ := topUpFixture(t, db)
	in := CustomerGroupCreateInput{Name: "Legacy group", Color: "#123456", DiscountPercent: 10}
	out, replay, err := db.CreateAdminCustomerGroup(t.Context(), actor, "group-legacy-create", in)
	if err != nil || replay {
		t.Fatal(out, replay, err)
	}
	id := out["id"].(int64)
	verifyHash := func(scope, key, literal string) {
		t.Helper()
		sum := sha256.Sum256([]byte(literal))
		var hash string
		if err := db.Pool.QueryRow(t.Context(), "SELECT request_hash FROM mutation_operations WHERE scope=$1 AND idempotency_key=$2", scope, key).Scan(&hash); err != nil || hash != hex.EncodeToString(sum[:]) {
			t.Fatal("legacy group fingerprint changed", hash, literal, err)
		}
	}
	verifyHash("admin:customer_group.create", "group-legacy-create", `{"name":"Legacy group","color":"#123456","discount_percent":10,"suspend_delete_exempt":false,"separate_invoices":false}`)
	out, replay, err = db.CreateAdminCustomerGroup(t.Context(), actor, "group-legacy-create", in)
	if err != nil || !replay || out["replayed"] != true || out["id"] != float64(id) {
		t.Fatal("create replay semantics changed", out, replay, err)
	}
	percent := 20
	patch := CustomerGroupPatchInput{DiscountPercent: &percent, ExpectedVersion: 1}
	out, err = db.UpdateAdminCustomerGroup(t.Context(), actor, id, "group-legacy-update", patch)
	if err != nil {
		t.Fatal(err)
	}
	verifyHash(fmt.Sprintf("admin:customer_group.update:%d", id), "group-legacy-update", fmt.Sprintf(`{"ID":%d,"Input":{"name":null,"color":null,"discount_percent":20,"suspend_delete_exempt":null,"separate_invoices":null,"expected_version":1}}`, id))
	out, err = db.UpdateAdminCustomerGroup(t.Context(), actor, id, "group-legacy-update", patch)
	if err != nil || out["replayed"] != true {
		t.Fatal("update replay semantics changed", out, err)
	}
	if _, err = db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_group_admin_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='customer_group.updated' THEN RAISE EXCEPTION 'injected group audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_group_admin_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_group_admin_audit()`); err != nil {
		t.Fatal(err)
	}
	patch.ExpectedVersion = 2
	percent = 30
	if _, err = db.UpdateAdminCustomerGroup(t.Context(), actor, id, "group-audit-fail", patch); err == nil {
		t.Fatal("audit failure accepted")
	}
	var version, actual, receipts, audits int64
	if err = db.Pool.QueryRow(t.Context(), `SELECT version,discount_percent,(SELECT count(*) FROM mutation_operations WHERE idempotency_key='group-audit-fail'),(SELECT count(*) FROM audit_events WHERE action='customer_group.updated' AND entity_id=$2) FROM customer_groups WHERE id=$1`, id, fmt.Sprint(id)).Scan(&version, &actual, &receipts, &audits); err != nil || version != 2 || actual != 20 || receipts != 0 || audits != 1 {
		t.Fatal("failed group update escaped its transaction", version, actual, receipts, audits, err)
	}
	deleteIn := CustomerGroupDeleteInput{ExpectedVersion: 2}
	if out, err = db.DeleteAdminCustomerGroup(t.Context(), actor, id, "group-legacy-delete", deleteIn); err != nil {
		t.Fatal(out, err)
	}
	verifyHash(fmt.Sprintf("admin:customer_group.delete:%d", id), "group-legacy-delete", fmt.Sprintf(`{"ID":%d,"Input":{"expected_version":2}}`, id))
	if out, err = db.DeleteAdminCustomerGroup(t.Context(), actor, id, "group-legacy-delete", deleteIn); err != nil || out["replayed"] != true {
		t.Fatal("delete replay must precede the missing row check", out, err)
	}
}
