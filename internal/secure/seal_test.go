package secure_test

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"testing"

	"github.com/codex/zjmfgo/internal/secure"
)

func TestOpenKnownGCMVectors(t *testing.T) {
	// NIST GCM vectors: zero key, 96-bit zero IV and one zero plaintext block.
	for _, v := range []struct {
		keySize int
		sealed  string
	}{
		{16, "0388dace60b6a392f328c2b971b2fe78ab6e47d42cec13bdf53a67b21257bddf"},
		{32, "cea7403d4d606b6e074ec5d3baf39d18d0d1c8a799996bf0265b98b5d48ab919"},
	} {
		raw, err := hex.DecodeString(v.sealed)
		if err != nil {
			t.Fatal(err)
		}
		got, err := secure.Open(make([]byte, v.keySize), nil, append(make([]byte, 12), raw...))
		if err != nil || !bytes.Equal(got, make([]byte, 16)) {
			t.Fatalf("AES-%d known ciphertext: %x, %v", v.keySize*8, got, err)
		}
	}
}

func TestSealLegacyCompatibilityAndAuthentication(t *testing.T) {
	for _, size := range []int{16, 24, 32} {
		key := bytes.Repeat([]byte{7}, size)
		aad := []byte("supplier:password:42")
		plain := []byte("legacy credential")
		block, err := aes.NewCipher(key)
		if err != nil {
			t.Fatal(err)
		}
		legacy, err := cipher.NewGCM(block)
		if err != nil {
			t.Fatal(err)
		}
		nonce := bytes.Repeat([]byte{3}, legacy.NonceSize())
		old := legacy.Seal(bytes.Clone(nonce), nonce, plain, aad)
		got, err := secure.Open(key, aad, old)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("old ciphertext rejected: %v", err)
		}
		sealed, err := secure.Seal(key, aad, plain)
		if err != nil {
			t.Fatal(err)
		}
		got, err = legacy.Open(nil, sealed[:legacy.NonceSize()], sealed[legacy.NonceSize():], aad)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("new ciphertext incompatible: %v", err)
		}
		if string(plain) != "legacy credential" || string(aad) != "supplier:password:42" {
			t.Fatal("caller input modified")
		}
		original := bytes.Clone(sealed)
		if _, err = secure.Open(key, []byte("supplier:password:43"), sealed); err == nil {
			t.Fatal("wrong owner accepted")
		}
		wrongKey := bytes.Clone(key)
		wrongKey[0] ^= 1
		if _, err = secure.Open(wrongKey, aad, sealed); err == nil {
			t.Fatal("wrong key accepted")
		}
		for n := 0; n < len(sealed); n++ {
			if _, err = secure.Open(key, aad, sealed[:n]); err == nil {
				t.Fatalf("truncation at %d accepted", n)
			}
			tampered := bytes.Clone(sealed)
			tampered[n] ^= 1
			if _, err = secure.Open(key, aad, tampered); err == nil {
				t.Fatalf("tampering at %d accepted", n)
			}
		}
		if !bytes.Equal(original, sealed) {
			t.Fatal("Open modified ciphertext")
		}
		second, err := secure.Seal(key, aad, plain)
		if err != nil || bytes.Equal(sealed[:legacy.NonceSize()], second[:legacy.NonceSize()]) {
			t.Fatalf("nonce reused: %v", err)
		}
		empty, err := secure.Seal(key, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got, err = secure.Open(key, nil, empty); err != nil || len(got) != 0 {
			t.Fatalf("empty payload failed: %v", err)
		}
	}
	if _, err := secure.Seal([]byte("short"), nil, nil); err == nil {
		t.Fatal("invalid key accepted by Seal")
	}
	if _, err := secure.Open([]byte("short"), nil, nil); err == nil {
		t.Fatal("invalid key accepted by Open")
	}
}
