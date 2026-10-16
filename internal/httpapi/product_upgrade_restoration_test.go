package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func TestProductUpgradeRestorationQuoteInvalidation(t *testing.T) {
	for _, scenario := range []string{"unpublished", "stock", "expired", "price"} {
		t.Run(scenario, func(t *testing.T) {
			f := configurationUpgradeFixture(t, false)
			target, option := productChangeTarget(t, f, false)
			quote := productChangeQuote(t, f, target, option)
			switch scenario {
			case "unpublished":
				_, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET active=false WHERE id=$1", target)
				if err != nil {
					t.Fatal(err)
				}
			case "stock":
				_, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET stock=0 WHERE id=$1", target)
				if err != nil {
					t.Fatal(err)
				}
			case "expired":
				_, err := f.db.Pool.Exec(t.Context(), "UPDATE service_configuration_changes SET expires_at=now()-interval '1 second' WHERE id=$1", int64(quote["id"].(float64)))
				if err != nil {
					t.Fatal(err)
				}
			case "price":
				_, err := f.db.Pool.Exec(t.Context(), "UPDATE product_prices SET price_cents=99999 WHERE product_id=$1", target)
				if err != nil {
					t.Fatal(err)
				}
			}
			w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/configuration-changes", f.sid), map[string]any{"quote_id": quote["id"]}, f.customer, "restoration-submit")
			if scenario != "price" {
				expect(t, w, 409)
				var product int64
				if err := f.db.Pool.QueryRow(t.Context(), "SELECT product_id FROM services WHERE id=$1", f.sid).Scan(&product); err != nil || product != f.pid {
					t.Fatal("rejected quote changed service", err)
				}
				return
			}
			expect(t, w, 201)
			invoice := int64(data(t, w)["invoice_id"].(float64))
			var amount int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT amount_cents FROM invoices WHERE id=$1", invoice).Scan(&amount); err != nil || amount != int64(quote["amount_cents"].(float64)) {
				t.Fatal("catalog price mutated frozen quote", amount, quote, err)
			}
		})
	}
}

func TestProductUpgradeRestorationPolicyEditors(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	target, _ := productChangeTarget(t, f, false)
	staffID, staff := user(t, f.db, f.h, "upgrade-restoration-manager", "admin")
	contentRole(t, f.db, staffID, "products.view", "products.manage")
	path := fmt.Sprintf("/api/v1/admin/products/%d/upgrade-targets", f.pid)
	body := store.ProductUpgradeTargetsInput{ExpectedRevision: *configRevision(t, f.h, f.root, f.pid), Targets: []store.ProductUpgradeTarget{{ProductID: target, AllowDowngrade: true}}}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes <- request(f.h, "PUT", path, body, staff, fmt.Sprintf("restoration-editor-%d", i)).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for c := range codes {
		counts[c]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("editors overwrote", counts)
	}
	body.ExpectedRevision = *configRevision(t, f.h, f.root, f.pid)
	body.Targets[0].AllowDowngrade = false
	expect(t, request(f.h, "PUT", path, body, staff, "restoration-replay"), 200)
	expect(t, request(f.h, "PUT", path, body, staff, "restoration-replay"), 200)
	changed := body
	changed.Targets = []store.ProductUpgradeTarget{{ProductID: target, AllowDowngrade: true}}
	expect(t, request(f.h, "PUT", path, changed, staff, "restoration-replay"), 409)
	contentRole(t, f.db, staffID, "products.view")
	expect(t, request(f.h, "PUT", path, body, staff, "restoration-replay"), 403)
}

// Local-only fixture. Paid upgrades remain pending until the browser explicitly
// invokes the existing manual delivery endpoint. Only isolated manual jobs run.
func TestProductUpgradeRestorationBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "upgrade-restoration-super", func(db *store.Store, h http.Handler) {
		f := setupConfigurationUpgradeFixture(t, db, h, true)
		target, option := productChangeTarget(t, f, true)
		manager, _ := user(t, db, h, "upgrade-restoration-manager", "admin")
		contentRole(t, db, manager, "products.view", "products.manage", "services.view", "services.manage", "invoices.view")
		reader, _ := user(t, db, h, "upgrade-restoration-reader", "admin")
		contentRole(t, db, reader, "products.view", "services.view", "invoices.view")
		services := []int64{f.sid}
		for i := 0; i < 3; i++ {
			v, item := configuredCart(t, h, f.customer, f.pid, []int64{f.small}, 1)
			w := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, f.customer, fmt.Sprintf("fixture-upgrade-order-%d", i))
			expect(t, w, 201)
			order := int64(data(t, w)["order_id"].(float64))
			expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(data(t, w)["invoice_id"].(float64))), map[string]any{}, f.customer, fmt.Sprintf("fixture-upgrade-pay-%d", i)), 201)
			job, err := db.ClaimProvisionJob(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = db.ProcessProvisionJob(t.Context(), *job); err != nil {
				t.Fatal(err)
			}
			var sid int64
			if err = db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1", order).Scan(&sid); err != nil {
				t.Fatal(err)
			}
			expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery", sid), store.ManualCompletion{JobID: job.ID, UpstreamID: fmt.Sprintf("fixture-local-%d", i), Note: "local fixture"}, f.root, ""), 200)
			services = append(services, sid)
		}
		// Unsupported supplier-mode catalog record carries no supplier account or
		// remote address. It cannot dispatch any external protocol.
		input := productInput("unsupported-upgrade-supplier", nil)
		input.Name = "供应商升级不支持示例"
		pid := managedProductID(t, h, f.root, input)
		if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET provision_mode='supplier' WHERE id=$1", pid); err != nil {
			t.Fatal(err)
		}

		// Only this isolated schema has jobs; manual mode never dispatches a remote
		// provider. Claim/process exposes the existing awaiting-manual state so the
		// browser can confirm delivery through the normal API.
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		t.Cleanup(func() { cancel(); <-done })
		go func() {
			defer close(done)
			tick := time.NewTicker(250 * time.Millisecond)
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
						t.Errorf("local fixture claim: %v", err)
						return
					}
					if err = db.ProcessProvisionJob(ctx, *job); err != nil && ctx.Err() == nil {
						t.Errorf("local fixture process: %v", err)
						return
					}
				}
			}
		}()
		manifest, err := json.Marshal(map[string]any{"customer_id": f.uid, "manager_id": manager, "reader_id": reader, "source_product_id": f.pid, "target_product_id": target, "target_option_id": option, "source_small_option_id": f.small, "source_large_option_id": f.large, "unsupported_product_id": pid, "service_ids": services})
		if err != nil {
			t.Fatal(err)
		}
		private := os.Getenv("ZJMFGO_BROWSER_READY_FILE") + ".fixture.json"
		if err = os.WriteFile(private, manifest, 0600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(private) })
	})
}
