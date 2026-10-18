package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
)

func certificationSettingsErrorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Error.Code
}

func TestCertificationSettingsContractAndPermissions(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "cert-settings-root", "super_admin")
	_, staff := user(t, db, h, "cert-settings-reader", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['certifications.view'] WHERE id=(SELECT admin_role_id FROM users WHERE username='cert-settings-reader')"); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/certification-settings"
	reader := request(h, http.MethodGet, path, nil, staff, "")
	expect(t, reader, http.StatusOK)
	settings := data(t, reader)
	if settings["version"] != float64(1) || settings["default_provider_id"] != "manual" || settings["default_instance_id"] != "manual-default" {
		t.Fatalf("unexpected certification settings: %#v", settings)
	}
	if _, ok := settings["instances"]; !ok {
		t.Fatal("missing certification channel instances")
	}
	input := map[string]any{
		"enabled": true, "require_for_purchase": true, "default_provider_id": "manual",
		"advanced_enabled": true, "auto_update_name": true, "require_phone_match": true,
		"notify_on_approval": true, "suspend_unverified": true, "allow_file_upload": true,
		"expected_version": 1,
	}
	expect(t, request(h, http.MethodPut, path, input, staff, "cert-settings-1"), http.StatusForbidden)
	updated := request(h, http.MethodPut, path, input, root, "cert-settings-1")
	expect(t, updated, http.StatusOK)
	if data(t, updated)["version"] != float64(2) {
		t.Fatalf("expected version 2: %v", updated.Body.String())
	}
	replay := request(h, http.MethodPut, path, input, root, "cert-settings-1")
	expect(t, replay, http.StatusOK)
	if data(t, replay)["replayed"] != true {
		t.Fatal("expected idempotent replay")
	}
	input["enabled"] = false
	expect(t, request(h, http.MethodPut, path, input, root, "cert-settings-1"), http.StatusConflict)
	input["expected_version"] = 1
	expect(t, request(h, http.MethodPut, path, input, root, "cert-settings-2"), http.StatusConflict)
	fresh := request(h, http.MethodGet, path, nil, root, "")
	expect(t, fresh, http.StatusOK)
	if data(t, fresh)["version"] != float64(2) || data(t, fresh)["suspend_unverified"] != true {
		t.Fatalf("settings were not persisted: %v", fresh.Body.String())
	}
	if data(t, fresh)["default_instance_id"] != "manual-default" {
		t.Fatal("legacy manual settings did not retain the explicit default instance")
	}
}

func certificationSettingsBody(instance string, version int) map[string]any {
	return map[string]any{
		"enabled": true, "require_for_purchase": false, "default_provider_id": "manual", "default_instance_id": instance,
		"advanced_enabled": false, "auto_update_name": false, "require_phone_match": false,
		"notify_on_approval": false, "suspend_unverified": false, "allow_file_upload": false, "expected_version": version,
	}
}

func TestCertificationSettingsExplicitInstanceAndReplay(t *testing.T) {
	db, h := testServer(t)
	actor, root := user(t, db, h, "cert-instance-root", "super_admin")
	created := request(h, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{
		"id": "cert-manual-second", "category": "certification", "implementation_id": "manual", "display_name": "第二人工核验", "enabled": true, "config": map[string]any{},
	}, root, "")
	expect(t, created, http.StatusCreated)
	path := "/api/v1/admin/certification-settings"
	input := certificationSettingsBody("cert-manual-second", 1)
	expect(t, request(h, http.MethodPut, path, input, root, "cert-selection-save"), http.StatusOK)
	fresh := request(h, http.MethodGet, path, nil, root, "")
	expect(t, fresh, http.StatusOK)
	if got := data(t, fresh); got["default_instance_id"] != "cert-manual-second" || got["default_provider_id"] != "manual" || got["version"] != float64(2) {
		t.Fatalf("specific instance was not selected: %s", fresh.Body.String())
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE channel_instances SET enabled=false WHERE id='cert-manual-second'"); err != nil {
		t.Fatal(err)
	}
	// An idempotent receipt describes the completed write, even if that instance
	// is subsequently disabled. New writes must revalidate the current instance.
	replay := request(h, http.MethodPut, path, input, root, "cert-selection-save")
	expect(t, replay, http.StatusOK)
	if data(t, replay)["replayed"] != true {
		t.Fatal("selection replay did not return its original receipt")
	}
	input["expected_version"] = 2
	expect(t, request(h, http.MethodPut, path, input, root, "cert-selection-new-write"), http.StatusConflict)
	var version int
	var audits int
	if err := db.Pool.QueryRow(t.Context(), "SELECT version FROM certification_settings WHERE id=1").Scan(&version); err != nil || version != 2 {
		t.Fatalf("invalid new write changed settings: version=%d err=%v", version, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='certification.settings_updated'", actor).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("invalid write or replay added an audit: count=%d err=%v", audits, err)
	}
}

func TestCertificationSettingsRejectsUnavailableInstanceWithoutMutation(t *testing.T) {
	db, h := testServer(t)
	actor, root := user(t, db, h, "cert-invalid-root", "super_admin")
	const missingFactory = "cert-settings-no-factory"
	if _, exists := channel.Lookup(channel.Certification, missingFactory); !exists {
		if err := channel.Register(channel.Descriptor{ID: missingFactory, Name: "无工厂测试", Category: channel.Certification, Capabilities: []string{"submit"}}); err != nil {
			t.Fatal(err)
		}
	}
	const needsSecret = "cert-settings-secret"
	if _, exists := channel.Lookup(channel.Certification, needsSecret); !exists {
		if err := channel.RegisterFactory(channel.Descriptor{ID: needsSecret, Name: "凭据校验测试", Category: channel.Certification, Capabilities: []string{"submit"}, Fields: []channel.Field{{Key: "token", Label: "Token", Type: channel.Text, Required: true, Secret: true}}}, func(any) (any, error) {
			panic("settings validation must not construct an external provider")
		}); err != nil {
			t.Fatal(err)
		}
	}
	const noSubmit = "cert-settings-no-submit"
	if _, exists := channel.Lookup(channel.Certification, noSubmit); !exists {
		if err := channel.RegisterFactory(channel.Descriptor{ID: noSubmit, Name: "不支持提交测试", Category: channel.Certification}, func(any) (any, error) {
			panic("settings validation must not construct an external provider")
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,config,schema_version) VALUES
 ('cert-disabled','certification','manual','停用认证',false,'{}',1),
 ('cert-incompatible','certification','manual','不兼容认证',true,'{}',999),
 ('cert-invalid-config','certification','manual','无效配置',true,'{"unexpected":true}',1),
 ('cert-no-factory','certification',$1,'缺失工厂',true,'{}',1),
 ('cert-no-secret','certification',$2,'缺失凭据',true,'{}',1),
 ('cert-no-submit','certification',$3,'不支持提交',true,'{}',1)`, missingFactory, needsSecret, noSubmit); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ instance, provider, code string }{
		{"cert-missing", "manual", "certification_instance_unavailable"},
		{"cert-disabled", "manual", "channel_unavailable"},
		{"sms-default", "manual", "certification_instance_mismatch"},
		{"cert-incompatible", "manual", "channel_schema_incompatible"},
		{"cert-invalid-config", "manual", "invalid_channel_config"},
		{"cert-no-factory", missingFactory, "channel_unavailable"},
		{"cert-no-secret", needsSecret, "invalid_channel_config"},
		{"cert-no-submit", noSubmit, "certification_submit_unavailable"},
		{"manual-default", needsSecret, "certification_instance_mismatch"},
	} {
		t.Run(tc.instance+"-"+tc.provider, func(t *testing.T) {
			input := certificationSettingsBody(tc.instance, 1)
			input["default_provider_id"] = tc.provider
			response := request(h, http.MethodPut, "/api/v1/admin/certification-settings", input, root, "cert-invalid-"+tc.instance+"-"+tc.provider)
			expect(t, response, http.StatusConflict)
			if got := certificationSettingsErrorCode(t, response); got != tc.code {
				t.Fatalf("expected %s, got %s", tc.code, got)
			}
		})
	}
	// An old non-manual client cannot silently select any matching instance.
	input := certificationSettingsBody("", 1)
	input["default_provider_id"] = needsSecret
	delete(input, "default_instance_id")
	response := request(h, http.MethodPut, "/api/v1/admin/certification-settings", input, root, "cert-missing-instance")
	expect(t, response, http.StatusBadRequest)
	if code := certificationSettingsErrorCode(t, response); code != "certification_instance_required" {
		t.Fatalf("missing explicit instance: %s", code)
	}
	for index, instance := range []string{"", "bad instance", "a\nb", fmt.Sprintf("%065d", 1)} {
		response := request(h, http.MethodPut, "/api/v1/admin/certification-settings", certificationSettingsBody(instance, 1), root, fmt.Sprintf("cert-invalid-id-%d", index))
		expect(t, response, http.StatusBadRequest)
	}
	var current string
	var version, receipts int
	if err := db.Pool.QueryRow(t.Context(), "SELECT default_instance_id,version FROM certification_settings WHERE id=1").Scan(&current, &version); err != nil || current != "manual-default" || version != 1 {
		t.Fatalf("invalid selection mutated settings: %s v%d err=%v", current, version, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mutation_operations WHERE actor_id=$1 AND scope='admin:certification.settings'", actor).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("invalid selection retained a receipt: %d err=%v", receipts, err)
	}
}

func TestCertificationSettingsLocksSettingsBeforeInstance(t *testing.T) {
	db, h := testServer(t)
	actor, root := user(t, db, h, "cert-lock-order-root", "super_admin")
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	blocker, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(ctx, "SELECT id FROM certification_settings WHERE id=1 FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- request(h, http.MethodPut, "/api/v1/admin/certification-settings", certificationSettingsBody("manual-default", 1), root, "cert-settings-lock-order")
	}()
	waitQueryLock(t, db, `SELECT version FROM "certification_settings" WHERE id=$1 FOR UPDATE`)
	// A waiting settings write must not already hold an instance share lock.
	// Otherwise this edit either times out or forms the reverse lock order.
	if _, err = blocker.Exec(ctx, "SET LOCAL lock_timeout='1s'"); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, "UPDATE channel_instances SET enabled=false WHERE id='manual-default'"); err != nil {
		t.Fatalf("waiting settings writer locked the instance before settings: %v", err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-done:
		expect(t, response, http.StatusConflict)
		if code := certificationSettingsErrorCode(t, response); code != "channel_unavailable" {
			t.Fatalf("instance disabled during settings wait was not revalidated: %s", code)
		}
	case <-ctx.Done():
		t.Fatal("settings writer stalled after instance edit")
	}
	var version, audits, receipts int
	if err = db.Pool.QueryRow(t.Context(), "SELECT version FROM certification_settings WHERE id=1").Scan(&version); err != nil || version != 1 {
		t.Fatalf("failed write mutated settings: version=%d err=%v", version, err)
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='certification.settings_updated'", actor).Scan(&audits); err != nil || audits != 0 {
		t.Fatalf("failed write left an audit: count=%d err=%v", audits, err)
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mutation_operations WHERE actor_id=$1 AND scope='admin:certification.settings'", actor).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("failed write left a receipt: count=%d err=%v", receipts, err)
	}
}
