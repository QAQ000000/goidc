package supplier

import (
	"sort"

	"github.com/codex/zjmfgo/internal/channel"
)

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

func init() {
	channel.MustRegister(channel.Descriptor{
		ID: "zjmf", Name: "魔方财务", Category: channel.Provision,
		Capabilities: []string{"catalog", "create", "sync", "power", "reset_password", "rescue", "vnc", "reinstall"},
		Fields: []channel.Field{
			{Key: "endpoint", Label: "API 地址", Type: channel.Text, Required: true},
			{Key: "username", Label: "用户名", Type: channel.Text, Required: true},
			{Key: "secret", Label: "SecretKey", Type: channel.Text, Required: true, Secret: true},
		},
	})
	channel.MustRegister(channel.Descriptor{
		ID: "nokvm", Name: "NOKVM", Category: channel.Provision,
		Capabilities: []string{"create", "sync", "power", "reset_password", "rescue", "vnc", "reinstall"},
		Fields:       []channel.Field{{Key: "profile", Label: "部署配置", Type: channel.Text, Required: true, Secret: true}},
	})
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
