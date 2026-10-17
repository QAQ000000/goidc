package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

// Opt-in API only: no workers, external channels, or upstream resources.
func TestPromoRestorationBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "promo-accept-super", func(db *store.Store, h http.Handler) {
		_, root := user(t, db, h, "promo-fixture-seeder", "super_admin")
		reader, _ := user(t, db, h, "promo-accept-reader", "admin")
		contentRole(t, db, reader, "promos.view")
		now := time.Now().UTC().Truncate(time.Second)
		ids := map[string][]int64{}
		for i := 0; i < 35; i++ {
			state := []string{"active", "expired", "disabled", "scheduled", "exhausted"}[i%5]
			body := map[string]any{"code": fmt.Sprintf("ACCEPT-%s-%02d", state, i), "discount_type": "percent", "value": 10, "notes": "优惠码页面独立验收", "starts_at": now.Add(-24 * time.Hour).Format(time.RFC3339), "expires_at": now.Add(30 * 24 * time.Hour).Format(time.RFC3339)}
			if state == "expired" {
				body["expires_at"] = now.Add(-time.Hour).Format(time.RFC3339)
			}
			if state == "scheduled" {
				body["starts_at"] = now.Add(24 * time.Hour).Format(time.RFC3339)
			}
			if state == "exhausted" {
				body["max_uses"] = 1
			}
			w := request(h, "POST", "/api/v1/admin/promo-codes", body, root, fmt.Sprintf("fixture-promo-%d", i))
			expect(t, w, 201)
			id := int64(data(t, w)["id"].(float64))
			ids[state] = append(ids[state], id)
			if state == "disabled" {
				if _, err := db.Pool.Exec(t.Context(), "UPDATE promo_codes SET active=false WHERE id=$1", id); err != nil {
					t.Fatal(err)
				}
			}
			// A historical usage counter is deliberately fixture-only SQL. No real
			// order, payment, or external call is required for this table state.
			if state == "exhausted" {
				if _, err := db.Pool.Exec(t.Context(), "UPDATE promo_codes SET used_count=1 WHERE id=$1", id); err != nil {
					t.Fatal(err)
				}
			}
		}
		manifest, err := json.Marshal(map[string]any{"root_username": "promo-accept-super", "root_password": "integration-password", "reader_username": "promo-accept-reader", "reader_password": "integration-password", "reader_id": reader, "promo_ids": ids, "editable_id": ids["active"][0], "clear_dates_id": ids["active"][1], "origin": "http://127.0.0.1:3093"})
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
