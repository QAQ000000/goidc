package payment

import (
	"errors"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
)

// Method is a stable, globally unique checkout choice. Configuration belongs
// to a channel instance; method metadata never contains merchant credentials.
type Method struct {
	ID         string
	Label      string
	Simulation bool
}

type Factory func(method string, config FactoryConfig) (Gateway, error)

type paymentImplementation struct {
	methods  []Method
	protocol WebhookProtocol
	query    bool
}

var methodsRegistry = struct {
	sync.RWMutex
	owners          map[string]string
	implementations map[string]paymentImplementation
}{owners: make(map[string]string), implementations: make(map[string]paymentImplementation)}

var paymentMethodID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)

// gatewayRegistry keeps executable factories in the common catalogue. The
// private payload carries the selected method without exposing it as mutable
// merchant configuration or changing the common module.Config contract.
var gatewayRegistry = module.NewRegistry[Gateway](channel.Payment)

type factoryInvocation struct {
	method  string
	payload any
}

func RegisterFactory(desc channel.Descriptor, methods []Method, protocol WebhookProtocol, factory Factory) error {
	return registerFactory(desc, methods, protocol, factory, false)
}

func MustRegisterFactory(desc channel.Descriptor, methods []Method, protocol WebhookProtocol, factory Factory) {
	if err := RegisterFactory(desc, methods, protocol, factory); err != nil {
		panic(err)
	}
}

func registerFactory(desc channel.Descriptor, methods []Method, protocol WebhookProtocol, factory Factory, builtin bool) error {
	desc.ID = strings.TrimSpace(desc.ID)
	if desc.Category != channel.Payment || desc.ID == "" || factory == nil || nilValue(protocol) || len(methods) == 0 {
		return errors.New("invalid payment registration")
	}
	registered := append([]Method(nil), methods...)
	seen := make(map[string]bool, len(registered))
	for i := range registered {
		method := &registered[i]
		method.ID, method.Label = strings.TrimSpace(method.ID), strings.TrimSpace(method.Label)
		if !paymentMethodID.MatchString(method.ID) || method.Label == "" || seen[method.ID] {
			return errors.New("invalid or duplicate payment method")
		}
		if method.Simulation && (!builtin || desc.ID != "mock" || method.ID != "mock") {
			return errors.New("only the built-in mock payment method may simulate payments")
		}
		seen[method.ID] = true
	}
	sort.Slice(registered, func(i, j int) bool { return registered[i].ID < registered[j].ID })
	methodsRegistry.Lock()
	defer methodsRegistry.Unlock()
	if _, exists := methodsRegistry.implementations[desc.ID]; exists {
		return errors.New("payment implementation already registered")
	}
	for _, method := range registered {
		if _, exists := methodsRegistry.owners[method.ID]; exists {
			return errors.New("payment method already registered")
		}
	}
	if err := gatewayRegistry.Register(desc, func(config FactoryConfig) (Gateway, error) {
		method := ""
		if invocation, ok := config.Payload.(factoryInvocation); ok {
			method, config.Payload = invocation.method, invocation.payload
		} else if len(registered) == 1 {
			method = registered[0].ID
		}
		if !seen[method] {
			return nil, errors.New("payment method does not belong to the implementation")
		}
		gateway, err := factory(method, config)
		if err != nil {
			return nil, err
		}
		if nilValue(gateway) || gateway.Name() != method {
			return nil, errors.New("payment factory returned a mismatched gateway")
		}
		return gateway, nil
	}); err != nil {
		return err
	}
	query := false
	for _, capability := range desc.Capabilities {
		if capability == "query" {
			query = true
		}
	}
	methodsRegistry.implementations[desc.ID] = paymentImplementation{registered, protocol, query}
	for _, method := range registered {
		methodsRegistry.owners[method.ID] = desc.ID
	}
	return nil
}

func nilValue(value any) bool {
	if value == nil {
		return true
	}
	switch reflect.ValueOf(value).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	}
	return false
}

func MethodOwner(id string) (string, bool) {
	methodsRegistry.RLock()
	owner, ok := methodsRegistry.owners[strings.TrimSpace(id)]
	methodsRegistry.RUnlock()
	return owner, ok
}

func MethodInfo(id string) (Method, bool) {
	id = strings.TrimSpace(id)
	methodsRegistry.RLock()
	defer methodsRegistry.RUnlock()
	owner, ok := methodsRegistry.owners[id]
	if ok {
		for _, method := range methodsRegistry.implementations[owner].methods {
			if method.ID == id {
				return method, true
			}
		}
	}
	return Method{}, false
}

// Methods returns static metadata. The caller must resolve and validate the
// actual instance configuration before exposing a method for a new intent.
func Methods(implementation string) []Method {
	methodsRegistry.RLock()
	methods := append([]Method(nil), methodsRegistry.implementations[strings.TrimSpace(implementation)].methods...)
	methodsRegistry.RUnlock()
	return methods
}

func LookupProtocol(method string) (WebhookProtocol, bool) {
	methodsRegistry.RLock()
	defer methodsRegistry.RUnlock()
	owner, ok := methodsRegistry.owners[strings.TrimSpace(method)]
	if !ok {
		return nil, false
	}
	return methodsRegistry.implementations[owner].protocol, true
}

func QueryMethods() []string {
	methodsRegistry.RLock()
	defer methodsRegistry.RUnlock()
	methods := []string{}
	for _, implementation := range methodsRegistry.implementations {
		if !implementation.query {
			continue
		}
		for _, method := range implementation.methods {
			if !method.Simulation {
				methods = append(methods, method.ID)
			}
		}
	}
	sort.Strings(methods)
	return methods
}

func NewFor(implementation, method string, config FactoryConfig) (Gateway, error) {
	implementation, method = strings.TrimSpace(implementation), strings.TrimSpace(method)
	owner, ok := MethodOwner(method)
	if !ok || implementation == "" || owner != implementation {
		return nil, errors.New("payment method does not belong to the implementation")
	}
	config.Payload = factoryInvocation{method: method, payload: config.Payload}
	return gatewayRegistry.New(implementation, config)
}
