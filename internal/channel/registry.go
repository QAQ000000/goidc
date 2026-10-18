// Package channel contains the compiled-in channel catalogue. Implementations
// are deliberately registered by the binary; database rows hold configuration,
// never executable code.
package channel

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Category string

const (
	Payment       Category = "payment"
	OAuth         Category = "oauth"
	SMS           Category = "sms"
	Mail          Category = "mail"
	Bot           Category = "bot"
	Certification Category = "certification"
	Provision     Category = "provision"
	Storage       Category = "storage"
	Captcha       Category = "captcha"
)

// ValidCategory recognizes public module categories. Registry tests may use
// private categories, while persisted/API contracts use this finite catalogue.
func ValidCategory(category Category) bool {
	switch category {
	case Payment, OAuth, SMS, Mail, Bot, Certification, Provision, Storage, Captcha:
		return true
	default:
		return false
	}
}

type FieldType string

const (
	Text        FieldType = "text"
	Number      FieldType = "number"
	Boolean     FieldType = "boolean"
	Select      FieldType = "select"
	MultiSelect FieldType = "multiselect"
	Textarea    FieldType = "textarea"
)

type Field struct {
	Key      string    `json:"key"`
	Label    string    `json:"label"`
	Type     FieldType `json:"type"`
	Required bool      `json:"required"`
	Secret   bool      `json:"secret"`
	Options  []string  `json:"options,omitempty"`
	Default  any       `json:"default,omitempty"`
	Min      *float64  `json:"min,omitempty"`
	Max      *float64  `json:"max,omitempty"`
	Integer  bool      `json:"integer,omitempty"`
}

type Descriptor struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Category        Category `json:"category"`
	Capabilities    []string `json:"capabilities"`
	Fields          []Field  `json:"fields"`
	Version         string   `json:"version"`
	SchemaVersion   int      `json:"schema_version"`
	ContractVersion int      `json:"contract_version"`
	InstanceMode    string   `json:"instance_mode"`
}

// Factory is the erased boundary used by the common registry. Category
// packages wrap it with a typed registry so business code never handles any.
// Factories are compiled into the binary; database configuration is data only.
type Factory func(config any) (any, error)

var ErrFactoryUnavailable = errors.New("channel factory is not registered")

var registry = struct {
	sync.RWMutex
	items            map[string]Descriptor
	factories        map[string]Factory
	validators       map[string]ConfigValidator
	secretValidators map[string]SecretValidator
}{items: make(map[string]Descriptor), factories: make(map[string]Factory), validators: make(map[string]ConfigValidator), secretValidators: make(map[string]SecretValidator)}

func key(category Category, id string) string { return string(category) + ":" + id }

func cloneDescriptor(desc Descriptor) Descriptor {
	desc.Capabilities = append([]string(nil), desc.Capabilities...)
	desc.Fields = append([]Field(nil), desc.Fields...)
	for i := range desc.Fields {
		desc.Fields[i].Options = append([]string(nil), desc.Fields[i].Options...)
		desc.Fields[i].Default = cloneDefault(desc.Fields[i].Default)
		if desc.Fields[i].Min != nil {
			value := *desc.Fields[i].Min
			desc.Fields[i].Min = &value
		}
		if desc.Fields[i].Max != nil {
			value := *desc.Fields[i].Max
			desc.Fields[i].Max = &value
		}
	}
	return desc
}

func Register(desc Descriptor) error {
	var err error
	desc, err = normalizeDescriptor(desc)
	if err != nil {
		return err
	}
	registry.Lock()
	defer registry.Unlock()
	k := key(desc.Category, desc.ID)
	if _, exists := registry.items[k]; exists {
		return errors.New("channel descriptor already registered")
	}
	registry.items[k] = cloneDescriptor(desc)
	return nil
}

// RegisterFactory registers a descriptor and its executable implementation in
// one operation. Use AttachFactory when a descriptor is intentionally declared
// separately from its implementation package.
func RegisterFactory(desc Descriptor, factory Factory) error {
	if factory == nil {
		return errors.New("nil channel factory")
	}
	var err error
	desc, err = normalizeDescriptor(desc)
	if err != nil {
		return err
	}
	registry.Lock()
	defer registry.Unlock()
	k := key(desc.Category, desc.ID)
	if _, exists := registry.items[k]; exists {
		return errors.New("channel descriptor already registered")
	}
	registry.items[k] = cloneDescriptor(desc)
	registry.factories[k] = factory
	return nil
}

// AttachFactory adds behavior to an already registered descriptor. It is
// useful for packages that keep catalogue metadata in a small shared file.
func AttachFactory(category Category, id string, factory Factory) error {
	if factory == nil {
		return errors.New("nil channel factory")
	}
	registry.Lock()
	defer registry.Unlock()
	k := key(category, strings.TrimSpace(id))
	if _, exists := registry.items[k]; !exists {
		return errors.New("channel descriptor not registered")
	}
	if _, exists := registry.factories[k]; exists {
		return errors.New("channel factory already registered")
	}
	registry.factories[k] = factory
	return nil
}

func MustRegister(desc Descriptor) {
	if err := Register(desc); err != nil {
		panic(err)
	}
}

func MustRegisterFactory(desc Descriptor, factory Factory) {
	if err := RegisterFactory(desc, factory); err != nil {
		panic(err)
	}
}

func MustAttachFactory(category Category, id string, factory Factory) {
	if err := AttachFactory(category, id, factory); err != nil {
		panic(err)
	}
}

// New resolves a compiled-in implementation. The returned value is kept
// erased here to avoid an import cycle between the common catalogue and
// category-specific contracts.
func New(category Category, id string, config any) (any, error) {
	registry.RLock()
	factory, ok := registry.factories[key(category, strings.TrimSpace(id))]
	registry.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s:%s", ErrFactoryUnavailable, category, strings.TrimSpace(id))
	}
	return factory(config)
}

func HasFactory(category Category, id string) bool {
	registry.RLock()
	_, ok := registry.factories[key(category, strings.TrimSpace(id))]
	registry.RUnlock()
	return ok
}

func Lookup(category Category, id string) (Descriptor, bool) {
	registry.RLock()
	d, ok := registry.items[key(category, id)]
	registry.RUnlock()
	return cloneDescriptor(d), ok
}

func List(category Category) []Descriptor {
	registry.RLock()
	result := make([]Descriptor, 0, len(registry.items))
	for _, d := range registry.items {
		if category == "" || d.Category == category {
			result = append(result, cloneDescriptor(d))
		}
	}
	registry.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		if result[i].Category != result[j].Category {
			return result[i].Category < result[j].Category
		}
		return result[i].ID < result[j].ID
	})
	return result
}
