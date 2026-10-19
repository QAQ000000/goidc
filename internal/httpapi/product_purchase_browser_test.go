package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

// Connect a real browser to the production API in a disposable test schema.
// This fixture starts no workers and performs no external supplier/payment calls.
func TestProductPurchaseBrowserFixture(t *testing.T) {
	ready, _ := browserFixturePaths(t)
	adminBrowserFixture(t, "purchase-browser-admin", func(db *store.Store, h http.Handler) {
		buyer, _ := user(t, db, h, "purchase-browser-customer", "customer")
		_, root := user(t, db, h, "purchase-browser-seed", "super_admin")
		pid := product(t, db, 12)
		other := product(t, db, 0)
		if _, err := db.Pool.Exec(t.Context(), `UPDATE products SET name=CASE id WHEN $1 THEN '弹性云配置验收' ELSE '售罄固定套餐' END,description='按需选择 CPU、内存与磁盘，配置后加入购物车。' WHERE id=ANY($2)`, pid, []int64{pid, other}); err != nil {
			t.Fatal(err)
		}
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/pricing", pid), map[string]any{"billing_cycle": "quarterly", "price_cents": 3000, "setup_fee_cents": 50, "default": false}, root, ""), 200)
		option := func(label, value string, price, setup int64) store.ProductOptionInput {
			return store.ProductOptionInput{Label: label, Value: value, PriceDeltaCents: price, SetupDeltaCents: setup, Active: enabled()}
		}
		create := func(name, slug string, kind int64, required bool, choices []store.ProductOptionInput) int64 {
			in := store.ProductOptionGroupInput{Name: name, Slug: slug, OptionType: kind, Required: required, Active: enabled(), ExpectedRevision: configRevision(t, h, root, pid), Options: choices}
			if kind == 4 {
				in.QtyMinimum, in.QtyMaximum, in.QtyStage, in.Unit = 1, 10, 1, "GB"
			}
			response := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), in, root, "")
			expect(t, response, 201)
			return int64(data(t, response)["id"].(float64))
		}
		cpu := create("CPU", "cpu", 2, true, []store.ProductOptionInput{option("2 核", "two", 0, 0), option("4 核", "four", 300, 20)})
		memory := create("内存", "memory", 1, true, []store.ProductOptionInput{option("2 GB", "two", 0, 0), option("4 GB", "four", 200, 0)})
		disk := create("磁盘", "disk", 4, true, []store.ProductOptionInput{option("SSD", "ssd", 0, 0)})
		backup := create("备份", "backup", 3, false, []store.ProductOptionInput{option("每日备份", "daily", 100, 10)})
		create("地区", "region", 1, true, []store.ProductOptionInput{option("上海", "shanghai", 0, 0), option("广州", "guangzhou", 0, 0)})
		diskOption := configOption(t, h, root, pid, disk, "ssd")
		for _, cycle := range []string{"monthly", "quarterly"} {
			price, second, setup := int64(100), int64(80), int64(11)
			if cycle == "quarterly" {
				price, second, setup = 200, 150, 13
			}
			in := map[string]any{"billing_cycle": cycle, "expected_revision": configRevision(t, h, root, pid), "tiers": []map[string]int64{
				{"qty_minimum": 1, "qty_maximum": 3, "price_cents": price, "setup_fee_cents": 7},
				{"qty_minimum": 4, "qty_maximum": 10, "price_cents": second, "setup_fee_cents": setup},
			}}
			expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d/options/%d/price-tiers", pid, disk, diskOption), in, root, ""), 200)
		}
		expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/purchase-fields", pid), store.PurchaseFieldsInput{ExpectedRevision: *configRevision(t, h, root, pid), Fields: []store.PurchaseField{
			{Key: "hostname", Label: "主机名称", Type: "text", Required: true, Active: true, MaxLength: 40, Pattern: `[a-z][a-z0-9-]*`},
		}}, root, ""), 200)
		writeBrowserJSON(t, ready+".fixture.json", map[string]any{"customer_email": "purchase-browser-customer@example.test", "product_id": pid, "soldout_product_id": other, "cpu_option": configOption(t, h, root, pid, cpu, "four"), "memory_option": configOption(t, h, root, pid, memory, "four"), "backup_option": configOption(t, h, root, pid, backup, "daily"), "disk_option": diskOption, "expected_cart_total_cents": 9186, "expected_order_total_cents": 9486, "external_actions": false})
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var orders, units, total, quantity, staffOrders int64
			var cycle, hostname string
			err := db.Pool.QueryRow(ctx, `SELECT count(DISTINCT o.id),coalesce(sum(i.quantity),0),coalesce(sum(i.total_cents),0),coalesce(max((i.option_quantities->>$2)::bigint),0),coalesce(max(i.billing_cycle),''),coalesce(max((SELECT f->>'value' FROM jsonb_array_elements(i.purchase_fields) f WHERE f->>'key'='hostname')),''),(SELECT count(*) FROM orders x JOIN users u ON u.id=x.user_id WHERE u.role IN ('admin','super_admin')) FROM orders o JOIN order_items i ON i.order_id=o.id WHERE o.user_id=$1`, buyer, fmt.Sprint(diskOption)).Scan(&orders, &units, &total, &quantity, &cycle, &hostname, &staffOrders)
			if err != nil || orders != 1 || units != 2 || total != 9486 || quantity != 6 || cycle != "quarterly" || hostname != "purchase-web-01" || staffOrders != 0 {
				t.Errorf("browser purchase facts mismatch: orders=%d units=%d cents=%d disk=%d cycle=%s hostname=%s staff=%d err=%v", orders, units, total, quantity, cycle, hostname, staffOrders, err)
			}
		})
	})
}
