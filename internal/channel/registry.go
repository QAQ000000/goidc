// Package channel contains the compiled-in channel catalogue. Implementations
// are deliberately registered by the binary; database rows hold configuration,
// never executable code.
package channel

import (
	"errors"
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
	Certification Category = "certification"
	Provision     Category = "provision"
)

type FieldType string

const (
	Text     FieldType = "text"
	Number   FieldType = "number"
	Boolean  FieldType = "boolean"
	Select   FieldType = "select"
	Textarea FieldType = "textarea"
)

type Field struct {
	Key      string    `json:"key"`
	Label    string    `json:"label"`
	Type     FieldType `json:"type"`
	Required bool      `json:"required"`
	Secret   bool      `json:"secret"`
	Options  []string  `json:"options,omitempty"`
}

type Descriptor struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Category     Category `json:"category"`
	Capabilities []string `json:"capabilities"`
	Fields       []Field  `json:"fields"`
}

var registry = struct {
	sync.RWMutex
	items map[string]Descriptor
}{items: make(map[string]Descriptor)}

func key(category Category, id string) string { return string(category) + ":" + id }

func Register(desc Descriptor) error {
	desc.ID = strings.TrimSpace(desc.ID)
	desc.Name = strings.TrimSpace(desc.Name)
	if desc.ID == "" || desc.Name == "" || desc.Category == "" {
		return errors.New("invalid channel descriptor")
	}
	registry.Lock()
	defer registry.Unlock()
	k := key(desc.Category, desc.ID)
	if _, exists := registry.items[k]; exists {
		return errors.New("channel descriptor already registered")
	}
	registry.items[k] = desc
	return nil
}

func MustRegister(desc Descriptor) {
	if err := Register(desc); err != nil {
		panic(err)
	}
}

func Lookup(category Category, id string) (Descriptor, bool) {
	registry.RLock()
	d, ok := registry.items[key(category, id)]
	registry.RUnlock()
	return d, ok
}

func List(category Category) []Descriptor {
	registry.RLock()
	result := make([]Descriptor, 0, len(registry.items))
	for _, d := range registry.items {
		if category == "" || d.Category == category {
			result = append(result, d)
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
