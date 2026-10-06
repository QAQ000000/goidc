package store

import (
	"bytes"
	"testing"
)

func TestSupplierSecretsRejectOwnerDomainAndTampering(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	value := map[int64]string{7: "private-fixed-value"}
	sealed, err := supplierSeal(key, "binding-fields", 42, value)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(value[7])) {
		t.Fatal("plaintext persisted")
	}
	var decoded map[int64]string
	if err = supplierUnseal(key, "binding-fields", 42, sealed, &decoded); err != nil || decoded[7] != value[7] {
		t.Fatal("round trip", err)
	}
	for _, test := range []struct {
		domain string
		id     int64
	}{{"account", 42}, {"binding-fields", 43}} {
		if supplierUnseal(key, test.domain, test.id, sealed, &decoded) == nil {
			t.Fatal("cross-owner or cross-domain decryption")
		}
	}
	sealed[len(sealed)-1] ^= 1
	if supplierUnseal(key, "binding-fields", 42, sealed, &decoded) == nil {
		t.Fatal("tampering accepted")
	}
}
