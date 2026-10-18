package supplier

import (
	"errors"
	"reflect"
	"slices"
	"sort"
	"sync"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
)

type ProviderFactory func(Account) (Adapter, error)

// Definition binds provider-neutral metadata to the compiled configuration
// schema exposed to administrators. The schema is public metadata only; it
// never contains account values or credentials.
type Definition struct {
	Provider   Provider           `json:"provider"`
	Descriptor channel.Descriptor `json:"descriptor"`
}

var adapterRegistry = module.NewRegistry[Adapter](channel.Provision)

var providerState = struct {
	sync.RWMutex
	metadata   map[string]Provider
	descriptor map[string]string
}{
	metadata:   make(map[string]Provider),
	descriptor: make(map[string]string),
}

// Register adds a provider-neutral adapter and its UI descriptor together.
// Provider IDs are the durable values stored on supplier_accounts; descriptor
// IDs are compiled channel IDs and may stay short for configuration backwards
// compatibility.
func Register(provider Provider, descriptor channel.Descriptor, factory ProviderFactory) error {
	if provider.ID == "" || provider.Name == "" || factory == nil || descriptor.ID == "" {
		return errors.New("invalid supplier registration")
	}
	if descriptor.InstanceMode == "" {
		descriptor.InstanceMode = "supplier"
	}
	if descriptor.InstanceMode != "supplier" {
		return errors.New("supplier adapters require supplier account configuration")
	}
	fields := make(map[string]channel.Field, len(descriptor.Fields))
	for _, field := range descriptor.Fields {
		fields[field.Key] = field
	}
	identity := make(map[string]bool, len(provider.IdentityFields))
	for _, key := range provider.IdentityFields {
		field, ok := fields[key]
		if !ok || field.Secret || !field.Required || identity[key] {
			return errors.New("supplier identity requires unique required public fields")
		}
		identity[key] = true
	}
	if len(descriptor.Fields) > 0 && len(identity) == 0 {
		return errors.New("supplier registration requires stable account identity")
	}
	declared := capabilityNames(provider.Capabilities)
	if len(descriptor.Capabilities) != 0 {
		actual := append([]string(nil), descriptor.Capabilities...)
		sort.Strings(actual)
		if !slices.Equal(actual, declared) {
			return errors.New("supplier capability metadata mismatch")
		}
	}
	descriptor.Capabilities = declared
	provider.IdentityFields = append([]string(nil), provider.IdentityFields...)
	providerState.Lock()
	defer providerState.Unlock()
	if _, exists := providerState.metadata[provider.ID]; exists {
		return errors.New("supplier provider already registered")
	}
	if err := adapterRegistry.Register(descriptor, func(config module.Config) (Adapter, error) {
		account, ok := config.Payload.(Account)
		if !ok {
			return nil, errors.New("invalid supplier factory configuration")
		}
		return factory(account)
	}); err != nil {
		return err
	}
	providerState.metadata[provider.ID] = provider
	providerState.descriptor[provider.ID] = descriptor.ID
	return nil
}

func MustRegister(provider Provider, descriptor channel.Descriptor, factory ProviderFactory) {
	if err := Register(provider, descriptor, factory); err != nil {
		panic(err)
	}
}

func init() {
	MustRegister(
		Provider{ID: ProviderZJMF, Name: "魔方财务", IdentityFields: []string{"endpoint", "username"}, Capabilities: Capabilities{Catalog: true, Product: true, Create: true, Sync: true, Power: true, ResetPassword: true, Rescue: true, VNC: true, Reinstall: true, Renew: true}},
		channel.Descriptor{
			ID: "zjmf", Name: "魔方财务", Category: channel.Provision, InstanceMode: "supplier",
			Fields: []channel.Field{
				{Key: "endpoint", Label: "API 地址", Type: channel.Text, Required: true},
				{Key: "username", Label: "用户名", Type: channel.Text, Required: true},
				{Key: "secret", Label: "SecretKey", Type: channel.Text, Required: true, Secret: true},
			},
		},
		newZJMF,
	)
	// NOKVM still uses its dedicated profile store. Its descriptor is visible
	// in the common catalogue, but its account factory is migrated separately.
	channel.MustRegister(channel.Descriptor{
		ID: "nokvm", Name: "NOKVM", Category: channel.Provision, InstanceMode: "profile",
		Capabilities: []string{"create", "sync", "power", "reset_password", "rescue", "vnc", "reinstall"},
		Fields:       []channel.Field{{Key: "profile", Label: "部署配置", Type: channel.Text, Required: true, Secret: true}},
	})
}

func New(account Account) (Adapter, error) {
	definition, ok := Lookup(account.Provider)
	if !ok {
		return nil, &Error{Code: "unsupported_provider"}
	}
	account, err := NormalizeAccount(account)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]bool)
	configured := make(map[string]bool)
	for _, key := range channel.SecretKeys(definition.Descriptor) {
		keys[key] = true
	}
	for key, value := range account.Secrets {
		if !keys[key] || len(value) > 4096 {
			return nil, &Error{Code: "invalid_configuration"}
		}
		configured[key] = value != ""
	}
	if _, errs := channel.ValidateConfiguration(definition.Descriptor, account.Values, configured, true); len(errs) > 0 || len(channel.ValidateSecretValues(definition.Descriptor, account.Secrets)) > 0 {
		return nil, &Error{Code: "invalid_configuration"}
	}
	adapter, err := adapterRegistry.New(definition.Descriptor.ID, module.Config{Payload: account})
	if err != nil {
		return nil, &Error{Code: "invalid_configuration"}
	}
	if adapter == nil || (reflect.ValueOf(adapter).Kind() == reflect.Pointer && reflect.ValueOf(adapter).IsNil()) || adapter.Capabilities() != definition.Provider.Capabilities {
		return nil, &Error{Code: "invalid_factory"}
	}
	if definition.Provider.Capabilities.Renew {
		if _, ok := adapter.(RenewalAdapter); !ok {
			return nil, &Error{Code: "invalid_factory"}
		}
	}
	if definition.Provider.Capabilities.VNC || definition.Provider.Capabilities.Reinstall {
		if _, ok := adapter.(ConsoleAdapter); !ok {
			return nil, &Error{Code: "invalid_factory"}
		}
	}
	return adapter, nil
}

func capabilityNames(c Capabilities) []string {
	flags := map[string]bool{"catalog": c.Catalog, "product": c.Product, "create": c.Create, "sync": c.Sync, "power": c.Power, "reset_password": c.ResetPassword, "rescue": c.Rescue, "vnc": c.VNC, "reinstall": c.Reinstall, "suspend": c.Suspend, "resume": c.Resume, "terminate": c.Terminate, "renew": c.Renew}
	result := []string{}
	for name, enabled := range flags {
		if enabled {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func Providers() []Provider {
	providerState.RLock()
	result := make([]Provider, 0, len(providerState.metadata))
	for _, provider := range providerState.metadata {
		provider.IdentityFields = append([]string(nil), provider.IdentityFields...)
		result = append(result, provider)
	}
	providerState.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// Definitions returns the compiled provider catalogue together with the
// matching form schema. Database rows are deliberately not consulted here.
func Definitions() []Definition {
	providers := Providers()
	result := make([]Definition, 0, len(providers))
	for _, provider := range providers {
		providerState.RLock()
		descriptorID := providerState.descriptor[provider.ID]
		providerState.RUnlock()
		descriptor, ok := channel.Lookup(channel.Provision, descriptorID)
		if !ok {
			continue
		}
		result = append(result, Definition{Provider: provider, Descriptor: descriptor})
	}
	return result
}
