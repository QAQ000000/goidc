package captcha

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"regexp"
	"sort"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
)

const (
	BuiltinProfileID        = "builtin"
	BuiltinImplementationID = "builtin_png"
)

var providers = module.NewRegistry[Provider](channel.Captcha)

func init() {
	if err := Register(channel.Descriptor{
		ID: BuiltinImplementationID, Name: "内置图片验证码", Category: channel.Captcha,
		Capabilities: []string{"image"}, SchemaVersion: 1,
		ContractVersion: channel.ContractVersion, InstanceMode: "profile",
	}, func(module.Config) (Provider, error) { return PNG(), nil }); err != nil {
		panic(err)
	}
}

// Register links a provider to the binary. Deployment configuration can select
// this factory but can never introduce executable code.
func Register(desc channel.Descriptor, factory module.Factory[Provider]) error {
	if desc.SchemaVersion != 1 || desc.InstanceMode != "profile" || factory == nil {
		return ErrConfiguration
	}
	return providers.Register(desc, func(cfg module.Config) (Provider, error) {
		normalized, err := normalizeConfiguration(desc.ID, cfg)
		if err != nil {
			return nil, ErrConfiguration
		}
		provider, err := factory(normalized)
		if err != nil || nilProvider(provider) {
			return nil, ErrConfiguration
		}
		return provider, nil
	})
}

func nilProvider(provider Provider) bool {
	if provider == nil {
		return true
	}
	v := reflect.ValueOf(provider)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func normalizeConfiguration(id string, cfg module.Config) (module.Config, error) {
	entry, ok := providers.Lookup(id)
	if !ok || entry.Descriptor.SchemaVersion != 1 || entry.Descriptor.InstanceMode != "profile" || cfg.Secret != "" || cfg.Payload != nil || cfg.PublicOrigin != "" {
		return module.Config{}, ErrConfiguration
	}
	allowed := map[string]bool{}
	for _, key := range channel.SecretKeys(entry.Descriptor) {
		allowed[key] = true
	}
	secrets := make(map[string]string, len(cfg.Secrets))
	configured := make(map[string]bool, len(cfg.Secrets))
	for key, secret := range cfg.Secrets {
		if !allowed[key] {
			return module.Config{}, ErrConfiguration
		}
		secrets[key] = secret
		configured[key] = secret != ""
	}
	values, issues := channel.ValidateConfiguration(entry.Descriptor, cfg.Values, configured, true)
	if len(issues) != 0 {
		return module.Config{}, ErrConfiguration
	}
	identity, err := json.Marshal(struct {
		Values  map[string]any
		Secrets map[string]string
	}{values, secrets})
	if err != nil || len(identity) > 64*1024 {
		return module.Config{}, ErrConfiguration
	}
	// Canonical serialization also rejects mutable Go-only configuration such
	// as functions, nonfinite floats, and cycles before it reaches a factory.
	raw, err := json.Marshal(values)
	if err != nil || len(raw) > 64*1024 {
		return module.Config{}, ErrConfiguration
	}
	var cloned map[string]any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return module.Config{}, ErrConfiguration
	}
	return module.Config{Values: cloned, Secrets: secrets}, nil
}

func New(id string, cfg module.Config) (Provider, error) {
	normalized, err := normalizeConfiguration(id, cfg)
	if err != nil {
		return nil, ErrConfiguration
	}
	provider, err := providers.New(id, normalized)
	if err != nil || nilProvider(provider) {
		return nil, ErrConfiguration
	}
	return provider, nil
}

type Profile struct {
	ID               string            `json:"id"`
	ImplementationID string            `json:"implementation_id"`
	Config           map[string]any    `json:"config"`
	Secrets          map[string]string `json:"secrets"`
}

// Binding is safe to persist with the challenge. Fingerprint is a digest of
// the resolved implementation and normalized config, including credentials;
// it prevents a pending challenge from silently switching verifier on rotation.
type Binding struct {
	ProfileID        string
	ImplementationID string
	Fingerprint      string
}

type managedProvider struct {
	provider         Provider
	binding          Binding
	secretConfigured bool
}

// Manager is immutable after startup and retains the built-in implementation
// even when a deployment selects a different active profile.
type Manager struct {
	activeID string
	profiles map[string]managedProvider
}

var profileID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func NewManager(profiles []Profile, active string) (*Manager, error) {
	if active == "" {
		active = BuiltinProfileID
	}
	if !profileID.MatchString(active) {
		return nil, ErrConfiguration
	}
	m := &Manager{activeID: active, profiles: make(map[string]managedProvider, len(profiles)+1)}
	all := make([]Profile, 0, len(profiles)+1)
	all = append(all, Profile{ID: BuiltinProfileID, ImplementationID: BuiltinImplementationID})
	all = append(all, profiles...)
	for _, profile := range all {
		if !profileID.MatchString(profile.ID) || !profileID.MatchString(profile.ImplementationID) {
			return nil, ErrConfiguration
		}
		if _, exists := m.profiles[profile.ID]; exists {
			return nil, ErrConfiguration
		}
		cfg, err := normalizeConfiguration(profile.ImplementationID, module.Config{Values: profile.Config, Secrets: profile.Secrets})
		if err != nil {
			return nil, ErrConfiguration
		}
		identity, err := json.Marshal(struct {
			Implementation string
			Values         map[string]any
			Secrets        map[string]string
		}{profile.ImplementationID, cfg.Values, cfg.Secrets})
		if err != nil {
			return nil, ErrConfiguration
		}
		digest := sha256.Sum256(identity)
		provider, err := providers.New(profile.ImplementationID, cfg)
		if err != nil || nilProvider(provider) {
			return nil, ErrConfiguration
		}
		secretConfigured := false
		for _, secret := range cfg.Secrets {
			if secret != "" {
				secretConfigured = true
			}
		}
		m.profiles[profile.ID] = managedProvider{provider: provider, binding: Binding{ProfileID: profile.ID, ImplementationID: profile.ImplementationID, Fingerprint: hex.EncodeToString(digest[:])}, secretConfigured: secretConfigured}
	}
	if _, exists := m.profiles[active]; !exists {
		return nil, ErrConfiguration
	}
	return m, nil
}

func (m *Manager) Active() (Binding, Provider, error) {
	if m == nil {
		return Binding{}, nil, ErrConfiguration
	}
	profile, exists := m.profiles[m.activeID]
	if !exists {
		return Binding{}, nil, ErrConfiguration
	}
	return profile.binding, profile.provider, nil
}

func (m *Manager) Resolve(binding Binding) (Provider, error) {
	if m == nil {
		return nil, ErrConfiguration
	}
	profile, exists := m.profiles[binding.ProfileID]
	if !exists || profile.binding.ImplementationID != binding.ImplementationID {
		return nil, ErrConfiguration
	}
	// Rows issued before modularization carry no fingerprint and can resolve
	// only to the reserved, credential-free built-in provider.
	if binding.ProfileID == BuiltinProfileID && binding.ImplementationID == BuiltinImplementationID && binding.Fingerprint == "" {
		return profile.provider, nil
	}
	if len(binding.Fingerprint) != 64 || subtle.ConstantTimeCompare([]byte(binding.Fingerprint), []byte(profile.binding.Fingerprint)) != 1 {
		return nil, ErrConfiguration
	}
	return profile.provider, nil
}

// Summary exposes deployment membership without configuration, fingerprints or credentials.
type Summary struct {
	ID, ImplementationID     string
	Active, SecretConfigured bool
}

func (m *Manager) Summaries() []Summary {
	out := make([]Summary, 0)
	if m == nil {
		return out
	}
	for id, profile := range m.profiles {
		out = append(out, Summary{ID: id, ImplementationID: profile.binding.ImplementationID, Active: id == m.activeID, SecretConfigured: profile.secretConfigured})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
