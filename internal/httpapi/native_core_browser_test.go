package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// This fixture runs only against the integration helper's disposable schema.
func TestNativeCoreBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" {
		t.Skip("opt-in browser fixture")
	}
	var fixture *store.Store
	adminBrowserFixture(t, "native-core-super", func(db *store.Store, h http.Handler) {
		fixture = db
		_, admin := user(t, db, h, "native-core-editor", "super_admin")
		reader, _ := user(t, db, h, "native-core-reader", "admin")
		contentRole(t, db, reader, "content.view", "products.view")
		user(t, db, h, "native-core-customer", "customer")
		a := contentCategoryID(t, h, admin, categoryInput("knowledge", "cloud-guide"))
		b := contentCategoryID(t, h, admin, categoryInput("knowledge", "purchase-guide"))
		tag := request(h, "POST", "/api/v1/admin/content-tags", map[string]any{"content_type": "knowledge", "name": "新手指南", "slug": "getting-started", "active": true}, admin, "")
		expect(t, tag, 201)
		tagID := int64(data(t, tag)["id"].(float64))
		for _, entry := range []struct{ slug, title, access string }{
			{"public-core-guide", "公开云主机指南", "public"},
			{"private-core-guide", "登录后的配置指南", "login"},
		} {
			w := request(h, "POST", "/api/v1/admin/content", map[string]any{
				"content_type": "knowledge", "title": entry.title, "slug": entry.slug,
				"body":    "云主机配置说明。文本安全示例：</script><script>window.seoLeak=true</script>",
				"excerpt": "了解云主机配置与购买流程", "status": "published", "access_level": entry.access,
				"category_id": a, "category_ids": []int64{a, b}, "tag_ids": []int64{tagID}, "sort_order": 5,
			}, admin, "")
			expect(t, w, 201)
		}
		in := productInput("native-core-cloud", nil)
		in.Name = "自选云主机"
		pid := managedProductID(t, h, admin, in)
		for _, group := range []store.ProductOptionGroupInput{
			{Name: "机房", Slug: "datacenter", OptionType: 1, Required: true, Active: enabled(), Options: []store.ProductOptionInput{{Label: "广州", Value: "guangzhou", Active: enabled()}, {Label: "杭州", Value: "hangzhou", Active: enabled()}}},
			{Name: "性能", Slug: "performance", OptionType: 2, Required: true, Active: enabled(), Options: []store.ProductOptionInput{{Label: "标准", Value: "standard", PriceDeltaCents: 300, Active: enabled()}, {Label: "高性能", Value: "premium", PriceDeltaCents: 600, Active: enabled()}}},
			{Name: "自动备份", Slug: "backup", OptionType: 3, Active: enabled(), Options: []store.ProductOptionInput{{Label: "启用自动备份", Value: "enabled", PriceDeltaCents: 450, Active: enabled()}}},
		} {
			group.ExpectedRevision = configRevision(t, h, admin, pid)
			expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), group, admin, ""), 201)
		}
	})
	var count int
	var total int64
	if err := fixture.Pool.QueryRow(t.Context(), "SELECT count(*),coalesce(sum(total_cents),0) FROM orders").Scan(&count, &total); err != nil || count != 1 || total != 2700 {
		t.Fatalf("browser checkout count=%d total=%d err=%v", count, total, err)
	}
	var snapshots int
	if err := fixture.Pool.QueryRow(t.Context(), "SELECT jsonb_array_length(config_snapshot) FROM order_items").Scan(&snapshots); err != nil || snapshots != 2 {
		t.Fatalf("browser snapshot options=%d err=%v", snapshots, err)
	}
}
