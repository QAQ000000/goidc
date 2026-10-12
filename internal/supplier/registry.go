package supplier

import (
	"errors"
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
		Provider{ID: ProviderZJMF, Name: "魔方财务", Capabilities: Capabilities{Catalog: true, Product: true, Create: true, Sync: true, Power: true, ResetPassword: true, Rescue: true, VNC: true, Reinstall: true}},
		channel.Descriptor{
			ID: "zjmf", Name: "魔方财务", Category: channel.Provision,
			Capabilities: []string{"catalog", "create", "sync", "power", "reset_password", "rescue", "vnc", "reinstall"},
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
		ID: "nokvm", Name: "NOKVM", Category: channel.Provision,
		Capabilities: []string{"create", "sync", "power", "reset_password", "rescue", "vnc", "reinstall"},
		Fields:       []channel.Field{{Key: "profile", Label: "部署配置", Type: channel.Text, Required: true, Secret: true}},
	})
}

func New(account Account) (Adapter, error) {
	providerState.RLock()
	descriptorID, ok := providerState.descriptor[account.Provider]
	providerState.RUnlock()
	if !ok {
		return nil, &Error{Code: "unsupported_provider"}
	}
	return adapterRegistry.New(descriptorID, module.Config{Payload: account})
}

func Providers() []Provider {
	providerState.RLock()
	result := make([]Provider, 0, len(providerState.metadata))
	for _, provider := range providerState.metadata {
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
