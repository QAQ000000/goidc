package store

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/codex/zjmfgo/internal/secure"
	"github.com/codex/zjmfgo/internal/supplier"
)

func supplierConfigurationRecord(t *testing.T) (*Store, supplierAccountRecord) {
	t.Helper()
	s := &Store{ProvisionEncryptionKey: []byte("01234567890123456789012345678901")}
	sealed, err := supplierSeal(s.ProvisionEncryptionKey, "account", 42, "original-private-key")
	if err != nil {
		t.Fatal(err)
	}
	return s, supplierAccountRecord{item: SupplierAccountSummary{ID: 42, Provider: supplier.ProviderZJMF, SchemaVersion: 1, Active: true,
		Config: map[string]any{"endpoint": "https://supplier.example.test", "username": "account"}}, legacy: sealed, fields: map[string][]byte{}}
}

func TestSupplierNamedCredentialsAreOwnerAndFieldBound(t *testing.T) {
	s, rec := supplierConfigurationRecord(t)
	desc, _ := supplier.Lookup(rec.item.Provider)
	active := true
	in := SupplierInput{Provider: rec.item.Provider, Active: &active, SecureConfig: map[string]string{"secret": "replacement-private-key"}}
	raw, format, secrets, changed, err := s.mergeSupplierCredentials(rec, desc.Descriptor, in)
	if err != nil || format != 1 || !changed || secrets["secret"] != "replacement-private-key" {
		t.Fatal(format, changed, err)
	}
	if bytes.Contains(raw, []byte("private-key")) {
		t.Fatal("plaintext credential persisted")
	}
	oldCiphertext := append([]byte(nil), rec.legacy...)
	if err = json.Unmarshal(raw, &rec.fields); err != nil {
		t.Fatal(err)
	}
	rec.credentialFormat = format
	if !bytes.Equal(rec.legacy, oldCiphertext) {
		t.Fatal("promotion changed the original legacy ciphertext")
	}
	opened, err := s.openSupplierCredentials(rec, desc.Descriptor)
	if err != nil || opened["secret"] != secrets["secret"] {
		t.Fatal(opened, err)
	}
	if _, err = secure.Open(s.ProvisionEncryptionKey, supplierCredentialAAD(rec.item.ID, "another_field"), rec.fields["secret"]); err == nil {
		t.Fatal("named credential accepted another field owner")
	}
	rec.item.ID++
	if _, err = s.openSupplierCredentials(rec, desc.Descriptor); err == nil {
		t.Fatal("named credential accepted another account owner")
	}
}

func TestSupplierCredentialFormatNeverFallsBack(t *testing.T) {
	s, rec := supplierConfigurationRecord(t)
	desc, _ := supplier.Lookup(rec.item.Provider)
	for _, mutate := range []func(*supplierAccountRecord){
		func(r *supplierAccountRecord) { r.credentialFormat = 9 },
		func(r *supplierAccountRecord) { r.fields["secret"] = []byte("bad-named-field") },
		func(r *supplierAccountRecord) {
			r.credentialFormat = 1
			r.fields["secret"] = bytes.Repeat([]byte{1}, 48)
		},
		func(r *supplierAccountRecord) {
			r.credentialFormat = 1
			r.fields["unknown"] = bytes.Repeat([]byte{1}, 48)
		},
		func(r *supplierAccountRecord) { r.malformed = true },
	} {
		copyRecord := rec
		copyRecord.fields = map[string][]byte{}
		mutate(&copyRecord)
		if _, err := s.openSupplierCredentials(copyRecord, desc.Descriptor); err == nil {
			t.Fatal("malformed named format fell back to valid legacy ciphertext")
		}
	}
}

func TestSupplierConfigCompatibilityFreezeAndSafeSummary(t *testing.T) {
	s, rec := supplierConfigurationRecord(t)
	active := true
	in := SupplierInput{Provider: rec.item.Provider, BaseURL: "https://supplier.example.test/", Username: "account", Active: &active}
	if _, _, _, _, err := validateSupplierInput(in, &rec); err != nil {
		t.Fatal("legacy request rejected", err)
	}
	in.BaseURL = "https://redirect.example.test"
	if _, _, _, _, err := validateSupplierInput(in, &rec); err == nil {
		t.Fatal("legacy alias redirected the frozen identity")
	}
	in.BaseURL, in.Config, in.SchemaVersion, in.ExpectedSchemaVersion = "", map[string]any{"endpoint": "https://supplier.example.test", "username": "account"}, 1, 2
	if _, _, _, _, err := validateSupplierInput(in, &rec); err == nil {
		t.Fatal("stale schema CAS accepted")
	}
	rec.item.Config["secret"] = "misfiled-private-key"
	view := describeSupplierAccount(rec)
	raw, _ := json.Marshal(view)
	if bytes.Contains(raw, []byte("private-key")) || view.ConfigurationStatus != "invalid" {
		t.Fatal("summary leaked a credential or accepted invalid config", string(raw))
	}
	s.ProvisionEncryptionKey = nil
	delete(rec.item.Config, "secret")
	if view := describeSupplierAccount(rec); view.ConfigurationStatus != "valid" || !view.SecureFields["secret"] {
		t.Fatal("read-only metadata required decryption", view)
	}
}
