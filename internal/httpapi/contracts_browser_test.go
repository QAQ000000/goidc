package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// Opt-in real API fixture; never runs a worker or calls payment/supplier services.
func TestContractsBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "contract-super", func(db *store.Store, h http.Handler) {
		uid, _ := user(t, db, h, "contract-customer", "customer")
		reader, _ := user(t, db, h, "contract-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY['contracts.view','settings.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)`, reader); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `UPDATE contract_settings SET enabled=true`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET display_name='合同验收客户' WHERE id=$1`, uid); err != nil {
			t.Fatal(err)
		}
		pid := product(t, db, 20)
		var services []int64
		for n := 0; n < 3; n++ {
			sid, iid := billingSchedulerFixture(t, db, uid, pid, 8000+n, false)
			if _, err := db.Pool.Exec(t.Context(), `UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1`, iid); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Pool.Exec(t.Context(), `UPDATE orders SET status='paid' WHERE id=(SELECT order_id FROM services WHERE id=$1)`, sid); err != nil {
				t.Fatal(err)
			}
			services = append(services, sid)
		}
		manifest, err := json.Marshal(map[string]any{"customer_id": uid, "reader_id": reader, "service_ids": services})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(os.Getenv("ZJMFGO_BROWSER_READY_FILE")+".fixture.json", manifest, 0600); err != nil {
			t.Fatal(err)
		}
	})
}
