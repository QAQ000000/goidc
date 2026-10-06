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

func TestRecurringPromoBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "recurring-browser-admin", func(db *store.Store, h http.Handler) {
		uid, customer := user(t, db, h, "recurring-browser-customer", "customer")
		_, root := user(t, db, h, "recurring-browser-root", "super_admin")
		user(t, db, h, "recurring-browser-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['promos.view','services.view','invoices.view'] WHERE name='recurring-browser-reader-role'"); err != nil {
			t.Fatal(err)
		}
		pid := product(t, db, 10)
		if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET name='循环优惠云主机' WHERE id=$1", pid); err != nil {
			t.Fatal(err)
		}
		add(t, h, customer, pid, 2)
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", uid), map[string]any{"amount_cents": 10000}, root, "browser-recurring-topup"), 201)
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
						t.Errorf("fixture job: %v", err)
						return
					}
					if job != nil {
						if err = db.ProcessProvisionJob(ctx, *job); err != nil && ctx.Err() == nil {
							t.Errorf("fixture delivery: %v", err)
							return
						}
					}
				}
			}
		}()
		t.Cleanup(func() {
			var count int
			if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE i.user_id=$1 AND i.status='paid' AND ii.item_type='renew' AND ii.renewal_base_cents=1234 AND ii.renewal_discount_cents=123 AND ii.amount_cents=1111 AND ii.renewal_promo_code='REPEAT10'`, uid).Scan(&count); err != nil || count != 1 {
				t.Errorf("browser did not complete discounted renewal: count=%d err=%v", count, err)
			}
		})
	})
}
