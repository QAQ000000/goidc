package store

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestManualResourceInputAndSecretBoundaries(t *testing.T) {
	for _, key := range []string{" short", " leading-key", "trailing-key ", "newline\nkey", "unicode-密码"} {
		if _, err := manualResourceKey(key); err == nil {
			t.Fatal("accepted invalid stable operation key")
		}
	}
	for _, input := range []ManualResourceInput{
		{Name: "inventory", CostCents: -1},
		{Name: "inventory", CostCents: 9000000000000001},
		{Name: "inventory", Address: "192.0.2.1 192.0.2.2"},
		{Name: "inventory", UpstreamExpiresAt: "2026-10-01"},
		{Name: "inventory", Status: "assigned"},
	} {
		if err := normalizeManualResource(&input); err == nil {
			t.Fatal("accepted invalid inventory boundary")
		}
	}
	valid := ManualResourceInput{Name: "inventory", CostCents: 9000000000000000, Address: "2001:db8::1", UpstreamExpiresAt: "2027-01-01T00:00:00+08:00"}
	if err := normalizeManualResource(&valid); err != nil || valid.ResourceType != "server" || valid.Status != "available" {
		t.Fatal("valid inventory metadata failed", err)
	}
	key := bytes.Repeat([]byte{7}, 32)
	private := manualResourceSecret{Username: "inventory-login", Password: "low-entropy-secret"}
	sealed, err := manualResourceSeal(key, 41, private)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(private.Username)) || bytes.Contains(sealed, []byte(private.Password)) {
		t.Fatal("inventory credentials stored in plaintext")
	}
	opened, err := manualResourceUnseal(key, 41, sealed)
	if err != nil || opened != private {
		t.Fatal("inventory credentials did not round trip")
	}
	if _, err = manualResourceUnseal(key, 42, sealed); err == nil {
		t.Fatal("inventory credentials could be moved to another inventory owner")
	}
	if _, err = manualResourceUnseal(bytes.Repeat([]byte{8}, 32), 41, sealed); err == nil {
		t.Fatal("inventory credentials opened with another key")
	}
	if _, err = manualResourceSeal(nil, 41, private); err == nil {
		t.Fatal("private inventory accepted without encryption configuration")
	}
	db := &Store{ProvisionEncryptionKey: key}
	body := ManualResourceInput{Name: "inventory", Username: private.Username, Password: private.Password}
	fingerprint, err := db.manualResourceFingerprint(body, true)
	if err != nil || len(fingerprint) != 64 {
		t.Fatal("secret receipt fingerprint not compatible with unified receipts", err)
	}
	plain, err := db.manualResourceFingerprint(body, false)
	if err != nil || plain == fingerprint {
		t.Fatal("secret receipt permits offline plain SHA256 password guesses")
	}
	again, err := db.manualResourceFingerprint(body, true)
	if err != nil || again != fingerprint {
		t.Fatal("same secret intent lost stable recovery")
	}
	other := &Store{ProvisionEncryptionKey: bytes.Repeat([]byte{9}, 32)}
	changed, err := other.manualResourceFingerprint(body, true)
	if err != nil || changed == fingerprint {
		t.Fatal("secret fingerprint is not keyed")
	}
	db.ProvisionEncryptionKey = nil
	_, err = db.manualResourceFingerprint(body, true)
	var failure *AccountBusinessError
	if !errors.As(err, &failure) || failure.Status != 503 || !strings.HasPrefix(failure.Code, "manual_resource_encryption_") {
		t.Fatal("secret receipt did not fail closed without key")
	}
}
