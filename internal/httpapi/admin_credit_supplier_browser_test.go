package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// Opt-in acceptance fixture: disposable PG data, no worker or upstream calls.
func TestAdminCreditSupplierBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "next-super", func(db *store.Store, h http.Handler) {
		uid, customer := user(t, db, h, "next-customer", "customer")
		_, root := user(t, db, h, "fixture-super", "super_admin")
		reader, _ := user(t, db, h, "next-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY['credit.view','orders.view','products.view','jobs.view','services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)`, reader); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `UPDATE credit_settings SET enabled=true; UPDATE customer_credit_accounts SET limit_cents=50000`); err != nil {
			t.Fatal(err)
		}
		pid := product(t, db, 10)
		v, item := add(t, h, customer, pid, 1)
		checkout := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "next-browser-checkout")
		expect(t, checkout, 201)
		order := data(t, checkout)
		manual := manualInvoiceCreate(t, h, root, manualInvoiceInput(uid), "next-browser-manual")
		var supplier, service, job int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO supplier_accounts(name,provider,base_url,username,credentials_encrypted) VALUES('隔离供应商','mofang_finance','http://127.0.0.1:9','fixture',''::bytea) RETURNING id`).Scan(&supplier); err != nil {
			t.Fatal(err)
		}
		original, renewal := billingSchedulerFixture(t, db, uid, pid, 9001, false)
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,provision_mode,unit_number,upstream_id,resource_info,delivery_snapshot) SELECT user_id,order_id,order_item_id,product_id,'供应商验收资源','supplier-browser',billing_cycle,status,'supplier',2,'123','{"dedicated_ip":"203.0.113.42","password":"must-not-leak"}',jsonb_build_object('supplier_id',$2::bigint) FROM services WHERE id=$1 RETURNING id`, original, supplier).Scan(&service); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,status,idempotency_key,result) VALUES($1,'create','succeeded','next-create-cost','{"cost_cents":120,"currency":"CNY","token":"must-not-leak"}')`, service); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,status,attempts,idempotency_key,last_error) VALUES($1,'sync','failed',1,'next-safe-sync','must-not-leak') RETURNING id`, service).Scan(&job); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) VALUES($1,'供应商服务本地续费夹具','renew',1234,$2)`, renewal, service); err != nil {
			t.Fatal(err)
		}
		var unknown int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,provision_mode,unit_number,upstream_id,delivery_snapshot) SELECT user_id,order_id,order_item_id,product_id,'待核验供应商资源','supplier-unknown-browser',billing_cycle,status,'supplier',3,'124',delivery_snapshot FROM services WHERE id=$1 RETURNING id`, service).Scan(&unknown); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,status,attempts,idempotency_key,external_started_at) VALUES($1,'reboot','waiting_verification',1,'next-unknown-reboot',now())`, unknown); err != nil {
			t.Fatal(err)
		}
		manifest, err := json.Marshal(map[string]any{"customer_id": uid, "order": order, "manual_invoice_id": manual, "supplier_id": supplier, "service_id": service, "renewal_invoice_id": renewal, "job_id": job})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(os.Getenv("ZJMFGO_BROWSER_READY_FILE")+".fixture.json", manifest, 0600); err != nil {
			t.Fatal(err)
		}
	})
}
