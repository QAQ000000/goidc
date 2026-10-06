package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// Opt-in, isolated data only; the normal test suite never waits for a browser.
func TestNativeSecurityBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" {
		t.Skip("opt-in browser fixture")
	}
	adminBrowserFixture(t, "security-super", func(db *store.Store, h http.Handler) {
		_, admin := user(t, db, h, "security-editor", "super_admin")
		reader, _ := user(t, db, h, "security-reader", "admin")
		contentRole(t, db, reader, "products.view", "services.view")
		customer, cookie := user(t, db, h, "security-customer", "customer")
		user(t, db, h, "security-other", "customer")
		in := productInput("security-cloud", nil)
		in.Name = "规则验收云主机"
		pid := managedProductID(t, h, admin, in)
		for _, group := range []store.ProductOptionGroupInput{
			{Name: "性能", Slug: "performance", OptionType: 2, Required: true, Active: enabled(), Options: []store.ProductOptionInput{{Label: "标准", Value: "standard", Active: enabled()}, {Label: "高性能", Value: "premium", PriceDeltaCents: 600, Active: enabled()}}},
			{Name: "自动备份", Slug: "backup", OptionType: 3, Active: enabled(), Options: []store.ProductOptionInput{{Label: "启用自动备份", Value: "enabled", PriceDeltaCents: 450, Active: enabled()}}},
		} {
			group.ExpectedRevision = configRevision(t, h, admin, pid)
			expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), group, admin, ""), 201)
		}
		base := product(t, db, 10)
		sid := downloadService(t, db, h, customer, cookie, base, "security-existing-service")
		for i := 0; i < 28; i++ {
			if _, err := db.Pool.Exec(t.Context(), `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload,created_at)
VALUES($1,'service.auto_renew.updated','service',$2,jsonb_build_object('password','fixture-secret-never-public','supplier_url','https://private-vendor.invalid'),now()-($3::integer*interval '1 minute'))`, customer, fmt.Sprint(sid), i); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,status,idempotency_key,attempts,last_error,result,request_payload)
VALUES($1,'create','waiting_verification','security-browser-job',2,'fixture-secret-never-public','{"password":"fixture-secret-never-public"}','{"url":"https://private-vendor.invalid"}')`, sid); err != nil {
			t.Fatal(err)
		}
	})
}
