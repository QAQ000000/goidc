package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestNativeHierarchyBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "native-super", func(db *store.Store, h http.Handler) {
		adminID, admin := user(t, db, h, "native-fixture-admin", "super_admin")
		user(t, db, h, "native-customer", "customer")
		user(t, db, h, "native-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['products.view'] WHERE name='native-reader-role'"); err != nil {
			t.Fatal(err)
		}
		in := productInput("native-preview", nil)
		in.Name = "原生多区域云服务器"
		pid := managedProductID(t, h, admin, in)
		zero := int64(0)
		for _, root := range []struct{ name, slug, a, b string }{{"区域", "region", "广州", "杭州"}, {"独立区域", "alternate", "北京", "上海"}} {
			_, err := db.SaveProductOptionGroup(t.Context(), adminID, pid, 0, store.ProductOptionGroupInput{Name: root.name, Slug: root.slug, Active: enabled(), OptionType: 20, ParentGroupID: &zero, Options: []store.ProductOptionInput{{Label: root.a, Value: root.a, Active: enabled()}, {Label: root.b, Value: root.b, Active: enabled()}}})
			if err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestNativeHierarchyCheckoutBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "native-checkout-super", func(db *store.Store, h http.Handler) {
		adminID, admin := user(t, db, h, "native-checkout-admin", "super_admin")
		user(t, db, h, "native-customer", "customer")
		in := productInput("native-preview", nil)
		in.Name = "原生多区域云服务器"
		pid := managedProductID(t, h, admin, in)
		zero := int64(0)
		root, err := db.SaveProductOptionGroup(t.Context(), adminID, pid, 0, store.ProductOptionGroupInput{Name: "独立区域", Slug: "region", Active: enabled(), OptionType: 20, ParentGroupID: &zero, Options: []store.ProductOptionInput{{Label: "北京", Value: "beijing", Active: enabled()}, {Label: "上海", Value: "shanghai", Active: enabled()}}})
		if err != nil {
			t.Fatal(err)
		}
		a, b := configOption(t, h, admin, pid, root, "beijing"), configOption(t, h, admin, pid, root, "shanghai")
		_, err = db.SaveProductOptionGroup(t.Context(), adminID, pid, 0, store.ProductOptionGroupInput{Name: "可用区", Slug: "zone", Required: true, Active: enabled(), OptionType: 20, ParentGroupID: &root, Options: []store.ProductOptionInput{{Label: "华北一区", Value: "north", Active: enabled(), PriceDeltaCents: 250, ParentOptionID: &a}, {Label: "华东一区", Value: "east", Active: enabled(), ParentOptionID: &b}}})
		if err != nil {
			t.Fatal(err)
		}
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/duplicate", pid), store.ProductDuplicateInput{Name: "原生多级商品副本", Slug: "native-preview-copy"}, admin, ""), 201)
	})
}
