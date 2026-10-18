package filestorage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
)

func s3TestProfile() Profile {
	return Profile{ID: "objects", Implementation: "s3", Values: map[string]any{
		"endpoint": "https://s3.example.invalid", "region": "us-east-1", "bucket": "private-files", "prefix": "application",
	}, Secrets: map[string]string{"access_key": "AKIDEXAMPLE", "secret_key": "example-secret-key", "session_token": "example-session-token"}}
}

func TestManagerDatabaseCompatibilityAndSafeSummaries(t *testing.T) {
	m, err := NewManager("", nil)
	if err != nil {
		t.Fatal(err)
	}
	id, backend, location, err := m.Default()
	if id != "database" || backend != nil || location != "" || err != nil {
		t.Fatalf("unexpected compatibility default: %q %T %q %v", id, backend, location, err)
	}
	if _, _, err := m.Resolve("database"); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("resolving database: %v", err)
	}
	if len(m.Summaries()) != 0 {
		t.Fatal("database is not a configured executable profile")
	}
	local := Profile{ID: "disk", Implementation: "local", Values: map[string]any{"root": t.TempDir()}}
	s3 := s3TestProfile()
	m, err = NewManager("objects", []Profile{s3, local})
	if err != nil {
		t.Fatal(err)
	}
	items := m.Summaries()
	if len(items) != 2 || items[0] != (ProfileSummary{ID: "disk", Implementation: "local"}) || items[1] != (ProfileSummary{ID: "objects", Implementation: "s3", Default: true}) {
		t.Fatalf("unexpected summaries: %#v", items)
	}
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{local.Values["root"].(string), "s3.example.invalid", "private-files", s3.Secrets["access_key"], s3.Secrets["secret_key"], s3.Secrets["session_token"]} {
		if strings.Contains(string(data), sensitive) {
			t.Fatal("summary exposed deployment configuration")
		}
	}
	items[0].ID = "mutated"
	if m.Summaries()[0].ID != "disk" {
		t.Fatal("summary is not an independent copy")
	}
	if _, _, err := m.Resolve("missing"); err != ErrConfiguration {
		t.Fatalf("unknown profile: %v", err)
	}
	var empty *Manager
	if _, _, _, err := empty.Default(); err != ErrConfiguration {
		t.Fatalf("nil manager default: %v", err)
	}
	if _, _, err := empty.Resolve("disk"); err != ErrConfiguration || len(empty.Summaries()) != 0 {
		t.Fatal("nil manager must fail closed")
	}
}

func TestManagerLocationIdentityIgnoresCredentialsAndDefault(t *testing.T) {
	profile := s3TestProfile()
	first, err := NewManager(profile.ID, []Profile{profile})
	if err != nil {
		t.Fatal(err)
	}
	_, fingerprint, err := first.Resolve(profile.ID)
	if err != nil || !objectKey.MatchString(fingerprint) {
		t.Fatal("invalid location fingerprint")
	}
	profile.Secrets["access_key"], profile.Secrets["secret_key"], profile.Secrets["session_token"] = "ROTATEDACCESS", "rotated-secret", "rotated-token"
	profile.Values["endpoint"], profile.Values["prefix"] = "https://S3.EXAMPLE.INVALID:443/", "/application/"
	rotated, err := NewManager("database", []Profile{profile})
	if err != nil {
		t.Fatal(err)
	}
	_, afterRotation, _ := rotated.Resolve(profile.ID)
	if fingerprint != afterRotation {
		t.Fatal("credential/default rotation or equivalent canonical location changed identity")
	}
	for _, field := range []string{"endpoint", "bucket", "region", "prefix"} {
		changed := s3TestProfile()
		switch field {
		case "endpoint":
			changed.Values[field] = "https://other.example.invalid"
		case "bucket":
			changed.Values[field] = "other-files"
		case "region":
			changed.Values[field] = "eu-west-1"
		case "prefix":
			changed.Values[field] = "other"
		}
		manager, err := NewManager(changed.ID, []Profile{changed})
		if err != nil {
			t.Fatal(err)
		}
		_, actual, _ := manager.Resolve(changed.ID)
		if actual == fingerprint {
			t.Fatalf("location field %s did not change fingerprint", field)
		}
	}
	local := Profile{ID: "disk", Implementation: "local", Values: map[string]any{"root": t.TempDir()}}
	manager, err := NewManager(local.ID, []Profile{local})
	if err != nil {
		t.Fatal(err)
	}
	_, localFingerprint, _ := manager.Resolve(local.ID)
	local.Values["root"] = t.TempDir()
	manager, err = NewManager(local.ID, []Profile{local})
	if err != nil {
		t.Fatal(err)
	}
	_, differentRoot, _ := manager.Resolve(local.ID)
	if localFingerprint == differentRoot || localFingerprint == fingerprint {
		t.Fatal("root/backend location identity collision")
	}
}

func TestManagerRejectsUnknownDuplicateAndMalformedConfiguration(t *testing.T) {
	cases := map[string]func(*Profile){
		"reserved ID":            func(p *Profile) { p.ID = "database" },
		"invalid ID":             func(p *Profile) { p.ID = "../disk" },
		"unknown implementation": func(p *Profile) { p.Implementation = "external-plugin" },
		"ordinary secret":        func(p *Profile) { p.Values["secret_key"] = "not-allowed" },
		"unknown ordinary field": func(p *Profile) { p.Values["unknown"] = "ignored?" },
		"schema override":        func(p *Profile) { p.Values["schema_version"] = 2 },
		"unknown secret field":   func(p *Profile) { p.Secrets["unknown"] = "ignored?" },
		"missing secret":         func(p *Profile) { delete(p.Secrets, "secret_key") },
		"invalid secret":         func(p *Profile) { p.Secrets["secret_key"] = "secret\nheader" },
		"invalid access":         func(p *Profile) { p.Secrets["access_key"] = "access/credential" },
		"invalid type":           func(p *Profile) { p.Values["bucket"] = 123 },
		"external HTTP":          func(p *Profile) { p.Values["endpoint"] = "http://s3.example.invalid" },
		"DNS HTTP":               func(p *Profile) { p.Values["endpoint"] = "http://localhost:9000" },
		"endpoint credentials":   func(p *Profile) { p.Values["endpoint"] = "https://username:password@example.invalid" },
		"endpoint path":          func(p *Profile) { p.Values["endpoint"] = "https://example.invalid/base" },
		"endpoint query":         func(p *Profile) { p.Values["endpoint"] = "https://example.invalid?token=secret" },
		"endpoint fragment":      func(p *Profile) { p.Values["endpoint"] = "https://example.invalid#secret" },
		"endpoint port":          func(p *Profile) { p.Values["endpoint"] = "https://example.invalid:99999" },
		"bad region":             func(p *Profile) { p.Values["region"] = "us/east/1" },
		"bad bucket":             func(p *Profile) { p.Values["bucket"] = "../bucket" },
		"IPv4 bucket":            func(p *Profile) { p.Values["bucket"] = "127.0.0.1" },
		"prefix traversal":       func(p *Profile) { p.Values["prefix"] = "a/../b" },
		"prefix backslash":       func(p *Profile) { p.Values["prefix"] = "a\\b" },
		"prefix control":         func(p *Profile) { p.Values["prefix"] = "a\nb" },
		"prefix too long":        func(p *Profile) { p.Values["prefix"] = strings.Repeat("a", 1024) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			profile := s3TestProfile()
			mutate(&profile)
			if _, err := NewManager("database", []Profile{profile}); err != ErrConfiguration {
				t.Fatalf("expected fixed configuration error, got %v", err)
			}
		})
	}
	profile := s3TestProfile()
	if _, err := NewManager("database", []Profile{profile, profile}); err != ErrConfiguration {
		t.Fatalf("duplicate profile: %v", err)
	}
	if _, err := NewManager("missing", nil); err != ErrConfiguration {
		t.Fatalf("unknown default: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "must-not-be-created")
	if _, err := NewManager("disk", []Profile{{ID: "disk", Implementation: "local", Values: map[string]any{"root": missing}}}); err != ErrConfiguration {
		t.Fatal("missing local root was accepted")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("configuration created local directory")
	}
}

func TestStorageRegistrySchemaAndOfflineConstructors(t *testing.T) {
	for _, implementation := range []string{"local", "s3"} {
		desc, exists := channel.Lookup(channel.Storage, implementation)
		if !exists || !channel.HasFactory(channel.Storage, implementation) || desc.SchemaVersion != 1 || desc.ContractVersion != 1 || desc.InstanceMode != "profile" {
			t.Fatalf("invalid compiled registration: %#v", desc)
		}
	}
	profile := s3TestProfile()
	// example.invalid cannot resolve, but construction must remain fully offline.
	if _, err := NewManager(profile.ID, []Profile{profile}); err != nil {
		t.Fatal(err)
	}
	profile.Values["endpoint"] = "http://127.0.0.1:1"
	if _, err := NewManager(profile.ID, []Profile{profile}); err != nil {
		t.Fatal(err)
	}
	profile.Values["endpoint"] = "http://[::1]:1"
	if _, err := NewManager(profile.ID, []Profile{profile}); err != nil {
		t.Fatal(err)
	}
	if _, err := backends.New("s3", module.Config{Values: profile.Values, Secrets: profile.Secrets, Secret: "legacy-secret"}); err != ErrConfiguration {
		t.Fatal("untyped legacy credential bypass was accepted")
	}
	if _, err := backends.New("s3", module.Config{Values: profile.Values, Secrets: profile.Secrets, Payload: struct{}{}}); err != ErrConfiguration {
		t.Fatal("unsupported runtime configuration payload was accepted")
	}
}
