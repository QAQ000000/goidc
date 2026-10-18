package payment

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/channel"
)

var paymentFixtureSequence atomic.Uint64

func paymentFixtureID() string {
	return fmt.Sprintf("payment_fixture_%d", paymentFixtureSequence.Add(1))
}

type registeredPaymentFixture struct {
	method string
	secret string
}

func (g *registeredPaymentFixture) Name() string { return g.method }
func (g *registeredPaymentFixture) CreateIntent(IntentRequest) (Intent, error) {
	return Intent{ProviderPaymentID: g.method}, nil
}
func (*registeredPaymentFixture) VerifySignature([]byte, string) bool { return false }
func (*registeredPaymentFixture) DecodeWebhook([]byte) (Webhook, error) {
	return Webhook{}, errors.New("fixture does not settle")
}

func paymentFixtureDescriptor(id string) channel.Descriptor {
	return channel.Descriptor{ID: id, Name: "Payment fixture", Category: channel.Payment}
}

func TestPaymentRegistryOwnsFixedMethodsAndIndependentConfiguration(t *testing.T) {
	id := paymentFixtureID()
	methods := []Method{{ID: id + "_second", Label: "Second"}, {ID: id + "_first", Label: "First"}}
	desc := paymentFixtureDescriptor(id)
	desc.Capabilities = []string{"create_intent", "verify_webhook", "query"}
	if err := RegisterFactory(desc, methods, mockWebhookProtocol{}, func(method string, cfg FactoryConfig) (Gateway, error) {
		return &registeredPaymentFixture{method: method, secret: cfg.Secret}, nil
	}); err != nil {
		t.Fatal(err)
	}
	firstID := methods[1].ID
	methods[1].Label = "mutated caller data"
	listed := Methods(id)
	if len(listed) != 2 || listed[0].ID != firstID || listed[0].Label != "First" {
		t.Fatalf("method registration changed or was not sorted: %#v", listed)
	}
	listed[0].Label = "mutated result"
	info, ok := MethodInfo(firstID)
	if !ok || info.Label != "First" || info.Simulation {
		t.Fatal("method lookup shared caller-owned metadata")
	}
	owner, ok := MethodOwner(firstID)
	if !ok || owner != id || !channel.HasFactory(channel.Payment, id) {
		t.Fatal("typed payment factory missing from the common catalogue")
	}
	if _, ok := LookupProtocol(firstID); !ok {
		t.Fatal("registered method is missing its callback protocol")
	}
	first, err := NewFor(id, firstID, FactoryConfig{Secret: "first-instance-only"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(firstID, FactoryConfig{Secret: "second-instance-only"})
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first.(*registeredPaymentFixture).secret != "first-instance-only" || second.(*registeredPaymentFixture).secret != "second-instance-only" {
		t.Fatal("configured instances shared a gateway or credentials")
	}
	if _, err := NewFor("mock", firstID, FactoryConfig{}); err == nil {
		t.Fatal("method accepted by an unrelated instance implementation")
	}
	if _, err := New(firstID+"_unregistered", FactoryConfig{}); err == nil {
		t.Fatal("unregistered method was inferred from its name")
	}
	if _, ok := LookupProtocol(firstID + "_unregistered"); ok {
		t.Fatal("protocol inferred for an unregistered method")
	}
	queries := QueryMethods()
	if !slices.IsSorted(queries) || !slices.Contains(queries, firstID) || slices.Contains(queries, "mock") {
		t.Fatal("query candidates are unsorted, missing or include simulation")
	}
	noQuery := paymentFixtureID()
	MustRegisterFactory(paymentFixtureDescriptor(noQuery), []Method{{ID: noQuery, Label: "No query"}}, mockWebhookProtocol{}, func(method string, _ FactoryConfig) (Gateway, error) {
		return &registeredPaymentFixture{method: method}, nil
	})
	if slices.Contains(QueryMethods(), noQuery) {
		t.Fatal("method without descriptor query capability was scheduled")
	}
}

func TestPaymentRegistryRejectsAmbiguousRegistrationAndWrongFactories(t *testing.T) {
	factory := func(method string, _ FactoryConfig) (Gateway, error) {
		return &registeredPaymentFixture{method: method}, nil
	}
	for _, kind := range []string{"empty_methods", "duplicate_methods", "invalid_method", "missing_label", "simulation", "foreign_category", "nil_protocol", "nil_factory", "invalid_descriptor"} {
		t.Run(kind, func(t *testing.T) {
			id := paymentFixtureID()
			desc := paymentFixtureDescriptor(id)
			methods := []Method{{ID: id, Label: "Fixture"}}
			var protocol WebhookProtocol = mockWebhookProtocol{}
			currentFactory := Factory(factory)
			switch kind {
			case "empty_methods":
				methods = nil
			case "duplicate_methods":
				methods = append(methods, methods[0])
			case "invalid_method":
				methods[0].ID = "unsafe/route"
			case "missing_label":
				methods[0].Label = " "
			case "simulation":
				methods[0].Simulation = true
			case "foreign_category":
				desc.Category = channel.SMS
			case "nil_protocol":
				protocol = (*mockWebhookProtocol)(nil)
			case "nil_factory":
				currentFactory = nil
			case "invalid_descriptor":
				desc.SchemaVersion = -1
			}
			if err := RegisterFactory(desc, methods, protocol, currentFactory); err == nil {
				t.Fatal("invalid registration accepted")
			}
			if _, exists := MethodOwner(id); exists || len(Methods(id)) != 0 {
				t.Fatal("failed registration retained method metadata")
			}
			if kind == "invalid_descriptor" {
				if err := RegisterFactory(paymentFixtureDescriptor(id), []Method{{ID: id, Label: "Recovered"}}, mockWebhookProtocol{}, factory); err != nil {
					t.Fatal("failed descriptor registration reserved its method", err)
				}
			}
		})
	}
	id := paymentFixtureID()
	MustRegisterFactory(paymentFixtureDescriptor(id), []Method{{ID: id, Label: "Original"}}, mockWebhookProtocol{}, factory)
	for _, desc := range []channel.Descriptor{paymentFixtureDescriptor(id), paymentFixtureDescriptor(paymentFixtureID())} {
		if err := RegisterFactory(desc, []Method{{ID: id, Label: "Duplicate"}}, mockWebhookProtocol{}, factory); err == nil {
			t.Fatal("duplicate method or implementation registration accepted")
		}
	}
	if owner, _ := MethodOwner("mock"); owner != "mock" {
		t.Fatal("built-in simulation method was replaced")
	}
	for _, failure := range []string{"wrong_name", "nil_gateway", "factory_error"} {
		t.Run(failure, func(t *testing.T) {
			id := paymentFixtureID()
			cause := errors.New("local factory validation failed")
			MustRegisterFactory(paymentFixtureDescriptor(id), []Method{{ID: id, Label: "Fixture"}}, mockWebhookProtocol{}, func(method string, _ FactoryConfig) (Gateway, error) {
				switch failure {
				case "wrong_name":
					return &registeredPaymentFixture{method: "mock"}, nil
				case "nil_gateway":
					return (*registeredPaymentFixture)(nil), nil
				default:
					return nil, cause
				}
			})
			if gateway, err := New(id, FactoryConfig{}); err == nil || gateway != nil || (failure == "factory_error" && !errors.Is(err, cause)) {
				t.Fatalf("factory failure was lost: gateway=%v err=%v", gateway, err)
			}
		})
	}
}

func TestBuiltinPaymentMethodsStayCompatibleAndCreateIntentsLocally(t *testing.T) {
	var networkCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { networkCalls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	want := []Method{{ID: "epay_alipay", Label: "支付宝"}, {ID: "epay_bank", Label: "网银支付"}, {ID: "epay_qqpay", Label: "QQ 钱包"}, {ID: "epay_wxpay", Label: "微信支付"}}
	if !reflect.DeepEqual(Methods("epay"), want) || !reflect.DeepEqual(Methods("mock"), []Method{{ID: "mock", Label: "模拟支付", Simulation: true}}) {
		t.Fatal("built-in method IDs or labels changed")
	}
	config := EpayConfig{APIURL: server.URL, PID: "10001", Key: "local-merchant-key", PublicOrigin: "http://localhost:3088", Types: []string{"alipay", "wxpay", "qqpay", "bank"}}
	for _, method := range append(Methods("mock"), Methods("epay")...) {
		gateway, ok := For(method.ID, "local-mock-secret", config)
		if !ok || gateway.Name() != method.ID {
			t.Fatalf("legacy factory no longer resolves %s", method.ID)
		}
		intent, err := gateway.CreateIntent(IntentRequest{InvoiceID: 1, InvoiceNo: "LOCAL-1", AmountCents: 1234})
		if err != nil || intent.ProviderPaymentID == "" || intent.CheckoutURL == "" {
			t.Fatalf("local intent failed for %s: %v", method.ID, err)
		}
	}
	if networkCalls.Load() != 0 {
		t.Fatal("payment factory or CreateIntent performed network I/O")
	}
	for _, method := range []string{"epay_unknown", "epay", "mock_extra", ""} {
		if _, ok := For(method, "secret", config); ok {
			t.Fatalf("legacy factory accepted unknown method %q", method)
		}
	}
}
