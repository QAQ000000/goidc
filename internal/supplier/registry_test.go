package supplier

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/channel"
)

var registryFixtureID atomic.Uint64

func TestSupplierRegistrationUsesDedicatedConfiguration(t *testing.T) {
	provider := Provider{ID: fmt.Sprintf("registry_account_mode_fixture_%d", registryFixtureID.Add(1)), Name: "Fixture"}
	providerID := provider.ID
	desc := channel.Descriptor{ID: provider.ID, Name: provider.Name, Category: channel.Provision}
	if err := Register(provider, desc, func(Account) (Adapter, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		providerState.Lock()
		delete(providerState.metadata, providerID)
		delete(providerState.descriptor, providerID)
		providerState.Unlock()
	})
	registered, ok := channel.Lookup(channel.Provision, desc.ID)
	if !ok || registered.InstanceMode != "supplier" {
		t.Fatalf("supplier registered with generic channel configuration: %+v", registered)
	}
	desc.ID, desc.InstanceMode = "registry_wrong_mode_fixture", "channel"
	provider.ID = desc.ID
	if err := Register(provider, desc, func(Account) (Adapter, error) { return nil, nil }); err == nil {
		t.Fatal("supplier adapter accepted a parallel generic configuration store")
	}
}
