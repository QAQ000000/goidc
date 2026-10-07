package supplier

import "sort"

type registration struct {
	provider Provider
	factory  func(Account) (Adapter, error)
}

// Register additional protocols here; business code consumes normalized DTOs.
var providers = map[string]registration{
	ProviderZJMF: {
		provider: Provider{ID: ProviderZJMF, Name: "魔方财务", Capabilities: Capabilities{Catalog: true, Product: true, Create: true, Sync: true, Power: true, ResetPassword: true, Rescue: true, VNC: true, Reinstall: true}},
		factory:  newZJMF,
	},
}

func New(account Account) (Adapter, error) {
	p, ok := providers[account.Provider]
	if !ok {
		return nil, &Error{Code: "unsupported_provider"}
	}
	return p.factory(account)
}

func Providers() []Provider {
	result := make([]Provider, 0, len(providers))
	for _, p := range providers {
		result = append(result, p.provider)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
