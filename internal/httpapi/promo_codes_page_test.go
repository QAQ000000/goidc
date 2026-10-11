package httpapi

import (
	"fmt"
	"strings"
	"testing"
)

func TestPromoCodesPageSearchPaginationOrderFilterAndPermissions(t *testing.T) {
	f := newMutationOrderFixture(t)
	create := func(input map[string]any) int64 {
		t.Helper()
		w := request(f.h, "POST", "/api/v1/admin/promo-codes", input, f.root, "")
		expect(t, w, 201)
		return int64(data(t, w)["id"].(float64))
	}
	orderID := create(map[string]any{"code": "ORDER10", "discount_type": "percent", "value": 10, "notes": "首购订单优惠"})
	create(map[string]any{"code": "ORDER20", "discount_type": "fixed", "value": 200, "notes": "订单目录搜索"})
	create(map[string]any{"code": "UPGRADE10", "discount_type": "percent", "value": 10, "upgrade_only": true})
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/admin/promo-codes/%d", orderID), map[string]any{"active": false}, f.root, ""), 200)

	base := "/api/v1/admin/promo-codes/page"
	all := request(f.h, "GET", base+"?order_eligible=true&limit=1&offset=0", nil, f.root, "")
	expect(t, all, 200)
	first := data(t, all)
	if first["total"] != float64(2) || first["limit"] != float64(1) || first["offset"] != float64(0) || len(first["items"].([]any)) != 1 {
		t.Fatalf("unexpected page metadata: %#v", first)
	}
	second := request(f.h, "GET", base+"?order_eligible=true&limit=1&offset=1", nil, f.root, "")
	expect(t, second, 200)
	if data(t, second)["total"] != float64(2) || len(data(t, second)["items"].([]any)) != 1 {
		t.Fatalf("pagination did not return a stable second row: %#v", data(t, second))
	}

	search := request(f.h, "GET", base+"?q=ORDER10&active=inactive&order_eligible=true", nil, f.root, "")
	expect(t, search, 200)
	searchData := data(t, search)
	items := searchData["items"].([]any)
	if searchData["total"] != float64(1) || len(items) != 1 || items[0].(map[string]any)["code"] != "ORDER10" {
		t.Fatalf("search/status filter did not match: %#v", searchData)
	}

	upgrade := request(f.h, "GET", base+"?q=UPGRADE10", nil, f.root, "")
	expect(t, upgrade, 200)
	upgradeData := data(t, upgrade)
	if got := upgradeData["total"]; got != float64(1) || upgradeData["items"].([]any)[0].(map[string]any)["upgrade_only"] != true {
		t.Fatalf("normal catalog should include upgrade-only entry, response=%#v", upgradeData)
	}
	excludedUpgrade := request(f.h, "GET", base+"?order_eligible=true&q=UPGRADE10", nil, f.root, "")
	expect(t, excludedUpgrade, 200)
	if got := data(t, excludedUpgrade)["total"]; got != float64(0) {
		t.Fatalf("order catalog included upgrade-only promo, total=%v", got)
	}
	for _, path := range []string{
		base + "?active=bad",
		base + "?order_eligible=maybe",
		base + "?q=" + strings.Repeat("x", 121),
	} {
		expect(t, request(f.h, "GET", path, nil, f.root, ""), 400)
	}

	contentRole(t, f.db, f.staffID, "orders.manage")
	expect(t, request(f.h, "GET", base, nil, f.staff, ""), 403)
	contentRole(t, f.db, f.staffID, "orders.manage", "promos.view")
	expect(t, request(f.h, "GET", base, nil, f.staff, ""), 200)
}
