package httpapi

import (
	"encoding/json"
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
	var fixture *store.Store
	adminBrowserFixture(t, "security-super", func(db *store.Store, h http.Handler) {
		fixture = db
		_, admin := user(t, db, h, "security-editor", "super_admin")
		reader, _ := user(t, db, h, "security-reader", "admin")
		contentRole(t, db, reader, "products.view", "services.view", "settings.view")
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
VALUES($1,'create','waiting_verification','security-browser-job',2,'等待核验','{"password":"fixture-secret-never-public"}','{}')`, sid); err != nil {
			t.Fatal(err)
		}
		manifest, _ := json.Marshal(map[string]int64{"product_id": pid, "service_id": sid})
		if err := os.WriteFile(os.Getenv("ZJMFGO_BROWSER_READY_FILE")+".fixture.json", manifest, 0600); err != nil {
			t.Fatal(err)
		}
	})
	var orders, rules, consumed, captchaLogins int
	var total int64
	if err := fixture.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM orders),(SELECT sum(total_cents) FROM orders),(SELECT count(*) FROM native_configuration_rules WHERE enabled),(SELECT count(*) FROM auth_captcha_challenges WHERE consumed_at IS NOT NULL)`).Scan(&orders, &total, &rules, &consumed); err != nil || orders != 2 || total != 4483 || rules != 1 || consumed < 2 {
		t.Fatalf("acceptance incomplete orders=%d total=%d rules=%d captcha-consumed=%d err=%v", orders, total, rules, consumed, err)
	}
	if err := fixture.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='auth.login_succeeded' AND created_at > (SELECT min(consumed_at) FROM auth_captcha_challenges)`).Scan(&captchaLogins); err != nil || captchaLogins < 1 {
		t.Fatalf("no successful login after captcha consumption: %d, error: %v", captchaLogins, err)
	}
}
