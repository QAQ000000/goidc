package httpapi

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestOptionTierChangesCommitWithMetadataAndRollbackTogether(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "tier-changes-admin", "super_admin")
	_, reader := user(t, db, h, "tier-changes-reader", "admin")
	_, customer := user(t, db, h, "tier-changes-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['products.view'] WHERE name='tier-changes-reader-role'"); err != nil {
		t.Fatal(err)
	}
	pid := managedProductID(t, h, admin, productInput("tier-changes", nil))
	group := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), store.ProductOptionGroupInput{Name: "storage", Slug: "storage", Required: true, Active: enabled(), OptionType: 4, QtyMinimum: 1, QtyMaximum: 10, Options: []store.ProductOptionInput{{Label: "SSD", Value: "ssd", Active: enabled()}}}, admin, "")
	expect(t, group, 201)
	gid := int64(data(t, group)["id"].(float64))
	oid := configOption(t, h, admin, pid, gid, "ssd")
	path := fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d/options/%d", pid, gid, oid)
	tier := func(minimum, maximum, price int64) store.OptionPriceTier {
		return store.OptionPriceTier{Minimum: minimum, Maximum: maximum, PriceCents: price, SetupFeeCents: 12}
	}
	expect(t, request(h, "PUT", path+"/price-tiers", store.OptionPriceTiersInput{BillingCycle: "daily", Tiers: []store.OptionPriceTier{tier(1, 10, 40)}, ExpectedRevision: configRevision(t, h, admin, pid)}, admin, ""), 200)
	in := store.ProductOptionInput{Label: "SSD NVMe", Value: "ssd", Active: enabled(), QtyMinimum: 1, QtyMaximum: 10, PriceTierChanges: map[string][]store.OptionPriceTier{"monthly": {tier(1, 3, 125), tier(5, 10, 80)}, "annually": {tier(1, 10, 950)}}, ExpectedRevision: configRevision(t, h, admin, pid)}
	beforeRevision := *in.ExpectedRevision
	expect(t, request(h, "PUT", path, in, admin, ""), 200)
	if revision := *configRevision(t, h, admin, pid); revision != beforeRevision+1 {
		t.Fatal("multiple cycles did not advance revision once", revision, beforeRevision)
	}
	var label string
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT label FROM product_options WHERE id=$1", oid).Scan(&label); err != nil || label != "SSD NVMe" {
		t.Fatal("metadata not committed", label, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM product_option_price_tiers WHERE option_id=$1", oid).Scan(&count); err != nil || count != 4 {
		t.Fatal("cycle changes lost unchanged daily tier", count, err)
	}
	snapshot := func() []byte {
		t.Helper()
		var state []byte
		err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object('option',(SELECT to_jsonb(o) FROM product_options o WHERE id=$1),'tiers',(SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY id),'[]'::jsonb) FROM product_option_price_tiers t WHERE option_id=$1),'revision',(SELECT revision FROM products WHERE id=$2),'audits',(SELECT count(*) FROM audit_events))`, oid, pid).Scan(&state)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	assertRejected := func(input store.ProductOptionInput, cookie string, status int) {
		t.Helper()
		before := snapshot()
		expect(t, request(h, "PUT", path, input, cookie, ""), status)
		if after := snapshot(); !bytes.Equal(before, after) {
			t.Fatal("failed save changed metadata, tiers, revision or audit", string(before), string(after))
		}
	}
	// A valid monthly draft must not survive an invalid annual draft.
	in.Label = "must roll back"
	in.ExpectedRevision = configRevision(t, h, admin, pid)
	in.PriceTierChanges = map[string][]store.OptionPriceTier{"monthly": {tier(1, 10, 200)}, "annually": {tier(1, 5, 900), tier(5, 10, 800)}}
	assertRejected(in, admin, 409)
	in.PriceTierChanges = map[string][]store.OptionPriceTier{"monthly": {tier(1, 10, 200)}}
	in.ExpectedRevision = &beforeRevision
	assertRejected(in, admin, 409)
	in.ExpectedRevision = configRevision(t, h, admin, pid)
	assertRejected(in, reader, 403)
	assertRejected(in, customer, 401)
	in.PriceTierChanges = map[string][]store.OptionPriceTier{"weekly": {tier(1, 10, 200)}}
	assertRejected(in, admin, 409)
	in.PriceTierChanges = map[string][]store.OptionPriceTier{"monthly": {tier(1, 10, -1)}}
	assertRejected(in, admin, 409)
	// Fail the audit after all option/tier writes to verify transactional rollback.
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_tier_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='product.option.updated' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_tier_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_tier_audit()`); err != nil {
		t.Fatal(err)
	}
	in.PriceTierChanges = map[string][]store.OptionPriceTier{"monthly": {tier(1, 10, 200)}, "annually": {tier(1, 10, 800)}}
	assertRejected(in, admin, 500)
	if _, err := db.Pool.Exec(t.Context(), "DROP TRIGGER reject_tier_audit ON audit_events"); err != nil {
		t.Fatal(err)
	}
	// Empty replaces only the explicitly named cycle; other cycles remain intact.
	in.Label = "SSD NVMe"
	in.PriceTierChanges = map[string][]store.OptionPriceTier{"monthly": {}}
	expect(t, request(h, "PUT", path, in, admin, ""), 200)
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM product_option_price_tiers WHERE option_id=$1 AND billing_cycle='monthly'", oid).Scan(&count); err != nil || count != 0 {
		t.Fatal("empty cycle was not cleared", count, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM product_option_price_tiers WHERE option_id=$1 AND billing_cycle IN ('daily','annually')", oid).Scan(&count); err != nil || count != 2 {
		t.Fatal("clear erased other cycles", count, err)
	}
	fixed := configGroup(t, h, admin, pid, "fixed", false, []store.ProductOptionInput{{Label: "fixed", Value: "fixed", Active: enabled()}})
	fixedOption := configOption(t, h, admin, pid, fixed, "fixed")
	in.ExpectedRevision = configRevision(t, h, admin, pid)
	w := request(h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d/options/%d", pid, fixed, fixedOption), in, admin, "")
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "invalid_option_pricing") {
		t.Fatal("fixed option accepted tiers", w.Body.String())
	}
}
