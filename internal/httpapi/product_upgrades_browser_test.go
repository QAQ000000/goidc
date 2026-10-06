package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestProductUpgradeBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "product-upgrade-super", func(db *store.Store, h http.Handler) {
		f := setupConfigurationUpgradeFixture(t, db, h, true)
		productChangeTarget(t, f, true)
		user(t, db, h, "product-upgrade-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['products.view','services.view','invoices.view'] WHERE name='product-upgrade-reader-role'"); err != nil {
			t.Fatal(err)
		}
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
						t.Errorf("browser worker delivery: %v", err)
						return
					}
				}
			}
		}()
	})
}
