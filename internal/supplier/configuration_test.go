package supplier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/channel"
)

type configurationAdapter struct{ caps Capabilities }

func (a *configurationAdapter) Capabilities() Capabilities                    { return a.caps }
func (*configurationAdapter) Catalog(context.Context) ([]Product, error)      { return nil, nil }
func (*configurationAdapter) Product(context.Context, int64) (Product, error) { return Product{}, nil }
func (*configurationAdapter) Create(context.Context, CreateRequest) (CreateResult, error) {
	return CreateResult{}, nil
}
func (*configurationAdapter) Sync(context.Context, int64) (Service, error) { return Service{}, nil }
func (*configurationAdapter) Execute(context.Context, ActionRequest) (Service, error) {
	return Service{}, nil
}

func configurationDefinition() (Provider, channel.Descriptor) {
	id := fmt.Sprintf("supplier_configuration_%d", registryFixtureID.Add(1))
	p := Provider{ID: id, Name: "Configuration fixture", IdentityFields: []string{"realm", "account"}, Capabilities: Capabilities{Catalog: true}}
	d := channel.Descriptor{ID: id, Name: p.Name, Category: channel.Provision, SchemaVersion: 2, Fields: []channel.Field{
		{Key: "realm", Label: "Realm", Type: channel.Text, Required: true},
		{Key: "account", Label: "Account", Type: channel.Text, Required: true},
		{Key: "timeout", Label: "Timeout", Type: channel.Number, Default: 0, Integer: true},
		{Key: "enabled", Label: "Enabled", Type: channel.Boolean, Default: false},
		{Key: "tags", Label: "Tags", Type: channel.MultiSelect, Options: []string{"a", "b"}, Default: []string{}},
		{Key: "read_key", Label: "Read key", Type: channel.Text, Required: true, Secret: true},
		{Key: "write_key", Label: "Write key", Type: channel.Text, Required: true, Secret: true},
	}}
	return p, d
}

func registerConfigurationFixture(t *testing.T, p Provider, d channel.Descriptor, factory ProviderFactory) error {
	t.Helper()
	if err := Register(p, d, factory); err != nil {
		return err
	}
	t.Cleanup(func() {
		providerState.Lock()
		delete(providerState.metadata, p.ID)
		delete(providerState.descriptor, p.ID)
		providerState.Unlock()
	})
	return nil
}

func TestSupplierConfigurationFactoryAndStableIdentity(t *testing.T) {
	p, d := configurationDefinition()
	var captured Account
	if err := registerConfigurationFixture(t, p, d, func(a Account) (Adapter, error) {
		captured = a
		return &configurationAdapter{caps: p.Capabilities}, nil
	}); err != nil {
		t.Fatal(err)
	}
	a := Account{ID: 17, Provider: p.ID, SchemaVersion: 2, Values: map[string]any{"realm": "north", "account": "merchant"}, Secrets: map[string]string{"read_key": "private-reader", "write_key": "private-writer"}}
	if _, err := New(a); err != nil {
		t.Fatal(err)
	}
	if captured.ID != 17 || captured.SchemaVersion != 2 || captured.Values["enabled"] != false || captured.Values["timeout"] != float64(0) || len(captured.Values["tags"].([]string)) != 0 || captured.Secrets["write_key"] != "private-writer" {
		t.Fatal("factory lost explicit defaults, identity or independent credentials")
	}
	identity, err := AccountIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Values["timeout"], a.Secrets["write_key"] = 90, "rotated-writer"
	if other, err := AccountIdentity(a); err != nil || identity != other {
		t.Fatal("optional settings or credential rotation created another account identity", err)
	}
	a.Values["realm"] = "south"
	if other, err := AccountIdentity(a); err != nil || identity == other {
		t.Fatal("different upstream accounts share a lease identity", err)
	}
	raw, _ := json.Marshal(captured)
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "merchant") {
		t.Fatal("runtime configuration leaked into public Account JSON")
	}
	for _, mutate := range []func(*Account){
		func(a *Account) { a.SchemaVersion = 1 },
		func(a *Account) {
			a.Values = map[string]any{"realm": "north", "account": "merchant", "read_key": "private-reader"}
		},
		func(a *Account) { a.Secrets = map[string]string{"read_key": "only-reader"} },
		func(a *Account) { a.Secrets = map[string]string{"unknown_key": "private-unknown"} },
		func(a *Account) { a.BaseURL = "https://unrelated.invalid" },
	} {
		invalid := a
		mutate(&invalid)
		if _, err := New(invalid); err == nil {
			t.Fatal("unsafe or incomplete factory configuration accepted")
		}
	}
}

func TestSupplierRegistrationRejectsUnsafeIdentityAndCapabilityMetadata(t *testing.T) {
	for _, mode := range []string{"missing", "secret", "optional", "duplicate", "capability", "wrong_mode"} {
		t.Run(mode, func(t *testing.T) {
			p, d := configurationDefinition()
			switch mode {
			case "missing":
				p.IdentityFields = nil
			case "secret":
				p.IdentityFields = []string{"read_key"}
			case "optional":
				p.IdentityFields = []string{"timeout"}
			case "duplicate":
				p.IdentityFields = []string{"realm", "realm"}
			case "capability":
				d.Capabilities = []string{"create"}
			case "wrong_mode":
				d.InstanceMode = "channel"
			}
			if err := Register(p, d, func(Account) (Adapter, error) { return &configurationAdapter{caps: p.Capabilities}, nil }); err == nil {
				t.Fatal("unsafe supplier registration accepted")
			}
			if _, ok := Lookup(p.ID); ok {
				t.Fatal("rejected registration polluted provider catalogue")
			}
		})
	}
}

func TestSupplierFactoryRejectsNilDriftAndSecretBearingErrors(t *testing.T) {
	for _, mode := range []string{"nil", "typed_nil", "drift", "error", "missing_renewal_interface"} {
		t.Run(mode, func(t *testing.T) {
			p, d := configurationDefinition()
			if mode == "missing_renewal_interface" {
				p.Capabilities.Renew = true
			}
			err := registerConfigurationFixture(t, p, d, func(Account) (Adapter, error) {
				switch mode {
				case "nil":
					return nil, nil
				case "typed_nil":
					return (*configurationAdapter)(nil), nil
				case "drift":
					return &configurationAdapter{}, nil
				case "error":
					return nil, errors.New("remote private-credential")
				default:
					return &configurationAdapter{caps: p.Capabilities}, nil
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = New(Account{Provider: p.ID, Values: map[string]any{"realm": "r", "account": "a"}, Secrets: map[string]string{"read_key": "read", "write_key": "write"}})
			if err == nil || strings.Contains(err.Error(), "private-credential") {
				t.Fatal("invalid factory result or sensitive error escaped", err)
			}
		})
	}
}

func TestSupplierLegacyAndGenericAccountIdentityStayCompatible(t *testing.T) {
	legacy := Account{Provider: ProviderZJMF, BaseURL: "https://UPSTREAM.invalid:443/api/", Username: " account ", Password: "legacy-private-password"}
	generic := Account{Provider: ProviderZJMF, SchemaVersion: 1, Values: map[string]any{"endpoint": "https://upstream.invalid/api", "username": "account"}, Secrets: map[string]string{"secret": "generic-private-password"}}
	one, err := AccountIdentity(legacy)
	if err != nil {
		t.Fatal(err)
	}
	two, err := AccountIdentity(generic)
	if err != nil || one != two {
		t.Fatal("legacy and named accounts could bypass the shared lease", err)
	}
	for _, a := range []Account{legacy, generic} {
		adapter, err := New(a)
		if err != nil {
			t.Fatal(err)
		}
		if adapter.(*zjmf).account.BaseURL != "https://upstream.invalid/api" {
			t.Fatal("endpoint aliases were not normalized")
		}
	}
	generic.Values["endpoint"] = "https://upstream.invalid/%61pi/"
	if other, err := AccountIdentity(generic); err != nil || other != one {
		t.Fatal("escaped endpoint bypassed identity", err)
	}
	generic.BaseURL = "https://other.invalid/api"
	if _, err := New(generic); err == nil {
		t.Fatal("contradictory legacy aliases accepted")
	}
	for _, aliases := range [][2]string{
		{"https://[::1]/api", "https://[0:0:0:0:0:0:0:1]:0443/api/"},
		{"http://127.0.0.1/api", "http://[::ffff:127.0.0.1]:080/api"},
		{"https://upstream.invalid/api", "https://upstream.invalid:0443/api//./"},
	} {
		identity := ""
		for _, endpoint := range aliases {
			value, err := AccountIdentity(Account{Provider: ProviderZJMF, BaseURL: endpoint, Username: "account"})
			if err != nil || identity != "" && value != identity {
				t.Fatal("literal URL aliases bypass shared identity", aliases, err)
			}
			identity = value
		}
	}
}

func TestSupplierAccountNormalizerOwnsCanonicalIdentity(t *testing.T) {
	p, d := configurationDefinition()
	p.NormalizeConfig = func(values map[string]any) (map[string]any, error) {
		values["realm"] = strings.ToLower(values["realm"].(string))
		return values, nil
	}
	if err := registerConfigurationFixture(t, p, d, func(Account) (Adapter, error) { return &configurationAdapter{caps: p.Capabilities}, nil }); err != nil {
		t.Fatal(err)
	}
	a := Account{Provider: p.ID, Values: map[string]any{"realm": "NORTH", "account": "merchant"}}
	one, err := AccountIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Values["realm"] = "north"
	if two, err := AccountIdentity(a); err != nil || one != two {
		t.Fatal("provider canonicalization did not bind equivalent accounts", err)
	}
	for _, mode := range []string{"error", "secret_pollution"} {
		p, d := configurationDefinition()
		p.NormalizeConfig = func(values map[string]any) (map[string]any, error) {
			if mode == "error" {
				return nil, errors.New("private-provider-detail")
			}
			values["read_key"] = "private-provider-detail"
			return values, nil
		}
		if err := registerConfigurationFixture(t, p, d, func(Account) (Adapter, error) { return &configurationAdapter{caps: p.Capabilities}, nil }); err != nil {
			t.Fatal(err)
		}
		_, err := NormalizeAccount(Account{Provider: p.ID, Values: a.Values})
		if err == nil || strings.Contains(err.Error(), "private-provider-detail") {
			t.Fatal("unsafe provider normalizer escaped schema or redaction", err)
		}
	}
}
