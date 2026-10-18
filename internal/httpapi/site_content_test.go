package httpapi

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestSiteContentVersionedCRUDAndPermissions(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "site-content-root", "super_admin")
	_, customer := user(t, db, h, "site-content-customer", "customer")
	for _, path := range []string{"/api/v1/admin/theme-settings", "/api/v1/admin/site-fields", "/api/v1/admin/navigation", "/api/v1/admin/friendly-links", "/api/v1/admin/file-groups"} {
		expect(t, request(h, "GET", path, nil, customer, ""), 403)
	}
	publicResponse := request(h, "GET", "/api/v1/site", nil, "", "")
	expect(t, publicResponse, 200)
	publicBefore := data(t, publicResponse)
	if !reflect.DeepEqual(publicBefore["theme"], map[string]any{"template": "default", "primary_color": "#165DFF", "logo_variant": "full", "footer_text": ""}) {
		t.Fatal("public theme defaults", publicBefore)
	}
	settings, err := db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	expectedProfile, err := json.Marshal(settings.SiteProfile)
	if err != nil {
		t.Fatal(err)
	}
	var expectedPublic map[string]any
	if err = json.Unmarshal(expectedProfile, &expectedPublic); err != nil {
		t.Fatal(err)
	}
	expectedPublic["theme"] = publicBefore["theme"]
	if !reflect.DeepEqual(publicBefore, expectedPublic) {
		t.Fatal("public site fields changed or private settings leaked", publicBefore)
	}
	theme := request(h, "GET", "/api/v1/admin/theme-settings", nil, root, "")
	expect(t, theme, 200)
	if data(t, theme)["version"] != float64(1) {
		t.Fatal("theme version")
	}
	themeBody := map[string]any{"template": "classic", "primary_color": "#112233", "logo_variant": "compact", "footer_text": "footer", "expected_version": 1}
	saved := request(h, "PUT", "/api/v1/admin/theme-settings", themeBody, root, "theme-save-1")
	expect(t, saved, 200)
	if data(t, saved)["version"] != float64(2) {
		t.Fatal("theme save")
	}
	publicResponse = request(h, "GET", "/api/v1/site", nil, "", "")
	expect(t, publicResponse, 200)
	expectedPublic["theme"] = map[string]any{"template": "classic", "primary_color": "#112233", "logo_variant": "compact", "footer_text": "footer"}
	if !reflect.DeepEqual(data(t, publicResponse), expectedPublic) {
		t.Fatal("saved theme not reflected in anonymous public site", publicResponse.Body.String())
	}
	expect(t, request(h, "PUT", "/api/v1/admin/theme-settings", themeBody, root, "theme-save-1"), 200)
	expect(t, request(h, "PUT", "/api/v1/admin/theme-settings", themeBody, root, "theme-save-stale"), 409)
	field := request(h, "POST", "/api/v1/admin/site-fields", map[string]any{"field_key": "hero_title", "label": "首页标题", "field_type": "text", "active": true}, root, "field-create")
	expect(t, field, 200)
	id := int64(data(t, field)["id"].(float64))
	list := request(h, "GET", "/api/v1/admin/site-fields?limit=10", nil, root, "")
	expect(t, list, 200)
	if len(data(t, list)["items"].([]any)) != 1 {
		t.Fatal("field page")
	}
	version := int64(data(t, field)["version"].(float64))
	expect(t, request(h, "PATCH", "/api/v1/admin/site-fields/"+strconv.FormatInt(id, 10), map[string]any{"field_key": "hero_title", "label": "首页主标题", "field_type": "text", "active": true, "expected_version": version}, root, "field-update"), 200)
	nav := request(h, "POST", "/api/v1/admin/navigation", map[string]any{"label": "首页", "href": "/", "active": true}, root, "nav-create")
	expect(t, nav, 200)
	link := request(h, "POST", "/api/v1/admin/friendly-links", map[string]any{"name": "伙伴", "url": "https://example.com", "active": true}, root, "link-create")
	expect(t, link, 200)
	group := request(h, "GET", "/api/v1/admin/file-groups?limit=10", nil, root, "")
	expect(t, group, 200)
	if _, ok := data(t, group)["items"]; !ok {
		t.Fatal("file group page")
	}
}

func TestSiteThemeValidationPermissionsAndReplay(t *testing.T) {
	db, h := testServer(t)
	writerID, root := user(t, db, h, "theme-write-admin", "admin")
	viewerID, viewer := user(t, db, h, "theme-write-viewer", "admin")
	_, customer := user(t, db, h, "theme-write-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY['settings.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)`, viewerID); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/theme-settings"
	valid := map[string]any{"template": "minimal", "primary_color": "#ABCDEF", "logo_variant": "text", "footer_text": "公开页脚", "expected_version": 1}
	expect(t, request(h, "GET", path, nil, "", ""), 401)
	expect(t, request(h, "PUT", path, valid, "", "anonymous-theme"), 401)
	expect(t, request(h, "GET", path, nil, viewer, ""), 200)
	for _, cookie := range []string{viewer, customer} {
		expect(t, request(h, "PUT", path, valid, cookie, "forbidden-theme"), 403)
	}
	expect(t, request(h, "PUT", path, valid, root, ""), 400)
	for _, fieldValue := range []struct{ field, value string }{
		{"template", "external-plugin"}, {"template", "https://example.test/theme.js"}, {"primary_color", "red"}, {"primary_color", "#123"}, {"primary_color", "#123456;background:url(https://example.test)"}, {"primary_color", "var(--injected)"}, {"logo_variant", "external"},
	} {
		input := map[string]any{}
		for key, value := range valid {
			input[key] = value
		}
		input[fieldValue.field] = fieldValue.value
		expect(t, request(h, "PUT", path, input, root, "invalid-theme"), 400)
	}
	saved := request(h, "PUT", path, valid, root, "theme-minimal-save")
	expect(t, saved, 200)
	if data(t, saved)["version"] != float64(2) {
		t.Fatal("theme version did not advance")
	}
	replayed := request(h, "PUT", path, valid, root, "theme-minimal-save")
	expect(t, replayed, 200)
	replayedData := data(t, replayed)
	if replayedData["replayed"] != true {
		t.Fatal("theme replay was not identified")
	}
	delete(replayedData, "replayed")
	if !reflect.DeepEqual(replayedData, data(t, saved)) {
		t.Fatal("theme replay changed response")
	}
	changed := map[string]any{"template": "classic", "primary_color": "#010203", "logo_variant": "compact", "footer_text": "conflict", "expected_version": 1}
	expect(t, request(h, "PUT", path, changed, root, "theme-minimal-save"), 409)
	expect(t, request(h, "PUT", path, changed, root, "theme-stale-save"), 409)
	public := request(h, "GET", "/api/v1/site", nil, "", "")
	expect(t, public, 200)
	if !reflect.DeepEqual(data(t, public)["theme"], map[string]any{"template": "minimal", "primary_color": "#ABCDEF", "logo_variant": "text", "footer_text": "公开页脚"}) {
		t.Fatal("failed mutation changed public theme", public.Body.String())
	}
	var writes, operations int
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM audit_events WHERE action='theme.settings_updated'),(SELECT count(*) FROM mutation_operations WHERE scope='admin:theme.settings')`).Scan(&writes, &operations); err != nil {
		t.Fatal(err)
	}
	if writes != 1 || operations != 1 {
		t.Fatalf("rejected/replayed write changed history: audits=%d operations=%d", writes, operations)
	}
	// A site settings update has its own input and cannot overwrite theme.
	settings, err := db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	input := siteInput(settings.SiteProfile, settings.Version)
	input["theme"] = store.PublicSiteTheme{Template: "classic", PrimaryColor: "#000000", LogoVariant: "full"}
	expect(t, request(h, "PUT", "/api/v1/admin/site-settings", input, root, ""), 400)
	if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY['settings.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)`, writerID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", path, nil, root, ""), 200)
	expect(t, request(h, "PUT", path, valid, root, "theme-minimal-save"), 403)
}
