package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func enabled() *bool { v := true; return &v }
func catalogGroupInput(name string, section *int64) store.CatalogGroupInput {
	return store.CatalogGroupInput{Name: name, Slug: name, Description: "目录介绍", Active: enabled(), SectionID: section}
}
func catalogGroupID(t *testing.T, h http.Handler, root, kind string, in store.CatalogGroupInput) int64 {
	t.Helper()
	w := request(h, "POST", "/api/v1/admin/catalog-groups/"+kind, in, root, "")
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}
func productInput(slug string, category *int64) store.ProductInput {
	return store.ProductInput{Name: "商品 " + slug, Slug: slug, Description: "商品介绍", CategoryID: category, Active: enabled(), BillingCycle: "monthly", PriceCents: 2000, SetupFeeCents: 100, ProvisionMode: "manual"}
}
func managedProductID(t *testing.T, h http.Handler, root string, in store.ProductInput) int64 {
	t.Helper()
	w := request(h, "POST", "/api/v1/admin/products", in, root, "")
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}

func TestCatalogGroupsAndVisibilityAcrossCommerce(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "catalog-root", "super_admin")
	_, customer := user(t, db, h, "catalog-client", "customer")
	section := catalogGroupInput("cloud", nil)
	sid := catalogGroupID(t, h, root, "section", section)
	category := catalogGroupInput("compute", &sid)
	category.Headline = "云计算"
	category.Tagline = "弹性部署"
	cid := catalogGroupID(t, h, root, "category", category)
	pid := managedProductID(t, h, root, productInput("cloud-instance", &cid))
	v, item := add(t, h, customer, pid, 1)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/catalog?section_id=%d&category_id=%d", sid, cid), nil, "", ""), 200)
	section.Active = new(bool)
	section.ExpectedVersion = 1
	sp := fmt.Sprintf("/api/v1/admin/catalog-groups/section/%d", sid)
	expect(t, request(h, "PUT", sp, section, root, ""), 200)
	hidden := request(h, "GET", "/api/v1/catalog/page", nil, "", "")
	expect(t, hidden, 200)
	if data(t, hidden)["total"] != float64(0) || len(data(t, hidden)["items"].([]any)) != 0 {
		t.Fatal("hidden products visible")
	}
	if len(listData(t, request(h, "GET", "/api/v1/categories", nil, "", ""))) != 0 {
		t.Fatal("hidden child directory visible")
	}
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/catalog/%d", pid), nil, "", ""), 404)
	cartData := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))["items"].([]any)
	if cartData[0].(map[string]any)["active"] != false {
		t.Fatal("cart did not mark hidden product")
	}
	expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": v, "product_id": pid, "quantity": 1}, customer, ""), 409)
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/cart/items/%d", item), map[string]any{"version": v, "quantity": 2}, customer, ""), 409)
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "hidden-catalog-order"), 409)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/products/%d", pid), nil, root, ""), 200)
	section.Active = enabled()
	section.ExpectedVersion = 2
	expect(t, request(h, "PUT", sp, section, root, ""), 200)
	cp := fmt.Sprintf("/api/v1/admin/catalog-groups/category/%d", cid)
	expect(t, request(h, "DELETE", sp, map[string]int{"expected_version": 3}, root, ""), 409)
	expect(t, request(h, "DELETE", cp, map[string]int{"expected_version": 1}, root, ""), 409)
	category.Active = new(bool)
	category.ExpectedVersion = 1
	expect(t, request(h, "PUT", cp, category, root, ""), 200)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/catalog/%d", pid), nil, "", ""), 404)
	category.Active = enabled()
	category.ExpectedVersion = 2
	expect(t, request(h, "PUT", cp, category, root, ""), 200)
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "visible-catalog-order"), 201)
	// Hiding a group after purchase does not restrict the existing service or successful idempotent replay.
	category.Active = new(bool)
	category.ExpectedVersion = 3
	expect(t, request(h, "PUT", cp, category, root, ""), 200)
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "visible-catalog-order"), 200)
	services := request(h, "GET", "/api/v1/services", nil, customer, "")
	expect(t, services, 200)
	if len(listData(t, services)) != 1 {
		t.Fatal("existing service disappeared")
	}
}

func TestCatalogManagementVersionsStockAndPrices(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "edit-product-root", "super_admin")
	_, customer := user(t, db, h, "stock-customer", "customer")
	stock := 5
	in := productInput("stock-product", nil)
	in.Stock = &stock
	pid := managedProductID(t, h, root, in)
	path := fmt.Sprintf("/api/v1/admin/products/%d", pid)
	v, item := add(t, h, customer, pid, 1)
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "stock-update-order"), 201)
	in.ExpectedRevision, in.ExpectedStock = 1, &stock
	in.Name = "Should not overwrite inventory"
	rejected := request(h, "PATCH", path, in, root, "")
	expect(t, rejected, 409)
	if errorData(t, rejected)["code"] != "stock_changed" {
		t.Fatal("stock conflict not explicit")
	}
	remaining := 4
	in.ExpectedStock, in.Stock = &remaining, &remaining
	in.PriceCents = 3000
	in.ProvisionMode = "simulation"
	expect(t, request(h, "PATCH", path, in, root, ""), 200)
	expect(t, request(h, "PATCH", path, in, root, ""), 409)
	detail := data(t, request(h, "GET", path, nil, root, ""))
	if detail["revision"] != float64(2) || detail["stock"] != float64(4) || detail["price_cents"] != float64(3000) {
		t.Fatal("product update mismatch")
	}
	var mode string
	var recurring int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT s.provision_mode,coalesce(s.recurring_price_cents,oi.price_cents) FROM services s JOIN order_items oi ON oi.id=s.order_item_id WHERE s.product_id=$1", pid).Scan(&mode, &recurring); err != nil || mode != "manual" || recurring != 2000 {
		t.Fatal("product edit changed purchased service snapshot", err)
	}
	pricePath := path + "/pricing"
	price := map[string]any{"billing_cycle": "monthly", "price_cents": 4000, "setup_fee_cents": 200, "default": false, "active": true, "expected_revision": 2}
	expect(t, request(h, "POST", pricePath, price, root, ""), 200)
	detail = data(t, request(h, "GET", path, nil, root, ""))
	if detail["price_cents"] != float64(4000) || detail["setup_fee_cents"] != float64(200) || detail["revision"] != float64(3) {
		t.Fatal("default pricing not synchronized")
	}
	expect(t, request(h, "POST", pricePath, price, root, ""), 409)
	price["expected_revision"] = 3
	price["active"] = false
	expect(t, request(h, "POST", pricePath, price, root, ""), 400)
	price["billing_cycle"], price["active"], price["default"] = "annually", true, true
	expect(t, request(h, "POST", pricePath, price, root, ""), 200)
	price["billing_cycle"], price["active"], price["default"], price["expected_revision"] = "monthly", false, false, 4
	expect(t, request(h, "POST", pricePath, price, root, ""), 200)
	detail = data(t, request(h, "GET", path, nil, root, ""))
	if detail["billing_cycle"] != "annually" || detail["revision"] != float64(5) {
		t.Fatal("default cycle switching failed")
	}
	in.ExpectedRevision = 5
	in.Active = new(bool)
	in.BillingCycle = "annually"
	expect(t, request(h, "PATCH", path, in, root, ""), 200)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/catalog/%d", pid), nil, "", ""), 404)
	expect(t, request(h, "GET", "/api/v1/services", nil, customer, ""), 200)
}

func TestCatalogFiltersPaginationPermissionsAndValidation(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "catalog-permission-root", "super_admin")
	rid, reader := user(t, db, h, "catalog-reader", "admin")
	wid, writer := user(t, db, h, "catalog-writer", "admin")
	_, customer := user(t, db, h, "catalog-customer", "customer")
	contentRole(t, db, rid, "products.view")
	contentRole(t, db, wid, "products.view", "products.manage")
	section := catalogGroupInput("group-z", nil)
	section.SortOrder = 5
	sid := catalogGroupID(t, h, writer, "section", section)
	cid := catalogGroupID(t, h, writer, "category", catalogGroupInput("category-z", &sid))
	for i := range 3 {
		in := productInput(fmt.Sprintf("filter-product-%d", i), &cid)
		in.SortOrder = 3 - i
		managedProductID(t, h, writer, in)
	}
	in := productInput("other-product", nil)
	managedProductID(t, h, root, in)
	w := request(h, "GET", fmt.Sprintf("/api/v1/catalog/page?section_id=%d&q=filter-product&limit=1&offset=1", sid), nil, "", "")
	expect(t, w, 200)
	d := data(t, w)
	if d["total"] != float64(3) || len(d["items"].([]any)) != 1 || d["items"].([]any)[0].(map[string]any)["slug"] != "filter-product-1" {
		t.Fatal("sorting/pagination mismatch", w.Body.String())
	}
	uncategorized := request(h, "GET", "/api/v1/catalog/page?category_id=0", nil, "", "")
	expect(t, uncategorized, 200)
	if data(t, uncategorized)["total"] != float64(1) {
		t.Fatal("unclassified filter")
	}
	expect(t, request(h, "GET", "/api/v1/admin/products/page", nil, reader, ""), 200)
	expect(t, request(h, "GET", "/api/v1/admin/catalog-groups/section", nil, reader, ""), 200)
	for _, cookie := range []string{reader, customer} {
		expected := 403
		if cookie == customer {
			expected = 401
		}
		expect(t, request(h, "POST", "/api/v1/admin/products", in, cookie, ""), expected)
		expect(t, request(h, "POST", "/api/v1/admin/catalog-groups/section", section, cookie, ""), expected)
	}
	key := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "product-editor"}, writer, "")
	expect(t, key, 201)
	secret := data(t, key)["secret"].(string)
	expect(t, requestBearer(h, "POST", "/api/v1/admin/products", productInput("key-product", nil), secret), 201)
	contentRole(t, db, wid, "products.view")
	expect(t, requestBearer(h, "POST", "/api/v1/admin/products", productInput("revoked-product", nil), secret), 403)
	for _, path := range []string{"/api/v1/catalog/page?section_id=0", "/api/v1/catalog/page?category_id=-1", "/api/v1/admin/catalog-groups/invalid", "/api/v1/admin/products/page?active=invalid"} {
		cookie := ""
		if strings.Contains(path, "/admin/") {
			cookie = root
		}
		expect(t, request(h, "GET", path, nil, cookie, ""), 400)
	}
	bad := catalogGroupInput("missing-parent", nil)
	expect(t, request(h, "POST", "/api/v1/admin/catalog-groups/category", bad, root, ""), 409)
	bad.SectionID = new(int64)
	*bad.SectionID = 999999
	expect(t, request(h, "POST", "/api/v1/admin/catalog-groups/category", bad, root, ""), 404)
	in = productInput("bad-product", nil)
	in.Name = "bad\x00name"
	expect(t, request(h, "POST", "/api/v1/admin/products", in, root, ""), 409)
	expect(t, request(h, "POST", "/api/v1/admin/catalog-groups/section", section, root, ""), 409)
}

func TestProductDuplicateCopiesCatalogStateWithoutBusinessHistory(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "duplicate-product-root", "super_admin")
	source := managedProductID(t, h, root, productInput("duplicate-source", nil))
	group := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", source), map[string]any{
		"name": "系统", "slug": "os", "required": true, "active": true, "sort_order": 1,
		"options":           []map[string]any{{"label": "Ubuntu", "value": "ubuntu", "price_delta_cents": 300, "setup_delta_cents": 20, "active": true, "sort_order": 1}},
		"expected_revision": 1,
	}, root, "")
	expect(t, group, 201)
	sourceDetail := data(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/products/%d", source), nil, root, ""))
	groups := sourceDetail["option_groups"].([]any)
	groupID := int64(groups[0].(map[string]any)["id"].(float64))
	optionID := int64(groups[0].(map[string]any)["options"].([]any)[0].(map[string]any)["id"].(float64))
	price := map[string]any{"billing_cycle": "quarterly", "price_cents": 2500, "setup_fee_cents": 100, "active": true}
	_, err := db.Pool.Exec(t.Context(), "INSERT INTO product_option_prices(option_id,billing_cycle,price_cents,setup_fee_cents) VALUES($1,$2,$3,$4)", optionID, price["billing_cycle"], price["price_cents"], price["setup_fee_cents"])
	if err != nil {
		t.Fatal("seed option price", err)
	}
	duplicate := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/duplicate", source), map[string]any{"name": "复制商品", "slug": "duplicate-copy"}, root, "")
	expect(t, duplicate, 201)
	copyID := int64(data(t, duplicate)["id"].(float64))
	copyDetail := data(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/products/%d", copyID), nil, root, ""))
	if copyDetail["name"] != "复制商品" || copyDetail["slug"] != "duplicate-copy" || copyDetail["revision"] != float64(1) {
		t.Fatal("duplicate basic fields mismatch", copyDetail)
	}
	copyGroups := copyDetail["option_groups"].([]any)
	if len(copyGroups) != 1 || copyGroups[0].(map[string]any)["name"] != "系统" {
		t.Fatal("duplicate configuration group missing")
	}
	copyOptions := copyGroups[0].(map[string]any)["options"].([]any)
	if len(copyOptions) != 1 || copyOptions[0].(map[string]any)["label"] != "Ubuntu" {
		t.Fatal("duplicate configuration option missing")
	}
	pricing := copyOptions[0].(map[string]any)["pricing"].([]any)
	if len(pricing) != 1 || pricing[0].(map[string]any)["billing_cycle"] != "quarterly" || pricing[0].(map[string]any)["price_cents"] != float64(2500) {
		t.Fatal("duplicate option price missing", pricing)
	}
	var copiedProduct, copiedGroups, copiedOptions int
	if err = db.Pool.QueryRow(t.Context(), `SELECT count(*), (SELECT count(*) FROM product_option_groups WHERE product_id=$1), (SELECT count(*) FROM product_options po JOIN product_option_groups pog ON pog.id=po.group_id WHERE pog.product_id=$1) FROM products WHERE id=$1`, copyID).Scan(&copiedProduct, &copiedGroups, &copiedOptions); err != nil {
		t.Fatal("duplicate counts", err)
	}
	if copiedProduct != 1 || copiedGroups != 1 || copiedOptions != 1 {
		t.Fatal("duplicate counts mismatch", copiedProduct, copiedGroups, copiedOptions)
	}
	var sourceLegacy string
	if err = db.Pool.QueryRow(t.Context(), "SELECT legacy_source FROM products WHERE id=$1", copyID).Scan(&sourceLegacy); err != nil || sourceLegacy != "" {
		t.Fatal("duplicate inherited legacy identity", sourceLegacy, err)
	}
	_ = groupID
}

func TestCatalogConcurrencyAuditRollbackAndMigration(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "catalog-race-root", "super_admin")
	section := catalogGroupInput("race-section", nil)
	sid := catalogGroupID(t, h, root, "section", section)
	path := fmt.Sprintf("/api/v1/admin/catalog-groups/section/%d", sid)
	section.ExpectedVersion = 1
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() { codes <- request(h, "PUT", path, section, root, "").Code })
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("group race", counts)
	}
	pid := managedProductID(t, h, root, productInput("rollback-product", nil))
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_catalog_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'catalog_group.%' OR NEW.action LIKE 'product.%' THEN RAISE EXCEPTION 'injected catalogue audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_catalog_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_catalog_audit();`); err != nil {
		t.Fatal(err)
	}
	section.ExpectedVersion = 2
	section.Active = new(bool)
	expect(t, request(h, "PUT", path, section, root, ""), 500)
	expect(t, request(h, "DELETE", path, map[string]int{"expected_version": 2}, root, ""), 500)
	expect(t, request(h, "POST", "/api/v1/admin/catalog-groups/category", catalogGroupInput("rollback-child", &sid), root, ""), 500)
	in := productInput("rollback-product", nil)
	in.ExpectedRevision = 1
	in.PriceCents = 999
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/products/%d", pid), in, root, ""), 500)
	d := data(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/products/%d", pid), nil, root, ""))
	if d["price_cents"] != float64(2000) || d["revision"] != float64(1) {
		t.Fatal("audit failure changed price")
	}
	var active bool
	var version int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT active,version FROM product_sections WHERE id=$1", sid).Scan(&active, &version); err != nil || !active || version != 2 {
		t.Fatal("audit failure changed group", err)
	}
	// Recreate the pre-037 layout only in this disposable schema, retaining a flat legacy category.
	if _, err := db.Pool.Exec(t.Context(), `DROP TRIGGER reject_catalog_audit ON audit_events; ALTER TABLE product_categories DROP COLUMN section_id,DROP COLUMN description,DROP COLUMN headline,DROP COLUMN tagline,DROP COLUMN version,DROP COLUMN created_at,DROP COLUMN updated_at; DROP TABLE product_sections; ALTER TABLE products DROP COLUMN sort_order; DROP FUNCTION catalog_category_visible(bigint); DELETE FROM schema_migrations WHERE version='037_catalog_management.sql'; INSERT INTO product_categories(name,slug,active) VALUES('原分类','flat-legacy',true); UPDATE products SET category_id=(SELECT id FROM product_categories WHERE slug='flat-legacy');`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	w := request(h, "GET", "/api/v1/catalog/page", nil, "", "")
	expect(t, w, 200)
	if data(t, w)["total"] != float64(1) {
		t.Fatal("migration hid legacy flat category")
	}
}

func TestCatalogProductRevisionRace(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "product-race-root", "super_admin")
	in := productInput("concurrent-product", nil)
	pid := managedProductID(t, h, root, in)
	in.ExpectedRevision = 1
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() { codes <- request(h, "PATCH", fmt.Sprintf("/api/v1/admin/products/%d", pid), in, root, "").Code })
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("product revision race", counts)
	}
}

func TestCatalogWaitingWriterRevocation(t *testing.T) {
	db, h := testServer(t)
	id, writer := user(t, db, h, "catalog-waiting-editor", "admin")
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108197)"); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		done <- request(h, "POST", "/api/v1/admin/products", productInput("revoked-waiting-product", nil), writer, "").Code
	}()
	for {
		var waiting bool
		if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query='SELECT pg_advisory_xact_lock(70108197)')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("editor never waited on catalogue lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE admin_roles SET permissions=ARRAY['products.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", id); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 403 {
			t.Fatal("revoked waiting editor accepted", code)
		}
	case <-ctx.Done():
		t.Fatal("editor failed to finish")
	}
	var count int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM products").Scan(&count); err != nil || count != 0 {
		t.Fatal("revoked edit persisted", err)
	}
}

func TestCatalogWaitingCheckoutRechecksVisibility(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "waiting-catalog-root", "super_admin")
	_, customer := user(t, db, h, "waiting-catalog-client", "customer")
	sid := catalogGroupID(t, h, root, "section", catalogGroupInput("waiting-section", nil))
	cid := catalogGroupID(t, h, root, "category", catalogGroupInput("waiting-category", &sid))
	pid := managedProductID(t, h, root, productInput("waiting-catalog-product", &cid))
	v, item := add(t, h, customer, pid, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108197)"); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		done <- request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "waiting-catalog-key").Code
	}()
	for {
		var waiting bool
		if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query='SELECT pg_advisory_xact_lock_shared(70108197)')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("checkout never reached catalog lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE product_sections SET active=false WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 409 {
			t.Fatal("waiting checkout accepted hidden group", code)
		}
	case <-ctx.Done():
		t.Fatal("checkout failed to finish")
	}
	var orders int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM orders").Scan(&orders); err != nil || orders != 0 {
		t.Fatal("hidden checkout persisted", err)
	}
}
