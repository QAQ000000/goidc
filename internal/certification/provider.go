package certification

import (
	"context"
	"errors"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
)

type Submission struct {
	Type    string
	Payload map[string]any
}

type Result struct {
	Status          string
	ProviderSubject string
	Message         string
}

type Provider interface {
	Submit(context.Context, Submission) (Result, error)
	Query(context.Context, string) (Result, error)
}

var registry = module.NewRegistry[Provider](channel.Certification)

func Register(id string, provider Provider) error {
	return RegisterDescriptor(channel.Descriptor{ID: id, Name: id, Category: channel.Certification}, provider)
}

func RegisterDescriptor(desc channel.Descriptor, provider Provider) error {
	if desc.ID == "" || provider == nil || desc.Category != channel.Certification {
		return errors.New("invalid certification provider")
	}
	return registry.Register(desc, func(module.Config) (Provider, error) { return provider, nil })
}

func Lookup(id string) (Provider, bool) {
	p, err := registry.New(id, module.Config{})
	return p, err == nil
}

type manualProvider struct{}

func (manualProvider) Submit(context.Context, Submission) (Result, error) {
	return Result{Status: "processing", Message: "等待人工审核"}, nil
}
func (manualProvider) Query(context.Context, string) (Result, error) {
	return Result{Status: "processing", Message: "等待人工审核"}, nil
}

func init() {
	_ = RegisterDescriptor(channel.Descriptor{ID: "manual", Name: "人工实名认证", Category: channel.Certification, Capabilities: []string{"submit", "review"}}, manualProvider{})
}
