package module

import (
	"errors"
	"testing"

	"github.com/codex/zjmfgo/internal/channel"
)

type fixtureModule struct{ ID string }

func TestRegistryRegistersDescriptorAndBuildsTypedImplementation(t *testing.T) {
	r := NewRegistry[fixtureModule](channel.Category("module-test"))
	desc := channel.Descriptor{ID: "fixture", Name: "Fixture", Category: channel.Category("module-test")}
	r.MustRegister(desc, func(cfg Config) (fixtureModule, error) {
		id, ok := cfg.Values["id"].(string)
		if !ok {
			return fixtureModule{}, errors.New("id is required")
		}
		return fixtureModule{ID: id}, nil
	})
	got, err := r.New("fixture", Config{Values: map[string]any{"id": "ok"}})
	if err != nil || got.ID != "ok" {
		t.Fatalf("factory result = %#v, %v", got, err)
	}
	if _, err = r.New("missing", Config{}); err == nil {
		t.Fatal("missing implementation accepted")
	}
	if _, err = r.New("fixture", Config{}); err == nil || err.Error() != "id is required" {
		t.Fatalf("factory validation error = %v", err)
	}
	if _, ok := r.Lookup("fixture"); !ok {
		t.Fatal("registered implementation missing from lookup")
	}
}

func TestRegistryRejectsDuplicateDescriptor(t *testing.T) {
	r := NewRegistry[fixtureModule](channel.Category("module-test-duplicates"))
	desc := channel.Descriptor{ID: "fixture", Name: "Fixture", Category: channel.Category("module-test-duplicates")}
	factory := func(Config) (fixtureModule, error) { return fixtureModule{}, nil }
	if err := r.Register(desc, factory); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(desc, factory); err == nil {
		t.Fatal("duplicate implementation accepted")
	}
}
