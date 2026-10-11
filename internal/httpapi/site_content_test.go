package httpapi

import (
	"strconv"
	"testing"
)

func TestSiteContentVersionedCRUDAndPermissions(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "site-content-root", "super_admin")
	_, customer := user(t, db, h, "site-content-customer", "customer")
	for _, path := range []string{"/api/v1/admin/theme-settings", "/api/v1/admin/site-fields", "/api/v1/admin/navigation", "/api/v1/admin/friendly-links", "/api/v1/admin/file-groups"} {
		expect(t, request(h, "GET", path, nil, customer, ""), 403)
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
