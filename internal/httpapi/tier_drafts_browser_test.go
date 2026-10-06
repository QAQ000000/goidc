package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestTierDraftBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "tier-draft-super", func(db *store.Store, h http.Handler) {
		_, admin := user(t, db, h, "tier-draft-fixture-admin", "super_admin")
		user(t, db, h, "tier-draft-customer", "customer")
		user(t, db, h, "tier-draft-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['products.view'] WHERE name='tier-draft-reader-role'"); err != nil {
			t.Fatal(err)
		}
		in := productInput("tier-draft-preview", nil)
		in.Name = "弹性存储服务器"
		pid := managedProductID(t, h, admin, in)
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/pricing", pid), map[string]any{"billing_cycle": "annually", "price_cents": 20000, "setup_fee_cents": 100, "active": true, "default": false, "expected_revision": configRevision(t, h, admin, pid)}, admin, ""), 200)
		group := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), store.ProductOptionGroupInput{Name: "存储容量", Slug: "storage", Required: true, Active: enabled(), OptionType: 4, QtyMinimum: 1, QtyMaximum: 6, Unit: "GB", Options: []store.ProductOptionInput{{Label: "SSD", Value: "ssd", Active: enabled()}}}, admin, "")
		expect(t, group, 201)
		gid := int64(data(t, group)["id"].(float64))
		oid := configOption(t, h, admin, pid, gid, "ssd")
		for cycle, tiers := range map[string][]store.OptionPriceTier{
			"monthly":  {{Minimum: 1, Maximum: 3, PriceCents: 100, SetupFeeCents: 7}, {Minimum: 4, Maximum: 6, PriceCents: 80, SetupFeeCents: 11}},
			"annually": {{Minimum: 1, Maximum: 6, PriceCents: 900, SetupFeeCents: 12}},
		} {
			path := fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d/options/%d/price-tiers", pid, gid, oid)
			expect(t, request(h, "PUT", path, store.OptionPriceTiersInput{BillingCycle: cycle, Tiers: tiers, ExpectedRevision: configRevision(t, h, admin, pid)}, admin, ""), 200)
		}
	})
}
