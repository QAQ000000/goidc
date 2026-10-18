package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func siteInput(profile store.SiteProfile, version int64) map[string]any {
	b, _ := json.Marshal(profile)
	var input map[string]any
	_ = json.Unmarshal(b, &input)
	input["expected_version"] = version
	return input
}

func TestSiteSettingsPermissionsValidationAndPublicProfile(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "site-root", "super_admin")
	_, staff := user(t, db, h, "site-staff", "admin")
	_, customer := user(t, db, h, "site-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['dashboard.view'] WHERE id=(SELECT admin_role_id FROM users WHERE username='site-staff')"); err != nil {
		t.Fatal(err)
	}
	settings, err := db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Version != 1 || settings.Name != "智简魔方" || !settings.RegistrationEnabled {
		t.Fatal("site defaults")
	}
	path := "/api/v1/admin/site-settings"
	expect(t, request(h, "GET", path, nil, "", ""), 401)
	for _, cookie := range []string{staff, customer} {
		expect(t, request(h, "GET", path, nil, cookie, ""), 403)
		expect(t, request(h, "PUT", path, siteInput(settings.SiteProfile, 1), cookie, ""), 403)
	}
	key := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "site-key"}, root, "")
	expect(t, key, 201)
	secret := data(t, key)["secret"].(string)
	expect(t, requestBearer(h, "GET", path, nil, secret), 403)
	expect(t, requestBearer(h, "PUT", path, siteInput(settings.SiteProfile, 1), secret), 403)
	missing := siteInput(settings.SiteProfile, 1)
	delete(missing, "registration_enabled")
	expect(t, request(h, "PUT", path, missing, root, ""), 400)
	for _, tc := range []struct{ field, value string }{
		{"site_name", ""}, {"support_email", "Name <test@example.test>"}, {"logo_url", "javascript:alert(1)"},
		{"terms_url", "//evil.example.test"}, {"privacy_url", "http://example.test/privacy"},
		{"support_url", "https://user:secret@example.test"}, {"logo_url", "/\\evil.test/logo.png"},
		{"support_phone", "one\x00two"}, {"terms_url", "relative-path"},
	} {
		input := siteInput(settings.SiteProfile, 1)
		input[tc.field] = tc.value
		expect(t, request(h, "PUT", path, input, root, ""), 409)
	}
	profile := settings.SiteProfile
	profile.Name = "云管理验收站"
	profile.CompanyName = "Example Cloud"
	profile.SupportEmail = "help@example.test"
	profile.TermsURL = "/terms"
	profile.PrivacyURL = "https://example.test/privacy"
	profile.SupportURL = "/console/tickets"
	profile.Description = "云管理\n服务"
	profile.Address = "上海\n测试地址"
	w := request(h, "PUT", path, siteInput(profile, 1), root, "")
	expect(t, w, 200)
	if data(t, w)["version"] != float64(2) {
		t.Fatal("site version")
	}
	w = request(h, "GET", "/api/v1/site", nil, "", "")
	expect(t, w, 200)
	public := data(t, w)
	if len(public) != 19 || public["site_name"] != profile.Name || public["description"] != "云管理 服务" || public["version"] != nil || public["updated_at"] != nil {
		t.Fatal("public profile incorrect", w.Body.String())
	}
	var count int
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='site.settings_updated'").Scan(&count); err != nil || count != 1 {
		t.Fatal("invalid save wrote audit", err)
	}
	expect(t, request(h, "PUT", path, siteInput(profile, 1), root, ""), 409)
}

func TestSiteSettingsConcurrentUpdateAndAuditRollback(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "site-race-root", "super_admin")
	settings, err := db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := settings.SiteProfile
			p.Name = fmt.Sprintf("Site %d", i)
			statuses <- request(h, "PUT", "/api/v1/admin/site-settings", siteInput(p, 1), root, "").Code
		}(i)
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("settings race", counts)
	}
	settings, err = db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_site_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='site.settings_updated' THEN RAISE EXCEPTION 'injected site audit failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_site_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_site_audit();`); err != nil {
		t.Fatal(err)
	}
	profile := settings.SiteProfile
	profile.Name = "Must roll back"
	expect(t, request(h, "PUT", "/api/v1/admin/site-settings", siteInput(profile, settings.Version), root, ""), 500)
	after, err := db.SiteSettings(t.Context())
	if err != nil || after.Name != settings.Name || after.Version != 2 {
		t.Fatal("audit failure left settings changed", err)
	}
}

func TestSiteRegistrationToggleAndWaitingRequest(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "site-register-root", "super_admin")
	settings, err := db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	profile := settings.SiteProfile
	profile.RegistrationEnabled = false
	expect(t, request(h, "PUT", "/api/v1/admin/site-settings", siteInput(profile, 1), root, ""), 200)
	registration := map[string]any{"email": "site-new@example.test", "display_name": "New customer", "password": "integration-password"}
	w := request(h, "POST", "/api/v1/auth/register", registration, "", "")
	expect(t, w, 403)
	if errorData(t, w)["code"] != "registration_disabled" {
		t.Fatal("wrong registration rejection")
	}
	profile.RegistrationEnabled = true
	expect(t, request(h, "PUT", "/api/v1/admin/site-settings", siteInput(profile, 2), root, ""), 200)
	expect(t, request(h, "POST", "/api/v1/auth/register", registration, "", ""), 201)
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT id FROM site_settings WHERE id=1 FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	registration["email"] = "site-waiting@example.test"
	go func() { result <- request(h, "POST", "/api/v1/auth/register", registration, "", "").Code }()
	for {
		var waiting bool
		if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query='SELECT registration_enabled FROM site_settings WHERE id=1 FOR SHARE')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("registration never reached settings row lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE site_settings SET registration_enabled=false,version=version+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-result:
		if status != 403 {
			t.Fatalf("waiting registration status=%d", status)
		}
	case <-ctx.Done():
		t.Fatal("registration did not finish")
	}
	var count int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE email='site-waiting@example.test'").Scan(&count); err != nil || count != 0 {
		t.Fatal("registration bypassed disabled setting", err)
	}
	expect(t, request(h, "POST", "/api/v1/auth/login", map[string]any{"identifier": "site-new@example.test", "password": "integration-password"}, "", ""), 200)
}

func TestCaptchaSettingsReadOnlyOmittedAndRevokedWhileWaiting(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "captcha-settings-root", "super_admin")
	viewerID, viewer := user(t, db, h, "captcha-settings-viewer", "admin")
	writerID, writer := user(t, db, h, "captcha-settings-writer", "admin")
	deniedID, denied := user(t, db, h, "captcha-settings-denied", "admin")
	contentRole(t, db, viewerID, "settings.view")
	contentRole(t, db, writerID, "settings.view", "settings.manage")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", deniedID); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/site-settings"
	expect(t, request(h, "GET", path, nil, viewer, ""), 200)
	expect(t, request(h, "GET", path, nil, denied, ""), 403)
	settings, err := db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PUT", path, siteInput(settings.SiteProfile, settings.Version), viewer, ""), 403)
	key := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "captcha-settings-key"}, root, "")
	expect(t, key, 201)
	secret := data(t, key)["secret"].(string)
	expect(t, requestBearer(h, "GET", path, nil, secret), 403)
	expect(t, requestBearer(h, "PUT", path, siteInput(settings.SiteProfile, settings.Version), secret), 403)
	input := siteInput(settings.SiteProfile, settings.Version)
	input["auth_captcha_enabled"] = true
	expect(t, request(h, "PUT", path, input, root, ""), 200)
	settings, err = db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	omitted := siteInput(settings.SiteProfile, settings.Version)
	delete(omitted, "auth_captcha_enabled")
	expect(t, request(h, "PUT", path, omitted, writer, ""), 200)
	settings, err = db.SiteSettings(t.Context())
	if err != nil || !settings.AuthCaptchaEnabled {
		t.Fatal("omitted captcha flag changed", err)
	}
	expect(t, request(h, "PUT", path, omitted, writer, ""), 409)
	var auditBefore int
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='site.settings_updated'").Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT id FROM site_settings WHERE id=1 FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() {
		result <- request(h, "PUT", path, siteInput(settings.SiteProfile, settings.Version), writer, "").Code
	}()
	for {
		var waiting bool
		if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'SELECT % FROM site_settings WHERE id=1 FOR UPDATE')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("settings write did not wait on row")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE admin_roles SET permissions=ARRAY['settings.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", writerID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-result:
		if status != 403 {
			t.Fatalf("revoked settings write status=%d", status)
		}
	case <-ctx.Done():
		t.Fatal("waiting settings write did not finish")
	}
	after, err := db.SiteSettings(t.Context())
	if err != nil || after.Version != settings.Version {
		t.Fatal("revoked write changed settings", err)
	}
	var auditAfter int
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='site.settings_updated'").Scan(&auditAfter); err != nil || auditAfter != auditBefore {
		t.Fatal("revoked write changed audit history", err)
	}
}

func TestCaptchaSettingsSessionRevokedWhileWaitingForUserLock(t *testing.T) {
	db, h := testServer(t)
	writerID, writer := user(t, db, h, "captcha-session-writer", "admin")
	contentRole(t, db, writerID, "settings.view", "settings.manage")
	settings, err := db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var auditBefore int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='site.settings_updated'").Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", writerID); err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() {
		result <- request(h, "PUT", "/api/v1/admin/site-settings", siteInput(settings.SiteProfile, settings.Version), writer, "").Code
	}()
	for {
		var waiting bool
		if err := db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query='SELECT role,admin_role_id FROM users WHERE id=$1 AND status=''active'' FOR SHARE')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("settings write did not wait on user row")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", writerID); err != nil {
		t.Fatal("session revocation blocked behind settings write:", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-result:
		if status != 401 {
			t.Fatalf("revoked settings write status=%d", status)
		}
	case <-ctx.Done():
		t.Fatal("revoked settings write did not finish")
	}
	after, err := db.SiteSettings(t.Context())
	if err != nil || after.Version != settings.Version {
		t.Fatal("revoked write changed settings", err)
	}
	var auditAfter int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='site.settings_updated'").Scan(&auditAfter); err != nil || auditAfter != auditBefore {
		t.Fatal("revoked write changed audit history", err)
	}
}

func TestSiteAdminSettingsAreVersionedAndNotPublic(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "site-admin-preferences", "super_admin")
	settings, err := db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	input := siteInput(settings.SiteProfile, settings.Version)
	input["admin_settings"] = map[string]any{
		"default_language":           "en_US",
		"language_menu_enabled":      true,
		"records_per_page":           50,
		"frontend_credit_enabled":    true,
		"custom_customer_id_enabled": true,
		"admin_login_days":           14,
		"frontend_ip_check":          true,
		"admin_ip_check":             true,
		"debug_enabled":              false,
	}
	response := request(h, "PUT", "/api/v1/admin/site-settings", input, root, "")
	expect(t, response, 200)
	updated := data(t, response)
	if updated["admin_settings"].(map[string]any)["default_language"] != "en_US" || updated["version"] != float64(settings.Version+1) {
		t.Fatal("admin preferences were not persisted")
	}
	invalid := siteInput(settings.SiteProfile, settings.Version+1)
	invalid["admin_settings"] = map[string]any{"default_language": "zh_CN", "records_per_page": 15, "admin_login_days": 7}
	expect(t, request(h, "PUT", "/api/v1/admin/site-settings", invalid, root, ""), 409)
	public := request(h, "GET", "/api/v1/site", nil, "", "")
	expect(t, public, 200)
	if _, exists := data(t, public)["admin_settings"]; exists {
		t.Fatal("admin preferences leaked into public profile")
	}
}
