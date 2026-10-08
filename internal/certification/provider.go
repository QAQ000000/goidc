package certification

import (
	"context"
	"errors"
	"sync"

	"github.com/codex/zjmfgo/internal/channel"
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

var registry = struct {
	sync.RWMutex
	items map[string]Provider
}{items: map[string]Provider{}}

func Register(id string, provider Provider) error {
	if id == "" || provider == nil {
		return errors.New("invalid certification provider")
	}
	registry.Lock()
	defer registry.Unlock()
	if _, ok := registry.items[id]; ok {
		return errors.New("certification provider already registered")
	}
	registry.items[id] = provider
	return nil
}

func Lookup(id string) (Provider, bool) {
	registry.RLock()
	p, ok := registry.items[id]
	registry.RUnlock()
	return p, ok
}

type manualProvider struct{}

func (manualProvider) Submit(context.Context, Submission) (Result, error) {
	return Result{Status: "processing", Message: "等待人工审核"}, nil
}
func (manualProvider) Query(context.Context, string) (Result, error) {
	return Result{Status: "processing", Message: "等待人工审核"}, nil
}

func init() {
	_ = Register("manual", manualProvider{})
	_ = channel.Register(channel.Descriptor{ID: "manual", Name: "人工实名认证", Category: channel.Certification, Capabilities: []string{"submit", "review"}})
}
