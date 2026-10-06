package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestConfigurationUpgradeBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "upgrade-super", func(db *store.Store, h http.Handler) {
		uid, customer := user(t, db, h, "upgrade-browser-customer", "customer")
		_, root := user(t, db, h, "upgrade-fixture-admin", "super_admin")
		user(t, db, h, "upgrade-browser-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['products.view','services.view','invoices.view'] WHERE name='upgrade-browser-reader-role'"); err != nil {
			t.Fatal(err)
		}
		product := productInput("upgrade-browser-storage", nil)
		product.Name = "弹性存储服务器"
		pid := managedProductID(t, h, root, product)
		group := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), store.ProductOptionGroupInput{Name: "存储容量", Slug: "storage", OptionType: 4, QtyMinimum: 1, QtyMaximum: 6, Required: true, Active: enabled(), Options: []store.ProductOptionInput{{Label: "SSD NVMe", Value: "ssd", Active: enabled()}}}, root, "")
		expect(t, group, 201)
		gid := int64(data(t, group)["id"].(float64))
		oid := configOption(t, h, root, pid, gid, "ssd")
		expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d/options/%d/price-tiers", pid, gid, oid), store.OptionPriceTiersInput{BillingCycle: "monthly", ExpectedRevision: configRevision(t, h, root, pid), Tiers: []store.OptionPriceTier{{Minimum: 1, Maximum: 3, PriceCents: 125, SetupFeeCents: 7}, {Minimum: 4, Maximum: 6, PriceCents: 80, SetupFeeCents: 11}}}, root, ""), 200)
		cart := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
		expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "option_ids": []int64{oid}, "option_quantities": map[int64]int64{oid: 2}}, customer, ""), 200)
		cart = data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
		item := int64(cart["items"].([]any)[0].(map[string]any)["id"].(float64))
		order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(cart["version"].(float64)), ItemIDs: []int64{item}}, customer, "browser-upgrade-order")
		expect(t, order, 201)
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", uid), map[string]int64{"amount_cents": 100000}, root, "browser-upgrade-topup"), 201)
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(data(t, order)["invoice_id"].(float64))), map[string]any{}, customer, "browser-upgrade-pay"), 201)
		job, err := db.ClaimProvisionJob(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err = db.ProcessProvisionJob(t.Context(), *job); err != nil {
			t.Fatal(err)
		}
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery", job.ServiceID), store.ManualCompletion{JobID: job.ID, UpstreamID: "storage-resource-verified", Note: "initial storage resource confirmed"}, root, ""), 200)
		if _, err = db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=date_trunc('second',now())+interval '15 days' WHERE id=$1", job.ServiceID); err != nil {
			t.Fatal(err)
		}
		expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/configuration-upgrade-policy", pid), store.ConfigurationUpgradePolicy{Enabled: true, AllowDowngrade: true, ExpectedRevision: *configRevision(t, h, root, pid)}, root, ""), 200)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		t.Cleanup(func() { cancel(); <-done })
		go func() {
			defer close(done)
			tick := time.NewTicker(time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					job, err := db.ClaimProvisionJob(ctx)
					if errors.Is(err, pgx.ErrNoRows) || ctx.Err() != nil {
						continue
					}
					if err != nil {
						t.Errorf("browser worker claim: %v", err)
						return
					}
					if err = db.ProcessProvisionJob(ctx, *job); err != nil && ctx.Err() == nil {
						t.Errorf("browser worker process: %v", err)
						return
					}
				}
			}
		}()
	})
}
