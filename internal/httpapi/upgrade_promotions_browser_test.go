package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func TestUpgradePromotionsBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "upgrade-promo-admin", func(db *store.Store, h http.Handler) {
		f := setupConfigurationUpgradeFixture(t, db, h, false)
		productChangeTarget(t, f, false)
		user(t, db, h, "upgrade-promo-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['promos.view','services.view','invoices.view'] WHERE name='upgrade-promo-reader-role'"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var used, redeemed int
			var status string
			if err := db.Pool.QueryRow(ctx, `SELECT used_count,(SELECT count(*) FROM promo_upgrade_redemptions r WHERE r.promo_code_id=p.id),(SELECT status FROM service_configuration_changes WHERE upgrade_promo_code=p.code AND invoice_id IS NOT NULL LIMIT 1) FROM promo_codes p WHERE code='BROWSER-UP500'`).Scan(&used, &redeemed, &status); err != nil || used != 1 || redeemed != 1 || status != "queued" {
				t.Errorf("browser upgrade settlement: used=%d redeemed=%d status=%s err=%v", used, redeemed, status, err)
			}
		})
	})
}
