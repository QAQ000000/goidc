package notification

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/store"
)

type fixtureMailProvider struct{ account, first, second string }

func (p fixtureMailProvider) Enabled() bool { return p.account != "" }
func (p fixtureMailProvider) Send(job store.EmailJob) error {
	if p.account != "fixture-account" || p.first != "one" || p.second != "two" || job.Body != "fixture-body" {
		return errors.New("upstream echoed private credential one")
	}
	return nil
}

type fixtureSMSProvider struct{ project, key string }

func (p fixtureSMSProvider) Send(_ context.Context, _ store.SMSRuntimeConfig, job store.SMSJob) error {
	if p.project != "fixture-project" || p.key != "three" || job.Body != "fixture-body" {
		return errors.New("upstream echoed private credential three")
	}
	return nil
}

var registerDispatchFixtures sync.Once

func registerDispatchProviders(t *testing.T) {
	t.Helper()
	registerDispatchFixtures.Do(func() {
		if err := RegisterMailProvider(channel.Descriptor{
			ID: "fixture_mail_dispatch", Name: "Fixture mail", Category: channel.Mail,
			Capabilities: []string{"send"}, Fields: []channel.Field{
				{Key: "account", Label: "Account", Type: channel.Text, Required: true},
				{Key: "first", Label: "First", Type: channel.Text, Required: true, Secret: true},
				{Key: "second", Label: "Second", Type: channel.Text, Required: true, Secret: true},
			},
		}, func(config module.Config) (MailProvider, error) {
			return fixtureMailProvider{configString(config.Values, "account"), config.Secrets["first"], config.Secrets["second"]}, nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := RegisterSMSFactory(channel.Descriptor{
			ID: "fixture_sms_dispatch", Name: "Fixture SMS", Category: channel.SMS,
			Capabilities: []string{"send"}, Fields: []channel.Field{
				{Key: "project", Label: "Project", Type: channel.Text, Required: true},
				{Key: "credential", Label: "Credential", Type: channel.Text, Required: true, Secret: true},
			},
		}, func(config module.Config) (SMSProvider, error) {
			return fixtureSMSProvider{configString(config.Values, "project"), config.Secrets["credential"]}, nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestMailDispatchUsesRegisteredFactoryAndIndependentSecrets(t *testing.T) {
	registerDispatchProviders(t)
	config := module.Config{Values: map[string]any{"account": "fixture-account"}, Secrets: map[string]string{"first": "one", "second": "two"}}
	if err := SendMail("fixture_mail_dispatch", config, store.EmailJob{Body: "fixture-body"}); err != nil {
		t.Fatal(err)
	}
	if err := SendMail("fixture_mail_dispatch", config, store.EmailJob{Body: "rejected"}); err == nil || err.Error() != "mail delivery failed" {
		t.Fatal("provider error was not redacted", err)
	}
	if err := SendMail("missing_mail", config, store.EmailJob{}); err == nil || err.Error() != "mail channel unavailable" {
		t.Fatal("missing factory was accepted", err)
	}
}

func TestSMSDispatchUsesRegisteredFactoryWithoutEndpointAssumptions(t *testing.T) {
	registerDispatchProviders(t)
	runtime := store.SMSRuntimeConfig{
		Provider: "fixture_sms_dispatch", Enabled: true,
		ModuleConfig: module.Config{Values: map[string]any{"project": "fixture-project"}, Secrets: map[string]string{"credential": "three"}},
	}
	job := store.SMSJob{Recipient: "+8613800138000", Body: "fixture-body", ExpiresAt: time.Now().Add(time.Minute)}
	evidence, err := SendSMSWithEvidence(context.Background(), runtime, job)
	if err != nil || evidence.Outcome != "accepted" {
		t.Fatal("new SMS provider was constrained to built-in endpoint schema", evidence, err)
	}
	job.Body = "rejected"
	evidence, err = SendSMSWithEvidence(context.Background(), runtime, job)
	if err == nil || err.Error() != "SMS delivery failed" || evidence.Outcome != "provider_failed" {
		t.Fatal("provider error was not redacted", evidence, err)
	}
	runtime.Enabled = false
	evidence, err = SendSMSWithEvidence(context.Background(), runtime, job)
	if err == nil || evidence.Outcome != "configuration_rejected" {
		t.Fatal("disabled channel was accepted", evidence, err)
	}
}

func TestSMTPFactoryValidatesBeforeConnectingAndUsesNamedPassword(t *testing.T) {
	values := map[string]any{"host": "smtp.example.test", "port": 587, "from": "sender@example.test", "username": "mailer", "tls_mode": "starttls"}
	provider, err := NewMailProvider("smtp", module.Config{Values: values, Secret: "legacy", Secrets: map[string]string{"password": "named"}})
	if err != nil || provider.(SMTPConfig).Password != "named" {
		t.Fatal("named SMTP password was not applied", err)
	}
	for _, invalid := range []any{0, 65536, 587.5} {
		values["port"] = invalid
		if _, err := NewMailProvider("smtp", module.Config{Values: values, Secret: "secret"}); err == nil {
			t.Fatal("invalid SMTP port accepted", invalid)
		}
	}
	values["port"], values["tls_mode"] = 587, "none"
	if _, err := NewMailProvider("smtp", module.Config{Values: values, Secret: "secret"}); err == nil {
		t.Fatal("plaintext authenticated SMTP was accepted")
	}
}
