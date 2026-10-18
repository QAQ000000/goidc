package certification

import (
	"context"
	"errors"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
)

type Submission struct {
	// RequestID is a stable, host-generated idempotency identity for this
	// submission. It must not contain identity documents or other personal data.
	RequestID string
	Type      string
	Payload   map[string]any
}

type Query struct {
	RequestID       string
	ProviderSubject string
}

type Result struct {
	Status          string
	ProviderSubject string
	Message         string
}

// Provider implements only the external protocol. Factories validate and build
// an instance without network access; Submit and Query run outside database
// transactions. An error or an ambiguous external result is not a rejection.
// Hosts must not blindly replay Submit after an unknown outcome: reconciliation
// uses Query only when the descriptor declares query (with a known subject) or
// query_by_request_id (when the original request has no known subject).
type Provider interface {
	Submit(context.Context, Submission) (Result, error)
	Query(context.Context, Query) (Result, error)
}

var registry = module.NewRegistry[Provider](channel.Certification)

func Register(id string, provider Provider) error {
	return RegisterDescriptor(channel.Descriptor{ID: id, Name: id, Category: channel.Certification}, provider)
}

// RegisterDescriptor retains the stateless singleton registration boundary.
// Configurable implementations must use RegisterFactory instead.
func RegisterDescriptor(desc channel.Descriptor, provider Provider) error {
	if desc.ID == "" || provider == nil || desc.Category != channel.Certification {
		return errors.New("invalid certification provider")
	}
	return RegisterFactory(desc, func(module.Config) (Provider, error) { return provider, nil })
}

// RegisterFactory registers a compiled implementation. The factory receives
// the resolved instance configuration and must return its own provider rather
// than sharing credentials or mutable state between configured instances.
func RegisterFactory(desc channel.Descriptor, factory module.Factory[Provider]) error {
	return registry.Register(desc, factory)
}

func New(id string, config module.Config) (Provider, error) {
	return registry.New(id, config)
}

func Lookup(id string) (Provider, bool) {
	p, err := New(id, module.Config{})
	return p, err == nil
}

type manualProvider struct{}

func (manualProvider) Submit(context.Context, Submission) (Result, error) {
	return Result{Status: "processing", Message: "等待人工审核"}, nil
}
func (manualProvider) Query(context.Context, Query) (Result, error) {
	return Result{Status: "processing", Message: "等待人工审核"}, nil
}

func init() {
	_ = RegisterFactory(channel.Descriptor{ID: "manual", Name: "人工实名认证", Category: channel.Certification, Capabilities: []string{"submit", "review"}}, func(module.Config) (Provider, error) {
		return manualProvider{}, nil
	})
}
