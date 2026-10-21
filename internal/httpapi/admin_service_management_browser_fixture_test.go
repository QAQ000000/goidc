package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// Browser acceptance starts from actually paid, reviewed and manually delivered
// services. Only the future deadline and retained renewal benefit are seeded.
func TestAdminServiceManagementBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "service-management-super", func(db *store.Store, h http.Handler) {
		staffID, staff := user(t, db, h, "service-management-admin", "admin")
		readerID, reader := user(t, db, h, "service-management-reader", "admin")
		customerID, customer := user(t, db, h, "service-management-customer", "customer")
		contentRole(t, db, readerID, "services.view")
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]any{"amount_cents": 100000}, staff, "service-management-browser-balance"), http.StatusCreated)

		createActiveService := func(name string, price int64, key string) (int64, int64) {
			t.Helper()
			pid := product(t, db, 3)
			if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET name=$2,price_cents=$3,provision_mode='manual',approval_required=true WHERE id=$1", pid, name, price); err != nil {
				t.Fatal(err)
			}
			version, item := add(t, h, customer, pid, 1)
			order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, customer, key+"-order")
			expect(t, order, http.StatusCreated)
			orderID := int64(data(t, order)["order_id"].(float64))
			invoiceID := int64(data(t, order)["invoice_id"].(float64))
			expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, key+"-pay"), http.StatusCreated)
			expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/review", orderID), map[string]any{"decision": "approved", "note": "隔离浏览器验收资料审核通过", "expected_version": orderVersion(t, db, orderID)}, staff, key+"-approve"), http.StatusCreated)

			job, err := db.ClaimProvisionJob(t.Context())
			if err != nil || job == nil || job.Action != "create" {
				t.Fatal("browser service-management create job missing", err)
			}
			var sid int64
			if err = db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1", orderID).Scan(&sid); err != nil || job.ServiceID != sid {
				t.Fatal("browser service-management job belongs to another service", err)
			}
			if err = db.ProcessProvisionJob(t.Context(), *job); err != nil {
				t.Fatal(err)
			}
			expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery", sid), store.ManualCompletion{JobID: job.ID, UpstreamID: key + "-resource", Note: "隔离验收已核对人工资源并完成交付"}, staff, ""), http.StatusOK)
			var status, jobStatus, reviewStatus, invoiceStatus string
			if err = db.Pool.QueryRow(t.Context(), `SELECT s.status,j.status,o.review_status,i.status
 FROM services s JOIN provision_jobs j ON j.service_id=s.id JOIN orders o ON o.id=s.order_id
 JOIN invoices i ON i.order_id=o.id WHERE s.id=$1 AND j.id=$2 AND i.id=$3`, sid, job.ID, invoiceID).Scan(&status, &jobStatus, &reviewStatus, &invoiceStatus); err != nil {
				t.Fatal(err)
			}
			if status != "active" || jobStatus != "succeeded" || reviewStatus != "approved" || invoiceStatus != "paid" {
				t.Fatalf("browser management fixture not actually active: service=%s job=%s review=%s invoice=%s", status, jobStatus, reviewStatus, invoiceStatus)
			}
			if _, err = db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=date_trunc('second',now())+interval '15 days 0.123456 seconds' WHERE id=$1", sid); err != nil {
				t.Fatal(err)
			}
			return sid, invoiceID
		}

		benefitID, benefitInvoice := createActiveService("浏览器验收·保留续费优惠", 2500, "service-management-browser-benefit")
		plainID, plainInvoice := createActiveService("浏览器验收·普通人工服务", 4500, "service-management-browser-plain")
		if _, err := db.Pool.Exec(t.Context(), `UPDATE services SET renewal_discount_percent=20,
 renewal_fixed_discount_cents=0,renewal_promo_code='MANAGEMENT-RETAINED',renewal_discount_limit=2,
 renewal_discounts_used=1,version=version+1 WHERE id=$1`, benefitID); err != nil {
			t.Fatal(err)
		}

		// Setup uses the helper's full role; browser actions use only the
		// permissions needed by this batch, alongside super and read-only users.
		contentRole(t, db, staffID, "services.view", "services.manage", "services.pricing", "services.term.correct", "services.term.compensate")
		path := fmt.Sprintf("/api/v1/admin/services/%d", benefitID)
		expect(t, request(h, "GET", path+"/management", nil, reader, ""), http.StatusOK)
		var version int64
		if err := db.Pool.QueryRow(t.Context(), "SELECT version FROM services WHERE id=$1", benefitID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		expect(t, request(h, "POST", path+"/renewal-pricing/preview", map[string]any{"expected_version": version, "mode": "fixed", "fixed_amount_cents": 1800}, reader, ""), http.StatusForbidden)
		expect(t, request(h, "POST", path+"/term-adjustment-quotes", map[string]any{"expected_version": version, "kind": "compensation", "next_due_at": "2099-01-01T00:00:00Z", "reason": "只读管理员不得调整期限"}, reader, ""), http.StatusForbidden)
		expect(t, request(h, "PATCH", path+"/auto-renew", map[string]any{"enabled": true}, reader, ""), http.StatusForbidden)
		t.Logf("service management browser fixture: customer=%d benefit_service=%d benefit_invoice=%d plain_service=%d plain_invoice=%d", customerID, benefitID, benefitInvoice, plainID, plainInvoice)
	})
}
