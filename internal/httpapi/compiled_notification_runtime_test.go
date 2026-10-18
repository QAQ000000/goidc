package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/notification"
	"github.com/codex/zjmfgo/internal/store"
)

// These adapters intentionally have no host/from or endpoint/token fields.
// Successful delivery proves the shared store/worker path uses the registered
// factory's complete schema rather than a built-in transport's configuration.
type compiledFixtureMail struct{ account, first, second string }

func (p compiledFixtureMail) Enabled() bool { return p.account != "" }
func (p compiledFixtureMail) Send(job store.EmailJob) error {
	if p.account != "account" || p.first != "first-private" || p.second != "second-private" || job.Recipient != "recipient@example.test" {
		return errors.New("fixture mail configuration rejected")
	}
	return nil
}

type compiledFixtureSMS struct{ project, credential string }

func (p compiledFixtureSMS) Send(_ context.Context, _ store.SMSRuntimeConfig, job store.SMSJob) error {
	if p.project != "project" || p.credential != "sms-private" || job.Recipient != "+8613800138000" {
		return errors.New("fixture SMS configuration rejected")
	}
	return nil
}

var registerCompiledNotificationFixtures sync.Once

func compiledNotificationFixtures(t *testing.T) {
	t.Helper()
	registerCompiledNotificationFixtures.Do(func() {
		if err := notification.RegisterMailProvider(channel.Descriptor{
			ID: "fixture_mail_runtime", Name: "Fixture compiled mail", Category: channel.Mail, Capabilities: []string{"send", "test"},
			Fields: []channel.Field{
				{Key: "account", Label: "Account", Type: channel.Text, Required: true},
				{Key: "first", Label: "First", Type: channel.Text, Required: true, Secret: true},
				{Key: "second", Label: "Second", Type: channel.Text, Required: true, Secret: true},
			},
		}, func(config module.Config) (notification.MailProvider, error) {
			account, _ := config.Values["account"].(string)
			return compiledFixtureMail{account, config.Secrets["first"], config.Secrets["second"]}, nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := notification.RegisterSMSFactory(channel.Descriptor{
			ID: "fixture_sms_runtime", Name: "Fixture compiled SMS", Category: channel.SMS, Capabilities: []string{"send", "test"},
			Fields: []channel.Field{
				{Key: "project", Label: "Project", Type: channel.Text, Required: true},
				{Key: "credential", Label: "Credential", Type: channel.Text, Required: true, Secret: true},
			},
		}, func(config module.Config) (notification.SMSProvider, error) {
			project, _ := config.Values["project"].(string)
			return compiledFixtureSMS{project, config.Secrets["credential"]}, nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCompiledMailAdapterRunsThroughInstanceOutbox(t *testing.T) {
	compiledNotificationFixtures(t)
	db, handler := testServer(t)
	_, admin := user(t, db, handler, "compiled-mail-admin", "super_admin")
	created := request(handler, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{
		"id": "compiled-mail-fixture", "category": "mail", "implementation_id": "fixture_mail_runtime",
		"display_name": "Compiled mail", "enabled": true,
		"config": map[string]any{"account": "account"}, "secure_config": map[string]string{"first": "first-private", "second": "second-private"},
	}, admin, "")
	expect(t, created, http.StatusCreated)
	updated := request(handler, http.MethodPut, "/api/v1/admin/channel-instances/compiled-mail-fixture", map[string]any{
		"display_name": "Compiled mail updated", "enabled": true, "config": map[string]any{"account": "account"}, "expected_version": 1,
	}, admin, "")
	expect(t, updated, http.StatusOK)
	expect(t, request(handler, http.MethodPost, "/api/v1/admin/smtp-settings/test", map[string]any{
		"recipient": "recipient@example.test", "channel_instance_id": "compiled-mail-fixture", "expected_version": 1,
	}, admin, ""), http.StatusConflict)
	expect(t, request(handler, http.MethodPost, "/api/v1/admin/smtp-settings/test", map[string]any{
		"recipient": "recipient@example.test", "channel_instance_id": "compiled-mail-fixture", "expected_version": 2,
	}, admin, ""), http.StatusAccepted)
	job, err := db.ClaimEmailJob(t.Context())
	if err != nil || job.ChannelInstanceID != "compiled-mail-fixture" {
		t.Fatal("test job did not bind the selected instance", err)
	}
	implementation, config, err := db.ResolveMailConfigForInstance(t.Context(), job.ChannelInstanceID, store.SMTPRuntimeConfig{})
	if err != nil || implementation != "fixture_mail_runtime" {
		t.Fatal("new mail implementation was not resolved", err)
	}
	if err = notification.SendMail(implementation, config, *job); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteEmailJob(t.Context(), *job, nil); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = db.Pool.QueryRow(t.Context(), "SELECT status FROM email_outbox WHERE id=$1", job.ID).Scan(&status); err != nil || status != "sent" {
		t.Fatal("mail job was not completed", status, err)
	}
	implementation, _, err = db.ResolveMailConfigForInstance(t.Context(), "", store.SMTPRuntimeConfig{})
	if err != nil || implementation != "fixture_mail_runtime" {
		t.Fatal("default mail still assumes SMTP", err)
	}
	expect(t, request(handler, http.MethodPut, "/api/v1/admin/channel-instances/compiled-mail-fixture", map[string]any{
		"display_name": "Compiled mail disabled", "enabled": false, "config": map[string]any{"account": "account"}, "expected_version": 2,
	}, admin, ""), http.StatusOK)
	if _, _, err = db.ResolveMailConfigForInstance(t.Context(), job.ChannelInstanceID, store.SMTPRuntimeConfig{}); err == nil {
		t.Fatal("bound disabled mail channel was accepted")
	}
}

func TestCompiledSMSAdapterRunsThroughInstanceOutbox(t *testing.T) {
	compiledNotificationFixtures(t)
	db, handler := testServer(t)
	_, admin := user(t, db, handler, "compiled-sms-admin", "super_admin")
	expect(t, request(handler, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{
		"id": "compiled-sms-fixture", "category": "sms", "implementation_id": "fixture_sms_runtime",
		"display_name": "Compiled SMS", "enabled": true,
		"config": map[string]any{"project": "project"}, "secure_config": map[string]string{"credential": "sms-private"},
	}, admin, ""), http.StatusCreated)
	expect(t, request(handler, http.MethodPost, "/api/v1/admin/sms-settings/test", map[string]any{
		"recipient": "+8613800138000", "channel_instance_id": "compiled-sms-fixture", "expected_version": 1,
	}, admin, ""), http.StatusAccepted)
	job, err := db.ClaimSMSJob(t.Context())
	if err != nil || job.ChannelInstanceID != "compiled-sms-fixture" {
		t.Fatal("test job did not bind the selected instance", err)
	}
	runtime, err := db.ResolveSMSConfigForInstance(t.Context(), job.ChannelInstanceID)
	if err != nil || runtime.Provider != "fixture_sms_runtime" {
		t.Fatal("new SMS implementation was not resolved", err)
	}
	evidence, err := notification.SendSMSWithEvidence(t.Context(), runtime, *job)
	if err != nil || evidence.Outcome != "accepted" {
		t.Fatal("new SMS provider could not run its own configuration", err)
	}
	if err = db.SaveSMSDeliveryEvidence(t.Context(), *job, evidence); err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteSMSJob(t.Context(), *job, nil); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = db.Pool.QueryRow(t.Context(), "SELECT status FROM sms_outbox WHERE id=$1", job.ID).Scan(&status); err != nil || status != "sent" {
		t.Fatal("SMS job was not completed", status, err)
	}
	runtime, err = db.ResolveSMSConfig(t.Context())
	if err != nil || runtime.Provider != "fixture_sms_runtime" {
		t.Fatal("default SMS still assumes a built-in provider", err)
	}
}

func TestNotificationEnableValidatesLocalTransportConfiguration(t *testing.T) {
	db, handler := testServer(t)
	_, admin := user(t, db, handler, "compiled-validation-admin", "super_admin")
	for _, config := range []map[string]any{
		{"host": "smtp.example.test", "port": 587, "from": "sender@example.test", "username": "mailer", "tls_mode": "starttls"},
		{"host": "smtp.example.test", "port": 587, "from": "invalid-address", "tls_mode": "starttls"},
		{"host": "smtp.example.test", "port": 587, "from": "sender@example.test", "username": "mailer", "tls_mode": "none"},
	} {
		expect(t, request(handler, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{
			"category": "mail", "implementation_id": "smtp", "display_name": "Invalid SMTP", "enabled": true, "config": config,
		}, admin, ""), http.StatusConflict)
	}
	expect(t, request(handler, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{
		"category": "sms", "implementation_id": "generic_http_json", "display_name": "Invalid endpoint", "enabled": true,
		"config": map[string]any{"endpoint": "http://insecure.example.test/sms"}, "secure_config": map[string]string{"token": "private"},
	}, admin, ""), http.StatusConflict)
}

func TestDefaultIDCSmartSMSUsesInstanceConfiguration(t *testing.T) {
	db, handler := testServer(t)
	_, admin := user(t, db, handler, "compiled-idcsmart-admin", "super_admin")
	// Restore a legacy default instance selecting a provider-specific schema.
	// Its singleton compatibility row has no api/sign columns, so resolution
	// must retain the channel row instead of rebuilding it from sms_settings.
	if _, err := db.Pool.Exec(t.Context(), `UPDATE channel_instances SET implementation_id='idcsmart',config='{}' WHERE id='sms-default'`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(handler, http.MethodPut, "/api/v1/admin/channel-instances/sms-default", map[string]any{
		"display_name": "Default IDCsmart", "enabled": true, "expected_version": 1,
		"config": map[string]any{"api": "app-id", "sign": "fixture-sign"}, "secure_config": map[string]string{"key": "fixture-private"},
	}, admin, ""), http.StatusOK)
	runtime, err := db.ResolveSMSConfig(t.Context())
	if err != nil || runtime.Provider != "idcsmart" || runtime.API != "app-id" || runtime.Sign != "fixture-sign" || runtime.Key != "fixture-private" {
		t.Fatal("default provider-specific SMS configuration was lost", err)
	}
	if _, err = notification.NewSMSProvider(runtime.Provider, runtime.ModuleConfig); err != nil {
		t.Fatal("default SMS factory rejected the restored instance", err)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE channel_instances SET enabled=false,schema_version=2 WHERE id='sms-default'"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ResolveSMSConfig(t.Context()); err == nil {
		t.Fatal("legacy SMS fallback bypassed the default instance schema")
	}
}

func TestLegacySMTPResolversCannotBypassInstanceSchema(t *testing.T) {
	db, handler := testServer(t)
	_, admin := user(t, db, handler, "compiled-smtp-schema-admin", "super_admin")
	fallback := store.SMTPRuntimeConfig{Host: "smtp.example.test", Port: 587, From: "sender@example.test", TLSMode: "starttls"}
	if _, _, err := db.ResolveMailConfigForInstance(t.Context(), "", fallback); err != nil {
		t.Fatal("compatible deployment fallback was rejected", err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE channel_instances SET schema_version=2 WHERE id='mail-smtp-default'"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.ResolveMailConfigForInstance(t.Context(), "", fallback); err == nil {
		t.Fatal("unbound mail bypassed an incompatible default through deployment fallback")
	}
	if _, err := db.ResolveSMTPConfigForInstance(t.Context(), "mail-smtp-default", fallback); err == nil {
		t.Fatal("legacy explicit SMTP resolver bypassed an incompatible schema")
	}
	created := request(handler, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{
		"id": "smtp-schema-fixture", "category": "mail", "implementation_id": "smtp", "display_name": "SMTP schema fixture", "enabled": true,
		"config": map[string]any{"host": "smtp.example.test", "port": 587, "from": "sender@example.test", "tls_mode": "starttls"},
	}, admin, "")
	expect(t, created, http.StatusCreated)
	if _, err := db.ResolveSMTPConfigForInstance(t.Context(), "smtp-schema-fixture", fallback); err != nil {
		t.Fatal("valid legacy explicit SMTP resolver failed", err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE channel_instances SET schema_version=2 WHERE id='smtp-schema-fixture'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResolveSMTPConfigForInstance(t.Context(), "smtp-schema-fixture", fallback); err == nil {
		t.Fatal("enabled legacy SMTP resolver bypassed an incompatible schema")
	}
}

func TestCompiledInstancesCanStartWithoutCredentials(t *testing.T) {
	compiledNotificationFixtures(t)
	db, handler := testServer(t)
	_, admin := user(t, db, handler, "compiled-empty-credentials-admin", "super_admin")
	for _, input := range []map[string]any{
		{"id": "compiled-empty-secrets", "category": "mail", "implementation_id": "fixture_mail_runtime", "display_name": "Disabled empty credentials", "enabled": false, "config": map[string]any{"account": "account"}},
		{"id": "compiled-no-secrets", "category": "payment", "implementation_id": "mock", "display_name": "No credentials schema", "enabled": false, "config": map[string]any{}},
	} {
		created := request(handler, http.MethodPost, "/api/v1/admin/channel-instances", input, admin, "")
		expect(t, created, http.StatusCreated)
		if data(t, created)["secret_configured"] != false {
			t.Fatal("empty credentials reported as configured")
		}
		var empty, fieldsEmpty bool
		if err := db.Pool.QueryRow(t.Context(), "SELECT secret_ciphertext=''::bytea,secret_fields='{}'::jsonb FROM channel_instances WHERE id=$1", input["id"]).Scan(&empty, &fieldsEmpty); err != nil || !empty || !fieldsEmpty {
			t.Fatal("empty credentials were not stored as non-null empty values", err)
		}
	}
}
