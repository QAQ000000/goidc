package store

import (
	"bytes"
	"testing"

	"github.com/codex/zjmfgo/internal/provision"
)

func TestPaymentSecretEncryptionAndDomainIsolation(t *testing.T) {
	s := &Store{ProvisionEncryptionKey: []byte("01234567890123456789012345678901")}
	ciphertext, err := s.paymentSecret([]byte("merchant-test-secret"), true)
	if err != nil || bytes.Contains([]byte(ciphertext), []byte("merchant-test-secret")) {
		t.Fatal("secret sealing failed", err)
	}
	value, err := s.paymentSecret([]byte(ciphertext), false)
	if err != nil || value != "merchant-test-secret" {
		t.Fatal("secret unsealing failed", err)
	}
	tampered := []byte(ciphertext)
	tampered[len(tampered)-1] ^= 1
	if _, err = s.paymentSecret(tampered, false); err == nil {
		t.Fatal("tampered secret accepted")
	}
	if _, err = provision.Unseal(s.ProvisionEncryptionKey, 1, []byte(ciphertext)); err == nil {
		t.Fatal("payment ciphertext accepted by provision domain")
	}
	provisionSecret, err := provision.Seal(s.ProvisionEncryptionKey, 1, provision.Credentials{Password: "provision-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.paymentSecret(provisionSecret, false); err == nil {
		t.Fatal("provision ciphertext accepted by payment domain")
	}
	s.ProvisionEncryptionKey = []byte("11234567890123456789012345678901")
	if _, err = s.paymentSecret([]byte(ciphertext), false); err == nil {
		t.Fatal("wrong encryption key accepted")
	}
}

func TestPaymentCredentialsBoundToReference(t *testing.T) {
	s := &Store{ProvisionEncryptionKey: []byte("01234567890123456789012345678901")}
	raw := []byte("merchant-test-identity")
	sealed, err := s.paymentCredentials("payment-a", raw, true)
	if err != nil || bytes.Contains(sealed, raw) {
		t.Fatal("credential sealing failed", err)
	}
	decoded, err := s.paymentCredentials("payment-a", sealed, false)
	if err != nil || !bytes.Equal(decoded, raw) {
		t.Fatal("credential opening failed", err)
	}
	if _, err = s.paymentCredentials("payment-b", sealed, false); err == nil {
		t.Fatal("credentials transplanted to another payment")
	}
	if _, err = s.paymentSecret(sealed, false); err == nil {
		t.Fatal("transaction credentials accepted in settings domain")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err = s.paymentCredentials("payment-a", sealed, false); err == nil {
		t.Fatal("tampered credentials accepted")
	}
}
