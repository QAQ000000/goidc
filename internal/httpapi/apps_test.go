package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/codex/zjmfgo/internal/supplier"
)

func decodeAppsPage(t *testing.T, response *httptest.ResponseRecorder) appCataloguePage {
	t.Helper()
	expect(t, response, http.StatusOK)
	var envelope struct {
		Data appCataloguePage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func seedCompiledApps(t *testing.T, db *store.Store) {
	t.Helper()
	if _, err := db.Pool.Exec(t.Context(), `
UPDATE channel_instances SET enabled=false;
INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,config,secret_ciphertext)
VALUES('apps-sms-enabled','sms','generic_http_json','私密实例名称',true,'{"endpoint":"https://private.invalid/send","private-config":"never-return-config"}',convert_to('never-return-channel-secret','UTF8')),
 ('apps-provision-channel','provision','zjmf','独立渠道配置',false,'{}',convert_to('never-return-provision-secret','UTF8')),
 ('apps-unregistered','sms','not_compiled','不可执行数据库记录',true,'{}','')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
INSERT INTO supplier_accounts(name,provider,base_url,username,credentials_encrypted,active)
VALUES('私密供应商名称',$1,'https://private.invalid/supplier','private-user',convert_to('never-return-supplier-secret','UTF8'),true),
 ('禁用供应商',$1,'https://disabled.invalid/supplier','disabled-user','',false),
 ('未知供应商','not_compiled','https://unknown.invalid/supplier','unknown-user','',true)`, supplier.ProviderZJMF); err != nil {
		t.Fatal(err)
	}
}

func TestCompiledAppsHTTPContractFilteringAndRedaction(t *testing.T) {
	db, h := testServer(t)
	seedCompiledApps(t, db)
	adminID, cookie := user(t, db, h, "apps-contract-super", "super_admin")
	response := request(h, "GET", "/api/v1/admin/apps?view=catalogue&limit=100", nil, cookie, "")
	page := decodeAppsPage(t, response)
	if page.Total != len(channel.List(""))+2 || len(page.Items) != page.Total || page.Limit != 100 || page.Offset != 0 {
		t.Fatalf("catalogue contract: %+v", page)
	}
	items := make(map[string]appCatalogueItem)
	for _, item := range page.Items {
		items[string(item.Descriptor.Category)+":"+item.Descriptor.ID] = item
	}
	sms := items["sms:generic_http_json"]
	if sms.InstanceCount != 2 || sms.EnabledInstanceCount != 1 || sms.SecretConfiguredCount != 1 || sms.Status != "enabled" || !sms.FactoryAvailable || sms.ConfigurationURL != "/admin/channels?category=sms" {
		t.Fatalf("wrong SMS aggregate: %+v", sms)
	}
	provision := items["provision:zjmf"]
	if provision.InstanceCount != 3 || provision.EnabledInstanceCount != 1 || provision.SecretConfiguredCount != 2 || provision.Status != "configuration_incompatible" || provision.UnsupportedInstanceCount != 1 || provision.SupplierAccountCount != 2 || !provision.FactoryAvailable || provision.ConfigurationURL != "/admin/suppliers" {
		t.Fatalf("wrong supplier mapping: %+v", provision)
	}
	metadata := items["provision:nokvm"]
	if metadata.Descriptor.ID != "nokvm" || metadata.FactoryAvailable || metadata.Status != "unconfigured" {
		t.Fatalf("metadata-only factory advertised: %+v", metadata)
	}
	if missing := items["sms:not_compiled"]; missing.Status != "implementation_missing" || missing.InstanceCount != 1 || missing.FactoryAvailable || missing.ConfigurationURL != "/admin/channels?category=sms" {
		t.Fatalf("missing implementation became invisible: %+v", missing)
	}
	if missing := items["provision:not_compiled"]; missing.Status != "implementation_missing" || missing.SupplierAccountCount != 1 || missing.ConfigurationURL != "/admin/suppliers" {
		t.Fatalf("missing supplier became invisible: %+v", missing)
	}
	for _, forbidden := range []string{"never-return", "private.invalid", "private-user", "私密实例名称", "私密供应商名称", "secret_ciphertext", "credentials_encrypted", "\"config\"", "\"secret\":\""} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("response leaked %q", forbidden)
		}
	}
	configured := decodeAppsPage(t, request(h, "GET", "/api/v1/admin/apps?limit=100", nil, cookie, ""))
	for _, item := range configured.Items {
		if item.InstanceCount == 0 || item.Status == "unconfigured" {
			t.Fatalf("configured view includes unconfigured module: %+v", item)
		}
	}
	for _, query := range []string{
		"view=catalogue&category=sms&status=enabled&q=GENERIC_HTTP_JSON",
		"view=configured&category=provision&status=configuration_incompatible&q=%E9%AD%94%E6%96%B9",
		"view=catalogue&category=provision&status=unconfigured&q=nokvm",
	} {
		filtered := decodeAppsPage(t, request(h, "GET", "/api/v1/admin/apps?"+query, nil, cookie, ""))
		if filtered.Total != 1 || len(filtered.Items) != 1 {
			t.Fatalf("bad filter %s: %+v", query, filtered)
		}
	}
	disabled := decodeAppsPage(t, request(h, "GET", "/api/v1/admin/apps?status=disabled&limit=100", nil, cookie, ""))
	if disabled.Total == 0 {
		t.Fatal("disabled filter omitted migrated defaults")
	}
	for _, item := range disabled.Items {
		if item.Status != "disabled" || item.EnabledInstanceCount != 0 || item.InstanceCount == 0 {
			t.Fatalf("bad disabled state: %+v", item)
		}
	}
	second := decodeAppsPage(t, request(h, "GET", "/api/v1/admin/apps?view=catalogue&limit=1&offset=1", nil, cookie, ""))
	if second.Total != page.Total || len(second.Items) != 1 || second.Items[0].Descriptor.ID != page.Items[1].Descriptor.ID {
		t.Fatalf("bad pagination: %+v", second)
	}
	for _, query := range []string{"q=NO_SUCH_MODULE", "view=catalogue&offset=9223372036854775807"} {
		empty := decodeAppsPage(t, request(h, "GET", "/api/v1/admin/apps?"+query, nil, cookie, ""))
		if empty.Items == nil || len(empty.Items) != 0 {
			t.Fatalf("empty page must be []: %+v", empty)
		}
	}
	for _, query := range []string{"view=install", "category=external", "status=ready", "limit=0", "limit=101", "limit=abc", "offset=-1", "offset=99999999999999999999999999", "q=" + strings.Repeat("a", 401)} {
		expect(t, request(h, "GET", "/api/v1/admin/apps?"+query, nil, cookie, ""), http.StatusBadRequest)
	}
	var logged bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM admin_api_logs WHERE actor_id=$1 AND path='/api/v1/admin/apps' AND status_code=200)`, adminID).Scan(&logged); err != nil || !logged {
		t.Fatalf("read access was not logged: %v", err)
	}
	var unmodified bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT bool_and(version=1) FROM channel_instances`).Scan(&unmodified); err != nil || !unmodified {
		t.Fatalf("read changed configuration: %v", err)
	}
}

func TestCompiledAppsRetainMissingAndIncompatibleConfigurations(t *testing.T) {
	counts := []store.AppConfigurationCount{
		{Source: "channel", Category: channel.Mail, ImplementationID: "smtp", SchemaVersion: 2, InstanceCount: 2, EnabledInstanceCount: 1},
		{Source: "channel", Category: channel.Mail, ImplementationID: "smtp", SchemaVersion: 1, InstanceCount: 3, SecretConfiguredCount: 1},
		{Source: "channel", Category: channel.SMS, ImplementationID: "removed_module", SchemaVersion: 1, InstanceCount: 1, EnabledInstanceCount: 1},
		{Source: "supplier", Category: channel.Provision, ImplementationID: "removed_supplier", InstanceCount: 1},
		{Source: "profile", Category: channel.Provision, ImplementationID: "nokvm", InstanceCount: 2, EnabledInstanceCount: 2},
	}
	page := compiledAppsPage(appListQuery{View: "configured", Status: "all", Limit: 100}, counts)
	items := make(map[string]appCatalogueItem)
	for _, item := range page.Items {
		items[string(item.Descriptor.Category)+":"+item.Descriptor.ID] = item
	}
	if len(items) != 4 {
		t.Fatalf("configured view lost rows: %+v", page)
	}
	if smtp := items["mail:smtp"]; smtp.InstanceCount != 5 || smtp.SchemaIncompatibleCount != 2 || smtp.Status != "configuration_incompatible" || smtp.ChannelInstanceCount != 5 {
		t.Fatalf("schema incompatibility was hidden: %+v", smtp)
	}
	if missing := items["sms:removed_module"]; missing.Status != "implementation_missing" || missing.EnabledInstanceCount != 1 || missing.FactoryAvailable {
		t.Fatalf("missing implementation not visible: %+v", missing)
	}
	if profile := items["provision:nokvm"]; profile.ProfileCount != 2 || profile.Status != "enabled" || profile.ConfigurationURL != "/admin/products" || profile.FactoryAvailable {
		t.Fatalf("dedicated profile state incorrect: %+v", profile)
	}
	filtered := compiledAppsPage(appListQuery{View: "configured", Category: "sms", Status: "implementation_missing", Limit: 100}, counts)
	if filtered.Total != 1 || filtered.Items[0].Descriptor.ID != "removed_module" {
		t.Fatalf("missing filter inconsistent: %+v", filtered)
	}
}

func TestCompiledAppsSuperAdminPermission(t *testing.T) {
	db, h := testServer(t)
	_, super := user(t, db, h, "apps-permission-super", "super_admin")
	_, fullAdmin := user(t, db, h, "apps-permission-full-admin", "admin")
	readerID, reader := user(t, db, h, "apps-permission-reader", "admin")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY['settings.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)`, readerID); err != nil {
		t.Fatal(err)
	}
	_, customer := user(t, db, h, "apps-permission-customer", "customer")
	expect(t, request(h, "GET", "/api/v1/admin/apps", nil, "", ""), http.StatusUnauthorized)
	expect(t, request(h, "GET", "/api/v1/admin/apps", nil, customer, ""), http.StatusUnauthorized)
	for _, cookie := range []string{fullAdmin, reader} {
		expect(t, request(h, "GET", "/api/v1/admin/apps", nil, cookie, ""), http.StatusForbidden)
	}
	expect(t, request(h, "GET", "/api/v1/admin/apps", nil, super, ""), http.StatusOK)
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		expect(t, request(h, method, "/api/v1/admin/apps", map[string]any{"source": "https://untrusted.invalid/plugin"}, super, ""), http.StatusMethodNotAllowed)
	}
}

func TestCompiledAppsBuildMetadataAndQueryValidation(t *testing.T) {
	for _, info := range []*debug.BuildInfo{nil, {Main: debug.Module{Version: "(devel)"}}} {
		build := appBuildMetadata(info)
		if build.Version != nil || build.Revision != nil || build.Modified != nil {
			t.Fatalf("unknown build should remain null: %+v", build)
		}
	}
	build := appBuildMetadata(&debug.BuildInfo{GoVersion: "go1.25.1", Main: debug.Module{Version: "v1.2.3"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "real-revision"}, {Key: "vcs.modified", Value: "true"}}})
	if build.Version == nil || *build.Version != "v1.2.3" || build.Revision == nil || *build.Revision != "real-revision" || build.Modified == nil || !*build.Modified || build.GoVersion == nil || *build.GoVersion != "go1.25.1" {
		t.Fatalf("build metadata lost: %+v", build)
	}
	query, err := parseAppListQuery(url.Values{})
	if err != nil || query.View != "configured" || query.Limit != 25 || query.Status != "all" {
		t.Fatalf("wrong defaults: %+v, %v", query, err)
	}
	for _, values := range []url.Values{{"limit": {""}}, {"limit": {"101"}}, {"offset": {"-1"}}, {"category": {"external"}}} {
		if _, err := parseAppListQuery(values); err == nil {
			t.Fatalf("invalid query accepted: %v", values)
		}
	}
}
