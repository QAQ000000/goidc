package channel

import (
	"encoding/json"
	"math"
	"testing"
)

func TestNormalizeConfigEnforcesContractWithoutEchoingValues(t *testing.T) {
	min, max := 1.0, 10.0
	desc := Descriptor{Fields: []Field{
		{Key: "name", Type: Text, Required: true},
		{Key: "enabled", Type: Boolean, Required: true},
		{Key: "count", Type: Number, Min: &min, Max: &max, Integer: true},
		{Key: "mode", Type: Select, Options: []string{"a", "b"}, Default: "b"},
		{Key: "choices", Type: MultiSelect, Options: []string{"a", "b"}, Default: []string{"a"}},
		{Key: "notes", Type: Textarea},
		{Key: "token", Type: Text, Secret: true, Required: true},
	}}
	base := func() map[string]any {
		return map[string]any{"name": "fixture", "enabled": false, "count": 3, "notes": nil}
	}
	got, errs := NormalizeConfig(desc, base())
	if len(errs) > 0 || got["enabled"] != false || got["mode"] != "b" || got["count"] != float64(3) {
		t.Fatalf("normalized=%#v errors=%#v", got, errs)
	}
	if _, ok := got["notes"]; ok {
		t.Fatal("optional null was persisted")
	}
	got["choices"].([]string)[0] = "b"
	if desc.Fields[4].Default.([]string)[0] != "a" {
		t.Fatal("default was mutated")
	}
	for _, tc := range []struct {
		name, key string
		value     any
		code      string
	}{
		{"unknown", "typo", "sensitive-value", "unknown_field"},
		{"secret", "token", "sensitive-value", "secret_in_config"},
		{"boolean", "enabled", "false", "invalid_type"},
		{"number", "count", "3", "invalid_type"},
		{"nan", "count", math.NaN(), "invalid_type"},
		{"fraction", "count", 1.5, "invalid_integer"},
		{"range", "count", 11, "out_of_range"},
		{"enum", "mode", "c", "invalid_option"},
		{"multi_type", "choices", "a", "invalid_type"},
		{"multi_enum", "choices", []string{"c"}, "invalid_option"},
		{"multi_duplicate", "choices", []string{"a", "a"}, "duplicate_option"},
		{"required", "name", " ", "required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := base()
			input[tc.key] = tc.value
			_, errs := NormalizeConfig(desc, input)
			if len(errs) != 1 || errs[0] != (ValidationError{Field: tc.key, Code: tc.code}) {
				t.Fatalf("unexpected safe errors: %#v", errs)
			}
		})
	}
	if len(ValidateSecrets(desc, map[string]bool{}, false)) != 0 || len(ValidateSecrets(desc, map[string]bool{}, true)) != 1 {
		t.Fatal("secret requiredness does not follow activation")
	}
}

func TestDescriptorRejectsInvalidSchemasAndDefensivelyCopiesDefaults(t *testing.T) {
	for _, empty := range []any{[]string{}, []any{}} {
		raw, err := json.Marshal(cloneDescriptor(Descriptor{Fields: []Field{{Key: "choices", Type: MultiSelect, Default: empty}}}).Fields[0].Default)
		if err != nil || string(raw) != "[]" {
			t.Fatalf("empty array default changed in catalogue: %s, %v", raw, err)
		}
	}
	for _, fields := range [][]Field{
		{{Key: "a", Type: Text}, {Key: "a", Type: Text}},
		{{Key: "a", Type: FieldType("script")}},
		{{Key: "a", Type: Select, Options: []string{"a"}, Default: "b"}},
		{{Key: "a", Type: Text, Secret: true, Default: "secret"}},
	} {
		if _, err := normalizeDescriptor(Descriptor{ID: "invalid", Name: "Invalid", Category: SMS, Fields: fields}); err == nil {
			t.Fatal("invalid schema accepted")
		}
	}
	defaults := []string{"a"}
	desc := Descriptor{ID: "schema-copy", Name: "Copy", Category: Category("schema-copy"), Fields: []Field{{Key: "a", Type: MultiSelect, Options: []string{"a", "b"}, Default: defaults}}}
	if err := Register(desc); err != nil {
		t.Fatal(err)
	}
	defaults[0] = "b"
	got, _ := Lookup(desc.Category, desc.ID)
	if got.Version != "builtin" || got.SchemaVersion != 1 || got.ContractVersion != ContractVersion || got.InstanceMode != "channel" || got.Fields[0].Default.([]string)[0] != "a" {
		t.Fatalf("bad registration: %#v", got)
	}
	got.Fields[0].Default.([]string)[0] = "b"
	again, _ := Lookup(desc.Category, desc.ID)
	if again.Fields[0].Default.([]string)[0] != "a" {
		t.Fatal("catalogue exposes mutable defaults")
	}
}

func TestSecretSyntaxValidatorsRequireFactoriesAndProtectInputMap(t *testing.T) {
	desc := Descriptor{ID: "secret-syntax-fixture", Name: "Fixture", Category: Bot, Fields: []Field{{Key: "token", Type: Text, Secret: true}}}
	validator := func(values map[string]string) []ValidationError {
		if _, exists := values["token"]; !exists {
			return nil
		}
		values["token"] = "mutated"
		return []ValidationError{{Field: "token", Code: "invalid_token"}}
	}
	if err := RegisterSecretValidator(Bot, desc.ID, validator); err == nil {
		t.Fatal("secret validator registered without implementation")
	}
	if err := RegisterFactory(desc, func(any) (any, error) { return struct{}{}, nil }); err != nil {
		t.Fatal(err)
	}
	if err := RegisterSecretValidator(Bot, desc.ID, nil); err == nil {
		t.Fatal("nil validator registered")
	}
	if err := RegisterSecretValidator(Bot, desc.ID, validator); err != nil {
		t.Fatal(err)
	}
	if err := RegisterSecretValidator(Bot, desc.ID, validator); err == nil {
		t.Fatal("duplicate validator registered")
	}
	values := map[string]string{"token": "private-fixture"}
	errs := ValidateSecretValues(desc, values)
	if len(errs) != 1 || errs[0].Code != "invalid_token" || values["token"] != "private-fixture" {
		t.Fatal("secret validation mutated caller values or exposed a secret")
	}
	if len(ValidateSecretValues(desc, nil)) != 0 || len(ValidateSecretValues(Descriptor{Category: Bot, ID: "missing-fixture"}, values)) != 0 {
		t.Fatal("absent optional validator changed credential semantics")
	}
}
