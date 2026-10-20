package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/httpapi"
	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/codex/zjmfgo/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func navigationImportFixture() legacy.Snapshot {
	s := importFixture()
	s["service_navigation_groups"] = []legacy.Record{{"id": "71", "groupname": "云服务", "fa_icon": "fa fa-cloud", "order": "9"}, {"id": "8", "groupname": "其他服务", "fa_icon": "el-icon-menu", "order": "2"}}
	s["products"][0]["groupid"] = "71"
	s["users"] = append(s["users"], legacy.Record{"id": "12", "email": "fresh@example.test", "username": "未初始化客户", "status": "1", "currency": "7", "credit": "0.00"}, legacy.Record{"id": "13", "email": "orphan@example.test", "username": "失效分组客户", "status": "1", "currency": "7", "credit": "0.00"})
	s["service_navigation_preferences"] = []legacy.Record{{"id": "901", "uid": "10", "groupid": "71", "is_show": "0"}, {"id": "902", "uid": "11", "groupid": "8", "is_show": "1"}, {"id": "903", "uid": "13", "groupid": "999", "is_show": "1"}, {"id": "904", "uid": "999", "groupid": "71", "is_show": "1"}}
	return s
}
func importedID(t *testing.T, db *store.Store, entity string, old int64) int64 {
	t.Helper()
	var id int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT target_id FROM legacy_id_map WHERE source='integration' AND entity=$1 AND legacy_id=$2", entity, old).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func importedNavigation(t *testing.T, db *store.Store, user int64) map[int64]bool {
	t.Helper()
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err = store.CatalogReadLock(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err = store.InitializeServiceNavigation(t.Context(), tx, user); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(t.Context(), "SELECT group_id,visible FROM service_navigation_preferences WHERE user_id=$1", user)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		var visible bool
		if err = rows.Scan(&id, &visible); err != nil {
			t.Fatal(err)
		}
		out[id] = visible
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return out
}
func navigationAdmin(t *testing.T, db *store.Store) int64 {
	t.Helper()
	var id int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO administrators(username,email,password_hash,display_name,role) VALUES('nav-import-root','nav-import-root@example.test','unused','导航测试管理员','super_admin') RETURNING id").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestNavigationImportPreservesSourceRelationshipsAndReplay(t *testing.T) {
	db := importTestDB(t)
	s := navigationImportFixture()
	im := newImporter(s)
	if err := im.apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	if im.report.Mapped["service_navigation_groups"] != 2 || im.report.Mapped["service_navigation_preferences"] != 2 {
		t.Fatal("navigation map counts", im.report.Mapped)
	}
	group := importedID(t, db, "service_navigation_groups", 71)
	other := importedID(t, db, "service_navigation_groups", 8)
	product := importedID(t, db, "products", 167)
	user := importedID(t, db, "users", 10)
	fresh := importedID(t, db, "users", 12)
	orphan := importedID(t, db, "users", 13)
	if group == 71 || other == 8 {
		t.Fatal("fixture must exercise real ID translation")
	}
	var name, icon string
	var sort int
	var membership int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT g.name,g.icon,g.sort_order,p.service_group_id FROM service_navigation_groups g JOIN products p ON p.service_group_id=g.id WHERE p.id=$1", product).Scan(&name, &icon, &sort, &membership); err != nil || name != "云服务" || icon != "cloud" || sort != 9 || membership != group {
		t.Fatal("source grouping loss", err)
	}
	var init bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM service_navigation_state WHERE user_id=$1)", fresh).Scan(&init); err != nil || init {
		t.Fatal("uninitialized source customer changed", err)
	}
	// Missing groups for an initialized customer are hidden, even if their only old row was orphaned.
	for _, uid := range []int64{user, orphan} {
		prefs := importedNavigation(t, db, uid)
		if len(prefs) != 2 || prefs[group] || prefs[other] {
			t.Fatal("missing/default preference shown")
		}
	}
	admin := navigationAdmin(t, db)
	next, err := db.SaveServiceGroup(t.Context(), admin, 0, store.ServiceGroupInput{Name: "新服务", Icon: "database"})
	if err != nil {
		t.Fatal(err)
	}
	if importedNavigation(t, db, user)[next] || importedNavigation(t, db, orphan)[next] {
		t.Fatal("later group shown for initialized source user")
	}
	prefs := importedNavigation(t, db, fresh)
	if len(prefs) != 3 || !prefs[group] || !prefs[other] || !prefs[next] {
		t.Fatal("fresh customer defaults not preserved")
	}
	if _, err = db.SaveServiceGroup(t.Context(), admin, group, store.ServiceGroupInput{Name: "迁移后分组", Icon: "globe", SortOrder: 1, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err = db.AssignProductServiceGroup(t.Context(), admin, product, 1, &other); err != nil {
		t.Fatal(err)
	}
	if err = db.SetServiceNavigation(t.Context(), user, group, 1, true); err != nil {
		t.Fatal(err)
	}
	// Recover identity maps from verified targets without changing live names, membership or preferences.
	if _, err = db.Pool.Exec(t.Context(), "DELETE FROM legacy_id_map"); err != nil {
		t.Fatal(err)
	}
	replay := newImporter(s)
	if err = replay.apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	if replay.report.Replayed["service_navigation_groups"] != 2 || replay.report.Replayed["service_navigation_preferences"] != 2 {
		t.Fatal("missing replay counts")
	}
	var version int64
	var visible bool
	if err = db.Pool.QueryRow(t.Context(), "SELECT name,version FROM service_navigation_groups WHERE id=$1", group).Scan(&name, &version); err != nil || name != "迁移后分组" || version != 2 {
		t.Fatal("replay overwrote group")
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT service_group_id FROM products WHERE id=$1", product).Scan(&membership); err != nil || membership != other {
		t.Fatal("replay overwrote product group")
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT visible,version FROM service_navigation_preferences WHERE user_id=$1 AND group_id=$2", user, group).Scan(&visible, &version); err != nil || !visible || version != 2 {
		t.Fatal("replay overwrote preference")
	}
	var archiveCount, mapCount int
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM legacy_source_records WHERE entity='service_navigation_preferences'").Scan(&archiveCount); err != nil || archiveCount != 4 {
		t.Fatal("orphan source archive loss")
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM legacy_id_map WHERE entity='service_navigation_preferences'").Scan(&mapCount); err != nil || mapCount != 2 {
		t.Fatal("orphan preference incorrectly mapped")
	}
}

func TestNavigationImportRejectsInvalidOrChangedSource(t *testing.T) {
	invalid := map[string]func(legacy.Snapshot){
		"duplicate pair": func(s legacy.Snapshot) {
			s["service_navigation_preferences"] = append(s["service_navigation_preferences"], legacy.Record{"id": "905", "uid": "10", "groupid": "71", "is_show": "1"})
		},
		"bad flag":         func(s legacy.Snapshot) { s["service_navigation_preferences"][0]["is_show"] = "2" },
		"bad reference":    func(s legacy.Snapshot) { s["products"][0]["groupid"] = "unparseable" },
		"bad name":         func(s legacy.Snapshot) { s["service_navigation_groups"][0]["groupname"] = "\x00" },
		"bad sort":         func(s legacy.Snapshot) { s["service_navigation_groups"][0]["order"] = "-1" },
		"missing snapshot": func(s legacy.Snapshot) { delete(s, "service_navigation_preferences") },
	}
	for name, change := range invalid {
		t.Run(name, func(t *testing.T) {
			s := navigationImportFixture()
			change(s)
			if err := newImporter(s).validate(); err == nil {
				t.Fatal("invalid navigation accepted")
			}
		})
	}
	changed := map[string]func(legacy.Snapshot){
		"group name":         func(s legacy.Snapshot) { s["service_navigation_groups"][0]["groupname"] = "source edit" },
		"preference flag":    func(s legacy.Snapshot) { s["service_navigation_preferences"][0]["is_show"] = "1" },
		"removed group":      func(s legacy.Snapshot) { s["service_navigation_groups"] = s["service_navigation_groups"][1:] },
		"removed preference": func(s legacy.Snapshot) { s["service_navigation_preferences"] = s["service_navigation_preferences"][1:] },
		"added group": func(s legacy.Snapshot) {
			s["service_navigation_groups"] = append(s["service_navigation_groups"], legacy.Record{"id": "99", "groupname": "增加", "order": "0", "fa_icon": ""})
		},
		"product assignment": func(s legacy.Snapshot) { s["products"][0]["groupid"] = "8" },
	}
	for name, change := range changed {
		t.Run(name, func(t *testing.T) {
			db := importTestDB(t)
			s := navigationImportFixture()
			if err := newImporter(s).apply(t.Context(), db.Pool); err != nil {
				t.Fatal(err)
			}
			change(s)
			if err := newImporter(s).apply(t.Context(), db.Pool); err == nil || !strings.Contains(err.Error(), "source changed") {
				t.Fatal("source mutation accepted", err)
			}
		})
	}
}

func TestNavigationImportArchiveFallbackAndTransactionRollback(t *testing.T) {
	db := importTestDB(t)
	s := navigationImportFixture()
	s["products"][0]["groupid"] = "999"
	s["service_navigation_groups"][0]["groupname"] = strings.Repeat("长", 255)
	s["service_navigation_groups"][0]["fa_icon"] = "<script>untrusted-icon</script>"
	s["service_navigation_preferences"][0]["is_show"] = ""
	im := newImporter(s)
	if err := im.apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	var group *int64
	var name, icon, raw string
	if err := db.Pool.QueryRow(t.Context(), "SELECT service_group_id FROM products").Scan(&group); err != nil || group != nil {
		t.Fatal("unresolved product should stay ungrouped")
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT name,icon FROM service_navigation_groups WHERE legacy_id=71").Scan(&name, &icon); err != nil || name != strings.Repeat("长", 255) || icon != "box" {
		t.Fatal("icon/name normalization", err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT payload->>'fa_icon' FROM legacy_source_records WHERE entity='service_navigation_groups' AND legacy_id=71").Scan(&raw); err != nil || raw != s["service_navigation_groups"][0]["fa_icon"] {
		t.Fatal("raw icon not archived")
	}
	issues := map[string]int{}
	for _, issue := range im.report.Issues {
		issues[issue.Code]++
	}
	if issues["missing_service_navigation_group"] != 2 || issues["navigation_icon_fallback"] != 1 || issues["null_navigation_visibility_hidden"] != 1 {
		t.Fatal("navigation anomalies missing", issues)
	}
	failed := importTestDB(t)
	if _, err := failed.Pool.Exec(t.Context(), `CREATE FUNCTION nav_import_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.legacy_source<>'' THEN RAISE EXCEPTION 'navigation import failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER nav_import_fail BEFORE UPDATE ON service_navigation_preferences FOR EACH ROW EXECUTE FUNCTION nav_import_fail()`); err != nil {
		t.Fatal(err)
	}
	if err := newImporter(navigationImportFixture()).apply(t.Context(), failed.Pool); err == nil {
		t.Fatal("expected preference failure")
	}
	for _, table := range []string{"users", "products", "service_navigation_groups", "service_navigation_preferences", "service_navigation_state", "legacy_id_map", "legacy_source_records"} {
		var count int
		if err := failed.Pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial navigation import", table, err)
		}
	}
}

func TestNavigationImportRefusesDeletedOrCorruptTargets(t *testing.T) {
	for _, kind := range []string{"group", "preference", "initialization", "owner"} {
		t.Run(kind, func(t *testing.T) {
			db := importTestDB(t)
			s := navigationImportFixture()
			if err := newImporter(s).apply(t.Context(), db.Pool); err != nil {
				t.Fatal(err)
			}
			user := importedID(t, db, "users", 10)
			group := importedID(t, db, "service_navigation_groups", 71)
			var query string
			switch kind {
			case "group":
				query = "UPDATE products SET service_group_id=NULL; DELETE FROM service_navigation_groups WHERE legacy_id=71; DELETE FROM legacy_id_map WHERE entity='service_navigation_groups' AND legacy_id=71"
			case "preference":
				query = "DELETE FROM service_navigation_preferences WHERE legacy_id=901; DELETE FROM legacy_id_map WHERE entity='service_navigation_preferences' AND legacy_id=901"
			case "initialization":
				query = fmt.Sprintf("DELETE FROM service_navigation_state WHERE user_id=%d", user)
			case "owner":
				query = fmt.Sprintf("DELETE FROM service_navigation_preferences WHERE user_id=%d AND group_id<>%d; UPDATE service_navigation_preferences SET group_id=(SELECT id FROM service_navigation_groups WHERE legacy_id=8) WHERE legacy_id=901", user, group)
			}
			if _, err := db.Pool.Exec(t.Context(), query); err != nil {
				t.Fatal(err)
			}
			if err := newImporter(s).apply(t.Context(), db.Pool); err == nil {
				t.Fatal("missing/corrupt target silently revived")
			}
		})
	}
}

func TestImportedNavigationThroughCustomerAPI(t *testing.T) {
	db := importTestDB(t)
	s := navigationImportFixture()
	if err := newImporter(s).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	uid := importedID(t, db, "users", 10)
	group := importedID(t, db, "service_navigation_groups", 71)
	// A disposable test credential verifies the imported account's real ownership.
	hash, err := bcrypt.GenerateFromPassword([]byte("navigation-api-fixture"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE users SET password_hash=$2,password_reset_required=false WHERE id=$1", uid, string(hash)); err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(db, config.Config{PublicOrigin: "http://localhost:3088"}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	call := func(method, path string, body any, cookie string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://localhost:3088/api/v1"+path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost:3088")
		r.Header.Set("X-Requested-With", "zjmfgo")
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	login := call("POST", "/auth/login", map[string]string{"identifier": "import@example.test", "password": "navigation-api-fixture"}, "")
	if login.Code != 200 {
		t.Fatal("imported test login status", login.Code)
	}
	cookies := login.Result().Cookies()
	cookie := cookies[0].Name + "=" + cookies[0].Value
	nav := call("GET", "/service-navigation", nil, cookie)
	if nav.Code != 200 {
		t.Fatal("navigation API", nav.Code)
	}
	var response struct {
		Data []struct {
			ID      int64 `json:"id"`
			Visible bool  `json:"visible"`
			Count   int   `json:"service_count"`
			Active  int   `json:"active_count"`
		}
	}
	if err = json.Unmarshal(nav.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range response.Data {
		if g.ID == group {
			found = true
			if g.Visible || g.Count != 1 || g.Active != 0 {
				t.Fatal("imported hidden service/count changed")
			}
		}
	}
	if !found {
		t.Fatal("mapped group missing from API")
	}
	page := call("GET", fmt.Sprintf("/services/page?group_id=%d", group), nil, cookie)
	if page.Code != 200 {
		t.Fatal("hidden imported group access", page.Code)
	}
	var paged struct {
		Data struct {
			Total int `json:"total"`
			Items []struct {
				Group  int64  `json:"service_group_id"`
				Status string `json:"status"`
			}
		}
	}
	if err = json.Unmarshal(page.Body.Bytes(), &paged); err != nil || paged.Data.Total != 1 || len(paged.Data.Items) != 1 || paged.Data.Items[0].Group != group || paged.Data.Items[0].Status != "pending_payment" {
		t.Fatal("imported membership API", err)
	}
	toggle := call("PUT", fmt.Sprintf("/service-navigation/%d", group), map[string]any{"visible": true, "expected_version": 1}, cookie)
	if toggle.Code != 200 {
		t.Fatal("customer preference API", toggle.Code)
	}
	if err = newImporter(s).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	if !importedNavigation(t, db, uid)[group] {
		t.Fatal("import replay overwrote API preference")
	}
	// Navigation access never releases resource migration guards.
	_, err = db.EnqueueServiceAction(t.Context(), uid, importedID(t, db, "services", 40), "suspend", "nav-import-guard", false)
	assertImportReview(t, err)
}

func TestNavigationMigrationPreservesExistingPreferences(t *testing.T) {
	db := emptyImportTestDB(t)
	files, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if name >= "040_" {
			break
		}
		sql, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Pool.Exec(t.Context(), string(sql)); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(sql)
		if _, err = db.Pool.Exec(t.Context(), "INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)", name, hex.EncodeToString(sum[:])); err != nil {
			t.Fatal(err)
		}
	}
	// Write schema039 facts explicitly: current Store writers require later
	// account tables. Preferences belong to a customer, separately from the
	// administrator who will manage groups after the complete upgrade.
	admin := historicalImportAdministrator(t, db)
	var customer, group int64
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role)
 VALUES('nav-upgrade-customer','nav-upgrade-customer@example.test','unused','导航测试客户','customer') RETURNING id`).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO service_navigation_groups(name,icon,sort_order) VALUES('迁移前分组','server',0) RETURNING id`).Scan(&group); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), `INSERT INTO service_navigation_state(user_id) VALUES($1)`, customer); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), `INSERT INTO service_navigation_preferences(user_id,group_id,visible,version) VALUES($1,$2,false,2)`, customer, group); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err = db.Pool.QueryRow(t.Context(), "SELECT jsonb_agg(to_jsonb(p) ORDER BY user_id,group_id)::text FROM service_navigation_preferences p").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal("repeat upgrade", err)
	}
	if err = db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT jsonb_agg(to_jsonb(p)-'id'-'legacy_source'-'legacy_id' ORDER BY user_id,group_id)::text FROM service_navigation_preferences p").Scan(&after); err != nil || before != after {
		t.Fatal("schema upgrade changed preferences", err)
	}
	var id int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT id FROM service_navigation_preferences WHERE user_id=$1 AND group_id=$2", customer, group).Scan(&id); err != nil || id < 1 {
		t.Fatal("preexisting preference missing stable ID")
	}
	next, err := db.SaveServiceGroup(t.Context(), admin, 0, store.ServiceGroupInput{Name: strings.Repeat("长", 255), Icon: "cloud"})
	if err != nil {
		t.Fatal("legacy length unsupported", err)
	}
	if importedNavigation(t, db, customer)[next] {
		t.Fatal("upgrade changed initialization")
	}
	var second int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT id FROM service_navigation_preferences WHERE user_id=$1 AND group_id=$2", customer, next).Scan(&second); err != nil || id == second {
		t.Fatal("identity sequence collision")
	}
}
