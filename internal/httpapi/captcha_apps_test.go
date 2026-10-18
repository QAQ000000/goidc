package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/captcha"
	"github.com/codex/zjmfgo/internal/config"
)

func TestCaptchaModuleApplicationDirectory(t *testing.T) {
	t.Run("builtin_switch_and_filters", func(t *testing.T) {
		db, h := testServer(t)
		_, admin := user(t, db, h, "captcha-apps-root", "super_admin")
		fetch := func(query string) appCataloguePage {
			t.Helper()
			return decodeAppsPage(t, request(h, "GET", "/api/v1/admin/apps?category=captcha&"+query, nil, admin, ""))
		}
		for _, view := range []string{"catalogue", "configured"} {
			page := fetch("view=" + view + "&q=builtin_png")
			if page.Total != 1 || len(page.Items) != 1 {
				t.Fatalf("builtin absent from %s view: %+v", view, page)
			}
			item := page.Items[0]
			if item.Descriptor.ID != captcha.BuiltinImplementationID || !item.FactoryAvailable || item.InstanceCount != 1 || item.ProfileCount != 1 || item.EnabledInstanceCount != 0 || item.SecretConfiguredCount != 0 || item.Status != "disabled" || item.ConfigurationURL != "/admin/auth-settings" || item.ChannelInstanceCount != 0 {
				t.Fatalf("disabled builtin directory state: %+v", item)
			}
		}
		if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=true"); err != nil {
			t.Fatal(err)
		}
		enabled := fetch("view=configured&status=enabled")
		if enabled.Total != 1 || len(enabled.Items) != 1 || enabled.Items[0].Descriptor.ID != captcha.BuiltinImplementationID || enabled.Items[0].EnabledInstanceCount != 1 || enabled.Items[0].Status != "enabled" {
			t.Fatalf("enabled builtin directory state: %+v", enabled)
		}
		if disabled := fetch("view=configured&status=disabled"); disabled.Total != 0 || len(disabled.Items) != 0 {
			t.Fatalf("disabled filter retained active builtin: %+v", disabled)
		}
		if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=false"); err != nil {
			t.Fatal(err)
		}
		if enabled := fetch("view=configured&status=enabled"); enabled.Total != 0 || len(enabled.Items) != 0 {
			t.Fatalf("site switch failed to disable the profile: %+v", enabled)
		}
	})

	t.Run("profiles_active_selection_and_redaction", func(t *testing.T) {
		active, _ := captchaModuleProfile(t, captcha.Widget, "")
		retained, _ := captchaModuleProfile(t, captcha.Widget, "")
		manager := captchaModuleManager(t, active.ID, active, retained)
		db, h := testServerConfig(t, func(cfg *config.Config) { cfg.Captcha = manager })
		_, admin := user(t, db, h, "captcha-apps-profile-root", "super_admin")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=true"); err != nil {
			t.Fatal(err)
		}
		response := request(h, "GET", "/api/v1/admin/apps?category=captcha&view=configured&limit=100", nil, admin, "")
		page := decodeAppsPage(t, response)
		if page.Total != 2 || len(page.Items) != 2 {
			t.Fatalf("deployment profiles were not aggregated by implementation: %+v", page)
		}
		items := map[string]appCatalogueItem{}
		for _, item := range page.Items {
			items[item.Descriptor.ID] = item
			if item.ConfigurationURL != "/admin/auth-settings" {
				t.Fatalf("captcha directory linked to the wrong configuration: %+v", item)
			}
		}
		builtin := items[captcha.BuiltinImplementationID]
		if builtin.InstanceCount != 1 || builtin.EnabledInstanceCount != 0 || builtin.SecretConfiguredCount != 0 || builtin.Status != "disabled" {
			t.Fatalf("inactive builtin was advertised as enabled: %+v", builtin)
		}
		widget := items[active.ImplementationID]
		if widget.InstanceCount != 2 || widget.ProfileCount != 2 || widget.EnabledInstanceCount != 1 || widget.SecretConfiguredCount != 2 || widget.Status != "enabled" {
			t.Fatalf("active and retained profile counts: %+v", widget)
		}
		for _, forbidden := range []string{active.ID, retained.ID, "private-server-only-state", "profile_fingerprint", "provider_state", "\"secrets\"", "\"config\""} {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatalf("application directory exposed private deployment data %q", forbidden)
			}
		}
		if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=false"); err != nil {
			t.Fatal(err)
		}
		disabled := decodeAppsPage(t, request(h, "GET", "/api/v1/admin/apps?category=captcha&view=configured&status=disabled", nil, admin, ""))
		if disabled.Total != 2 {
			t.Fatalf("site switch did not disable every captcha implementation: %+v", disabled)
		}
		for _, item := range disabled.Items {
			if item.EnabledInstanceCount != 0 {
				t.Fatalf("disabled site retained enabled profile count: %+v", item)
			}
		}
	})

	t.Run("site_switch_read_failure", func(t *testing.T) {
		db, h := testServer(t)
		_, admin := user(t, db, h, "captcha-apps-storage-root", "super_admin")
		if _, err := db.Pool.Exec(t.Context(), "ALTER TABLE site_settings RENAME COLUMN auth_captcha_enabled TO unavailable_captcha_switch"); err != nil {
			t.Fatal(err)
		}
		// A failed switch read must not turn into a misleading enabled or zero
		// count. The catalogue retains the normal API error semantics.
		expect(t, request(h, "GET", "/api/v1/admin/apps?category=captcha", nil, admin, ""), http.StatusInternalServerError)
	})
}
