package certification_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/certification"
	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
)

var fixtureSequence atomic.Int64

func fixtureDescriptor() channel.Descriptor {
	return channel.Descriptor{
		ID: fmt.Sprintf("certification_fixture_%d", fixtureSequence.Add(1)), Name: "Certification fixture",
		Category: channel.Certification, Capabilities: []string{"submit", "query", "query_by_request_id"},
	}
}

type configuredProvider struct {
	tenant, secret, origin string
	submissions            []certification.Submission
	queries                []certification.Query
}

func (p *configuredProvider) Submit(_ context.Context, submission certification.Submission) (certification.Result, error) {
	p.submissions = append(p.submissions, submission)
	return certification.Result{Status: "processing", ProviderSubject: p.tenant + ":" + submission.RequestID}, nil
}

func (p *configuredProvider) Query(_ context.Context, query certification.Query) (certification.Result, error) {
	p.queries = append(p.queries, query)
	return certification.Result{Status: "processing", ProviderSubject: query.ProviderSubject}, nil
}

func TestFactoryBuildsIndependentConfiguredProviders(t *testing.T) {
	desc := fixtureDescriptor()
	builds := 0
	if err := certification.RegisterFactory(desc, func(config module.Config) (certification.Provider, error) {
		builds++
		return &configuredProvider{tenant: config.Values["tenant"].(string), secret: config.Secrets["api_key"], origin: config.PublicOrigin}, nil
	}); err != nil {
		t.Fatal(err)
	}
	a, err := certification.New(desc.ID, module.Config{Values: map[string]any{"tenant": "tenant-a"}, Secrets: map[string]string{"api_key": "fixture-a"}, PublicOrigin: "https://a.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := certification.New(desc.ID, module.Config{Values: map[string]any{"tenant": "tenant-b"}, Secrets: map[string]string{"api_key": "fixture-b"}, PublicOrigin: "https://b.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	first, second := a.(*configuredProvider), b.(*configuredProvider)
	if builds != 2 || first == second || first.tenant != "tenant-a" || first.secret != "fixture-a" || first.origin != "https://a.example.test" || second.tenant != "tenant-b" || second.secret != "fixture-b" || second.origin != "https://b.example.test" {
		t.Fatal("factory did not receive separate instance configuration")
	}
	request := certification.Submission{RequestID: "submission-a", Type: "individual", Payload: map[string]any{"name": "Fixture applicant"}}
	result, err := a.Submit(t.Context(), request)
	if err != nil || result.Status != "processing" || result.ProviderSubject != "tenant-a:submission-a" {
		t.Fatalf("submit result=%#v err=%v", result, err)
	}
	query := certification.Query{RequestID: request.RequestID, ProviderSubject: result.ProviderSubject}
	if _, err = a.Query(t.Context(), query); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.submissions, []certification.Submission{request}) || !reflect.DeepEqual(first.queries, []certification.Query{query}) || len(second.submissions) != 0 || len(second.queries) != 0 {
		t.Fatal("submission/query identity or provider state crossed instance boundaries")
	}
}

func TestFactoryErrorsAndMissingImplementationsRemainErrors(t *testing.T) {
	desc := fixtureDescriptor()
	rejected := errors.New("fixture configuration rejected")
	if err := certification.RegisterFactory(desc, func(module.Config) (certification.Provider, error) { return nil, rejected }); err != nil {
		t.Fatal(err)
	}
	if provider, err := certification.New(desc.ID, module.Config{}); provider != nil || !errors.Is(err, rejected) {
		t.Fatalf("factory failure was hidden: provider=%T err=%v", provider, err)
	}
	if _, ok := certification.Lookup(desc.ID); ok {
		t.Fatal("legacy lookup accepted a failed factory")
	}
	for _, id := range []string{fixtureDescriptor().ID, ""} {
		if provider, err := certification.New(id, module.Config{}); provider != nil || !errors.Is(err, channel.ErrFactoryUnavailable) {
			t.Fatalf("missing factory accepted: provider=%T err=%v", provider, err)
		}
	}
	descriptorOnly := fixtureDescriptor()
	if err := channel.Register(descriptorOnly); err != nil {
		t.Fatal(err)
	}
	if provider, err := certification.New(descriptorOnly.ID, module.Config{}); provider != nil || !errors.Is(err, channel.ErrFactoryUnavailable) {
		t.Fatalf("descriptor without factory accepted: provider=%T err=%v", provider, err)
	}
	if provider, ok := certification.Lookup(descriptorOnly.ID); provider != nil || ok {
		t.Fatal("legacy lookup accepted a descriptor without a factory")
	}
	if err := certification.RegisterFactory(fixtureDescriptor(), nil); err == nil {
		t.Fatal("nil factory accepted")
	}
	wrongCategory := fixtureDescriptor()
	wrongCategory.Category = channel.Mail
	if err := certification.RegisterFactory(wrongCategory, func(module.Config) (certification.Provider, error) { return &configuredProvider{}, nil }); err == nil {
		t.Fatal("factory registered in the wrong category")
	}
}

func TestLegacyRegistrationAndManualContractRemainCompatible(t *testing.T) {
	for _, register := range []func(channel.Descriptor, certification.Provider) error{
		certification.RegisterDescriptor,
		func(desc channel.Descriptor, provider certification.Provider) error {
			return certification.Register(desc.ID, provider)
		},
	} {
		desc := fixtureDescriptor()
		provider := &configuredProvider{tenant: "legacy"}
		if err := register(desc, provider); err != nil {
			t.Fatal(err)
		}
		if got, ok := certification.Lookup(desc.ID); !ok || got != provider {
			t.Fatal("legacy singleton lookup changed")
		}
	}
	if err := certification.Register("nil_fixture", nil); err == nil {
		t.Fatal("nil legacy provider accepted")
	}
	desc, ok := channel.Lookup(channel.Certification, "manual")
	if !ok || desc.SchemaVersion != 1 || desc.ContractVersion != 1 || desc.Version != "builtin" || desc.InstanceMode != "channel" || len(desc.Fields) != 0 || !reflect.DeepEqual(desc.Capabilities, []string{"submit", "review"}) {
		t.Fatalf("manual descriptor changed: %#v", desc)
	}
	provider, ok := certification.Lookup("manual")
	if !ok {
		t.Fatal("manual provider missing")
	}
	want := certification.Result{Status: "processing", Message: "等待人工审核"}
	result, err := provider.Submit(t.Context(), certification.Submission{RequestID: "manual-submission", Type: "individual"})
	if err != nil || result != want {
		t.Fatalf("manual submit=%#v err=%v", result, err)
	}
	result, err = provider.Query(t.Context(), certification.Query{RequestID: "manual-submission"})
	if err != nil || result != want {
		t.Fatalf("manual query=%#v err=%v", result, err)
	}
}
