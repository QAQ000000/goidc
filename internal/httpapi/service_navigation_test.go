package httpapi

import (
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
	"sync"
	"testing"
)

func serviceGroup(t *testing.T, h http.Handler, root, name string) int64 {
	t.Helper()
	w := request(h, "POST", "/api/v1/admin/service-groups", store.ServiceGroupInput{Name: name, Icon: "cloud"}, root, "")
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}
func serviceNav(t *testing.T, h http.Handler, cookie string, id int64) map[string]any {
	t.Helper()
	w := request(h, "GET", "/api/v1/service-navigation", nil, cookie, "")
	expect(t, w, 200)
	for _, g := range listData(t, w) {
		if int64(g["id"].(float64)) == id {
			return g
		}
	}
	t.Fatal("navigation group missing")
	return nil
}
func setNav(t *testing.T, h http.Handler, cookie string, id int64, visible bool) {
	t.Helper()
	g := serviceNav(t, h, cookie, id)
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/service-navigation/%d", id), map[string]any{"visible": visible, "expected_version": g["preference_version"]}, cookie, ""), 200)
}
func assignNav(t *testing.T, h http.Handler, root string, pid int64, gid *int64) {
	t.Helper()
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/service-group", pid), map[string]any{"group_id": gid, "expected_revision": configRevision(t, h, root, pid)}, root, ""), 200)
}
func navOrder(t *testing.T, h http.Handler, customer string, pid int64, key string) store.CheckoutInput {
	t.Helper()
	v, item := add(t, h, customer, pid, 1)
	in := store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}
	expect(t, request(h, "POST", "/api/v1/orders", in, customer, key), 201)
	return in
}

func TestServiceNavigationVisibilityPurchaseAndCurrentMembership(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "nav-root", "super_admin")
	firstID, first := user(t, db, h, "nav-first", "customer")
	_, second := user(t, db, h, "nav-second", "customer")
	g1 := serviceGroup(t, h, root, "云服务器")
	if serviceNav(t, h, first, g1)["visible"] != true {
		t.Fatal("initial groups not visible")
	}
	setNav(t, h, first, g1, false)
	if serviceNav(t, h, second, g1)["visible"] != true {
		t.Fatal("preferences leaked")
	}
	g2 := serviceGroup(t, h, root, "独立服务器")
	if serviceNav(t, h, first, g2)["visible"] != false {
		t.Fatal("new group auto shown")
	}
	pid := managedProductID(t, h, root, productInput("nav-cloud", nil))
	assignNav(t, h, root, pid, &g1)
	in := navOrder(t, h, first, pid, "nav-first-order")
	navOrder(t, h, second, pid, "nav-second-order")
	g := serviceNav(t, h, first, g1)
	if g["visible"] != true || g["service_count"] != float64(1) || g["active_count"] != float64(0) {
		t.Fatal("purchase visibility/count", g)
	}
	var sid int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE user_id=$1", firstID).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET status='active' WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	if serviceNav(t, h, first, g1)["active_count"] != float64(1) {
		t.Fatal("active count")
	}
	setNav(t, h, first, g1, false)
	expect(t, request(h, "POST", "/api/v1/orders", in, first, "nav-first-order"), 200)
	if serviceNav(t, h, first, g1)["visible"] != false {
		t.Fatal("replay revealed navigation")
	}
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/services/%d", sid), nil, first, ""), 200)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/services/%d", sid), nil, second, ""), 404)
	page := request(h, "GET", fmt.Sprintf("/api/v1/services/page?group_id=%d&status=active&q=nav-cloud&limit=1", g1), nil, first, "")
	expect(t, page, 200)
	if data(t, page)["total"] != float64(1) || len(data(t, page)["items"].([]any)) != 1 {
		t.Fatal("owned page count")
	}
	page = request(h, "GET", fmt.Sprintf("/api/v1/services/page?group_id=%d&offset=100", g1), nil, first, "")
	if data(t, page)["total"] != float64(1) || len(data(t, page)["items"].([]any)) != 0 {
		t.Fatal("empty page total")
	}
	navOrder(t, h, first, pid, "nav-new-purchase")
	if serviceNav(t, h, first, g1)["visible"] != true {
		t.Fatal("purchase did not restore hidden group")
	}
	assignNav(t, h, root, pid, &g2)
	if serviceNav(t, h, first, g1)["service_count"] != float64(0) || serviceNav(t, h, first, g2)["service_count"] != float64(2) {
		t.Fatal("current membership not followed")
	}
	if serviceNav(t, h, first, g2)["visible"] != false {
		t.Fatal("admin move revealed nav")
	}
	detail := request(h, "GET", fmt.Sprintf("/api/v1/services/%d", sid), nil, first, "")
	if data(t, detail)["service_group_id"] != float64(g2) {
		t.Fatal("detail membership")
	}
	before := *configRevision(t, h, root, pid)
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/service-groups/%d", g2), map[string]any{"expected_version": 1, "target_group_id": g2}, root, ""), 409)
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/service-groups/%d", g2), map[string]any{"expected_version": 1, "target_group_id": g1}, root, ""), 200)
	if *configRevision(t, h, root, pid) != before+1 || serviceNav(t, h, first, g1)["service_count"] != float64(2) {
		t.Fatal("atomic reassignment")
	}
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM service_navigation_preferences WHERE group_id=$1", g2).Scan(&count); err != nil || count != 0 {
		t.Fatal("deleted preference", err)
	}
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/service-groups/%d", g1), map[string]any{"expected_version": 1, "target_group_id": nil}, root, ""), 200)
	page = request(h, "GET", "/api/v1/services/page?group_id=0", nil, first, "")
	if data(t, page)["total"] != float64(2) {
		t.Fatal("ungrouped services lost")
	}
	for _, suffix := range []string{"group_id=-1", "group_id=bad", "status=unknown"} {
		expect(t, request(h, "GET", "/api/v1/services/page?"+suffix, nil, first, ""), 400)
	}
}

func TestServiceNavigationPermissionsSalesScopeAndConcurrency(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "nav-scope-root", "super_admin")
	readerID, reader := user(t, db, h, "nav-reader", "admin")
	staffID, staff := user(t, db, h, "nav-sales", "admin")
	c1ID, c1 := user(t, db, h, "nav-owned", "customer")
	_, c2 := user(t, db, h, "nav-other", "customer")
	contentRole(t, db, readerID, "products.view", "services.view")
	g := serviceGroup(t, h, root, "服务测试")
	pid := managedProductID(t, h, root, productInput("nav-scope", nil))
	assignNav(t, h, root, pid, &g)
	navOrder(t, h, c1, pid, "nav-scope-order-one")
	navOrder(t, h, c2, pid, "nav-scope-order-two")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true,customer_scope='owned',customer_ownerless=false WHERE id=$1;", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", c1ID, staffID); err != nil {
		t.Fatal(err)
	}
	page := request(h, "GET", fmt.Sprintf("/api/v1/admin/services/page?group_id=%d", g), nil, staff, "")
	expect(t, page, 200)
	if data(t, page)["total"] != float64(1) || len(data(t, page)["items"].([]any)) != 1 {
		t.Fatal("sales count leak")
	}
	if data(t, request(h, "GET", "/api/v1/admin/services/page", nil, root, ""))["total"] != float64(2) {
		t.Fatal("super scope")
	}
	path := fmt.Sprintf("/api/v1/admin/service-groups/%d", g)
	in := store.ServiceGroupInput{Name: "已修改", Icon: "server", SortOrder: 3, ExpectedVersion: 1}
	expect(t, request(h, "GET", "/api/v1/admin/service-groups", nil, reader, ""), 200)
	expect(t, request(h, "PUT", path, in, reader, ""), 403)
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/service-group", pid), map[string]any{"group_id": nil, "expected_revision": 2}, reader, ""), 403)
	expect(t, request(h, "POST", "/api/v1/admin/service-groups", in, c1, ""), 403)
	// Two same-version writers cannot both succeed.
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { codes <- request(h, "PUT", path, in, root, "").Code })
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for c := range codes {
		counts[c]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("group race", counts)
	}
	pref := serviceNav(t, h, c1, g)
	codes = make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			codes <- request(h, "PUT", fmt.Sprintf("/api/v1/service-navigation/%d", g), map[string]any{"visible": false, "expected_version": pref["preference_version"]}, c1, "").Code
		})
	}
	wg.Wait()
	close(codes)
	counts = map[int]int{}
	for c := range codes {
		counts[c]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("preference race", counts)
	}
	key := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "navigation-writer"}, staff, "")
	expect(t, key, 201)
	secret := data(t, key)["secret"].(string)
	keyInput := store.ServiceGroupInput{Name: "API 分组", Icon: "database"}
	expect(t, requestBearer(h, "POST", "/api/v1/admin/service-groups", keyInput, secret), 201)
	contentRole(t, db, staffID, "products.view", "services.view")
	expect(t, requestBearer(h, "POST", "/api/v1/admin/service-groups", keyInput, secret), 403)
	// Cached route permissions cannot replace the store's fresh authorization.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveServiceGroup(t.Context(), rootID, 0, store.ServiceGroupInput{Name: "blocked", Icon: "server"}); err == nil {
		t.Fatal("disabled actor wrote")
	}
}

func TestServiceNavigationAtomicAuditRollbackAndInitialization(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "nav-rollback-root", "super_admin")
	customerID, customer := user(t, db, h, "nav-empty", "customer")
	expect(t, request(h, "GET", "/api/v1/service-navigation", nil, customer, ""), 200)
	g := serviceGroup(t, h, root, "后增分组")
	if serviceNav(t, h, customer, g)["visible"] != false {
		t.Fatal("empty initialization forgotten")
	}
	pid := managedProductID(t, h, root, productInput("nav-rollback", nil))
	assignNav(t, h, root, pid, &g)
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION nav_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('service_group.deleted','service_group.updated','product.service_group.updated','service_navigation.updated') THEN RAISE EXCEPTION 'nav audit rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER nav_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION nav_reject_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/service-groups/%d", g), map[string]any{"expected_version": 1, "target_group_id": nil}, root, ""), 500)
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/service-groups/%d", g), store.ServiceGroupInput{Name: "rollback", Icon: "cloud", ExpectedVersion: 1}, root, ""), 500)
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/service-group", pid), map[string]any{"expected_revision": 2, "group_id": nil}, root, ""), 500)
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/service-navigation/%d", g), map[string]any{"expected_version": 1, "visible": true}, customer, ""), 500)
	var group, revision, prefs int64
	var name string
	if err := db.Pool.QueryRow(t.Context(), "SELECT p.service_group_id,p.revision,g.name,(SELECT count(*) FROM service_navigation_preferences WHERE user_id=$2 AND group_id=g.id AND NOT visible AND version=1) FROM products p JOIN service_navigation_groups g ON g.id=p.service_group_id WHERE p.id=$1", pid, customerID).Scan(&group, &revision, &name, &prefs); err != nil || group != g || revision != 2 || name != "后增分组" || prefs != 1 {
		t.Fatal("audit rollback", err)
	}
}

func TestServiceNavigationCheckoutDeleteRace(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "nav-race-root", "super_admin")
	_, customer := user(t, db, h, "nav-race-client", "customer")
	for i := range 5 {
		g := serviceGroup(t, h, root, fmt.Sprintf("并发分组 %d", i))
		pid := managedProductID(t, h, root, productInput(fmt.Sprintf("nav-race-%d", i), nil))
		assignNav(t, h, root, pid, &g)
		v, item := add(t, h, customer, pid, 1)
		start := make(chan struct{})
		codes := make(chan int, 2)
		var wg sync.WaitGroup
		wg.Go(func() {
			<-start
			codes <- request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, fmt.Sprintf("nav-race-order-%d", i)).Code
		})
		wg.Go(func() {
			<-start
			codes <- request(h, "DELETE", fmt.Sprintf("/api/v1/admin/service-groups/%d", g), map[string]any{"expected_version": 1, "target_group_id": nil}, root, "").Code
		})
		close(start)
		wg.Wait()
		close(codes)
		counts := map[int]int{}
		for c := range codes {
			counts[c]++
		}
		if counts[200] != 1 || (counts[201]+counts[409]) != 1 {
			t.Fatal("checkout delete race", counts)
		}
		var group *int64
		var prefs int
		if err := db.Pool.QueryRow(t.Context(), "SELECT service_group_id,(SELECT count(*) FROM service_navigation_preferences WHERE group_id=$2) FROM products WHERE id=$1", pid, g).Scan(&group, &prefs); err != nil || group != nil || prefs != 0 {
			t.Fatal("dangling membership after race", err)
		}
		// If checkout lost, remove its stale cart row before the next independent race.
		if counts[409] == 1 {
			current, _ := cart(t, h, customer)
			expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/cart/items/%d", item), map[string]any{"version": current}, customer, ""), 200)
		}
	}
}

func TestServiceNavigationBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "navigation-super", func(db *store.Store, h http.Handler) {
		reader, _ := user(t, db, h, "navigation-reader", "admin")
		contentRole(t, db, reader, "products.view", "services.view")
		_, root := user(t, db, h, "navigation-writer", "super_admin")
		_, customer := user(t, db, h, "navigation-client", "customer")
		_, other := user(t, db, h, "navigation-other", "customer")
		g := serviceGroup(t, h, root, "云服务器")
		serviceGroup(t, h, root, "独立服务器")
		pid := managedProductID(t, h, root, productInput("navigation-cloud", nil))
		assignNav(t, h, root, pid, &g)
		for i := range 22 {
			navOrder(t, h, customer, pid, fmt.Sprintf("navigation-browser-%d", i))
		}
		navOrder(t, h, other, pid, "navigation-browser-other")
	})
}
