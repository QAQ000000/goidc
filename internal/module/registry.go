// Package module contains the common registry used by compiled-in adapters.
//
// A registry is deliberately a build-time composition point: implementations
// are linked into the binary and register a typed factory during startup.
// Database rows may select and configure an implementation, but they never
// contain executable code or a path to load at runtime.
package module

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/codex/zjmfgo/internal/channel"
)

// Config is the boundary between persisted instance configuration and a
// compiled-in factory. Values contains non-secret fields; Secret is reserved
// for the decrypted credential and must never be persisted by a factory.
type Config struct {
	Values       map[string]any
	Secret       string
	PublicOrigin string
	// Payload is an optional category-specific value. The generic registry
	// does not inspect it; typed category packages validate it at the factory
	// boundary when they need richer configuration than Values can express.
	Payload any
}

type Factory[T any] func(Config) (T, error)

type Entry[T any] struct {
	Descriptor channel.Descriptor
}

// Registry keeps behavior registration typed while sharing the same channel
// descriptor catalogue exposed to the administration UI.
type Registry[T any] struct {
	category channel.Category
}

func NewRegistry[T any](category channel.Category) *Registry[T] {
	return &Registry[T]{category: category}
}

func (r *Registry[T]) Register(desc channel.Descriptor, factory Factory[T]) error {
	if r == nil || factory == nil || desc.Category != r.category {
		return errors.New("invalid module registration")
	}
	desc.ID = strings.TrimSpace(desc.ID)
	desc.Name = strings.TrimSpace(desc.Name)
	if desc.ID == "" || desc.Name == "" {
		return errors.New("invalid module descriptor")
	}
	return channel.RegisterFactory(desc, func(raw any) (any, error) {
		cfg, ok := raw.(Config)
		if !ok {
			return nil, errors.New("invalid module factory configuration")
		}
		return factory(cfg)
	})
}

func (r *Registry[T]) MustRegister(desc channel.Descriptor, factory Factory[T]) {
	if err := r.Register(desc, factory); err != nil {
		panic(err)
	}
}

func (r *Registry[T]) New(id string, cfg Config) (T, error) {
	value, err := channel.New(r.category, strings.TrimSpace(id), cfg)
	if err != nil {
		var zero T
		return zero, err
	}
	out, ok := value.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("module factory returned unexpected type for %s", strings.TrimSpace(id))
	}
	return out, nil
}

func (r *Registry[T]) Lookup(id string) (Entry[T], bool) {
	if !channel.HasFactory(r.category, strings.TrimSpace(id)) {
		return Entry[T]{}, false
	}
	desc, ok := channel.Lookup(r.category, strings.TrimSpace(id))
	if !ok {
		return Entry[T]{}, false
	}
	return Entry[T]{Descriptor: desc}, true
}

func (r *Registry[T]) IDs() []string {
	items := channel.List(r.category)
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return ids
}
