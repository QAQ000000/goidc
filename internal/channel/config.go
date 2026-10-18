package channel

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sort"
	"strings"
)

const ContractVersion = 1

// ConfigValidator must only check local configuration. It receives credential
// presence, never plaintext, and must not perform network or business actions.
type ConfigValidator func(values map[string]any, secureFields map[string]bool) []ValidationError

// SecretValidator checks credential syntax locally. It must never retain,
// return, log or transmit values. Missing fields are handled separately by
// ValidateSecrets; validators inspect only fields present in the input map.
type SecretValidator func(values map[string]string) []ValidationError

func RegisterSecretValidator(category Category, id string, validator SecretValidator) error {
	registry.Lock()
	defer registry.Unlock()
	k := key(category, id)
	if validator == nil || registry.factories[k] == nil || registry.secretValidators[k] != nil {
		return errors.New("invalid secret validator registration")
	}
	registry.secretValidators[k] = validator
	return nil
}

func MustRegisterSecretValidator(category Category, id string, validator SecretValidator) {
	if err := RegisterSecretValidator(category, id, validator); err != nil {
		panic(err)
	}
}

func ValidateSecretValues(desc Descriptor, values map[string]string) []ValidationError {
	registry.RLock()
	validator := registry.secretValidators[key(desc.Category, desc.ID)]
	registry.RUnlock()
	if validator == nil {
		return nil
	}
	copyValues := make(map[string]string, len(values))
	for field, value := range values {
		copyValues[field] = value
	}
	return validator(copyValues)
}

func RegisterConfigValidator(category Category, id string, validator ConfigValidator) error {
	registry.Lock()
	defer registry.Unlock()
	k := key(category, id)
	if validator == nil || registry.factories[k] == nil || registry.validators[k] != nil {
		return errors.New("invalid configuration validator registration")
	}
	registry.validators[k] = validator
	return nil
}

func MustRegisterConfigValidator(category Category, id string, validator ConfigValidator) {
	if err := RegisterConfigValidator(category, id, validator); err != nil {
		panic(err)
	}
}

// ValidationError identifies a field without including its (possibly sensitive)
// value. It is safe to return in administration and readiness responses.
type ValidationError struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

var fieldKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func normalizeDescriptor(desc Descriptor) (Descriptor, error) {
	desc.ID, desc.Name = strings.TrimSpace(desc.ID), strings.TrimSpace(desc.Name)
	if desc.ID == "" || desc.Name == "" || desc.Category == "" {
		return Descriptor{}, errors.New("invalid channel descriptor")
	}
	if desc.Version == "" {
		desc.Version = "builtin"
	}
	if desc.SchemaVersion == 0 {
		desc.SchemaVersion = 1
	}
	if desc.ContractVersion == 0 {
		desc.ContractVersion = ContractVersion
	}
	if desc.InstanceMode == "" {
		desc.InstanceMode = "channel"
	}
	if desc.SchemaVersion < 1 || desc.ContractVersion != ContractVersion ||
		(desc.InstanceMode != "channel" && desc.InstanceMode != "supplier" && desc.InstanceMode != "profile") {
		return Descriptor{}, errors.New("unsupported module contract")
	}
	seen := map[string]bool{}
	for _, f := range desc.Fields {
		if !fieldKey.MatchString(f.Key) || seen[f.Key] {
			return Descriptor{}, errors.New("invalid or duplicate configuration field")
		}
		seen[f.Key] = true
		switch f.Type {
		case Text, Textarea, Number, Boolean, Select, MultiSelect:
		default:
			return Descriptor{}, errors.New("unsupported configuration field type")
		}
		if f.Secret && (f.Default != nil || (f.Type != Text && f.Type != Textarea)) {
			return Descriptor{}, errors.New("invalid secret field")
		}
		if (f.Min != nil || f.Max != nil || f.Integer) && f.Type != Number {
			return Descriptor{}, errors.New("numeric constraints require a number")
		}
		if (f.Min != nil && !finite(*f.Min)) || (f.Max != nil && !finite(*f.Max)) ||
			(f.Min != nil && f.Max != nil && *f.Min > *f.Max) {
			return Descriptor{}, errors.New("invalid numeric bounds")
		}
		options := map[string]bool{}
		for _, option := range f.Options {
			if options[option] || strings.TrimSpace(option) == "" {
				return Descriptor{}, errors.New("invalid configuration options")
			}
			options[option] = true
		}
		if (f.Type == Select || f.Type == MultiSelect) && len(f.Options) == 0 {
			return Descriptor{}, errors.New("select requires options")
		}
		if f.Default != nil {
			if _, code := normalizeField(f, f.Default); code != "" {
				return Descriptor{}, errors.New("invalid configuration default")
			}
		}
	}
	return desc, nil
}

func cloneDefault(value any) any {
	switch v := value.(type) {
	case []string:
		out := make([]string, len(v))
		copy(out, v)
		return out
	case []any:
		out := make([]any, len(v))
		copy(out, v)
		return out
	default:
		return v
	}
}

func finite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }

func number(value any) (float64, bool) {
	var n float64
	switch v := value.(type) {
	case float64:
		n = v
	case float32:
		n = float64(v)
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case int32:
		n = float64(v)
	case json.Number:
		var err error
		n, err = v.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return n, finite(n)
}

func normalizeField(f Field, value any) (any, string) {
	switch f.Type {
	case Text, Textarea, Select:
		v, ok := value.(string)
		if !ok {
			return nil, "invalid_type"
		}
		if f.Required && strings.TrimSpace(v) == "" {
			return nil, "required"
		}
		if f.Type == Select {
			found := false
			for _, option := range f.Options {
				found = found || option == v
			}
			if !found {
				return nil, "invalid_option"
			}
		}
		return v, ""
	case Boolean:
		v, ok := value.(bool)
		if !ok {
			return nil, "invalid_type"
		}
		return v, ""
	case Number:
		v, ok := number(value)
		if !ok {
			return nil, "invalid_type"
		}
		if f.Integer && math.Trunc(v) != v {
			return nil, "invalid_integer"
		}
		if (f.Min != nil && v < *f.Min) || (f.Max != nil && v > *f.Max) {
			return nil, "out_of_range"
		}
		return v, ""
	case MultiSelect:
		values := []string{}
		switch v := value.(type) {
		case []string:
			values = append(values, v...)
		case []any:
			for _, item := range v {
				text, ok := item.(string)
				if !ok {
					return nil, "invalid_type"
				}
				values = append(values, text)
			}
		default:
			return nil, "invalid_type"
		}
		if f.Required && len(values) == 0 {
			return nil, "required"
		}
		seen := map[string]bool{}
		for _, text := range values {
			found := false
			for _, option := range f.Options {
				found = found || option == text
			}
			if !found {
				return nil, "invalid_option"
			}
			if seen[text] {
				return nil, "duplicate_option"
			}
			seen[text] = true
		}
		return values, ""
	default:
		return nil, "unsupported_type"
	}
}

// NormalizeConfig is shared by writes, runtime resolution, and configuration
// status. Missing optional nulls are omitted, and explicit defaults are applied.
// Unknown fields and secret fields are rejected rather than silently persisted.
func NormalizeConfig(desc Descriptor, values map[string]any) (map[string]any, []ValidationError) {
	out := map[string]any{}
	errs := []ValidationError{}
	fields := map[string]Field{}
	for _, field := range desc.Fields {
		fields[field.Key] = field
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		field, known := fields[key]
		if !known {
			errs = append(errs, ValidationError{Field: key, Code: "unknown_field"})
		} else if field.Secret {
			errs = append(errs, ValidationError{Field: key, Code: "secret_in_config"})
		}
	}
	for _, f := range desc.Fields {
		if f.Secret {
			continue
		}
		value, exists := values[f.Key]
		if text, ok := value.(string); ok && f.Type == Select && !f.Required && text == "" {
			exists = false
		}
		if !exists || value == nil {
			if f.Default != nil {
				value = cloneDefault(f.Default)
			} else if f.Required {
				errs = append(errs, ValidationError{Field: f.Key, Code: "required"})
				continue
			} else {
				continue
			}
		}
		v, code := normalizeField(f, value)
		if code != "" {
			errs = append(errs, ValidationError{Field: f.Key, Code: code})
			continue
		}
		out[f.Key] = v
	}
	return out, errs
}

func SecretKeys(desc Descriptor) []string {
	keys := []string{}
	for _, field := range desc.Fields {
		if field.Secret {
			keys = append(keys, field.Key)
		}
	}
	return keys
}

func ValidateSecrets(desc Descriptor, configured map[string]bool, enabled bool) []ValidationError {
	errs := []ValidationError{}
	if !enabled {
		return errs
	}
	for _, field := range desc.Fields {
		if field.Secret && field.Required && !configured[field.Key] {
			errs = append(errs, ValidationError{Field: field.Key, Code: "required"})
		}
	}
	return errs
}

func ValidateConfiguration(desc Descriptor, values map[string]any, configured map[string]bool, enabled bool) (map[string]any, []ValidationError) {
	normalized, errs := NormalizeConfig(desc, values)
	errs = append(errs, ValidateSecrets(desc, configured, enabled)...)
	if enabled && len(errs) == 0 {
		registry.RLock()
		validator := registry.validators[key(desc.Category, desc.ID)]
		registry.RUnlock()
		if validator != nil {
			errs = append(errs, validator(normalized, configured)...)
		}
	}
	return normalized, errs
}
