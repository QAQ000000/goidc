package store

import (
	"bytes"
	"testing"
)

func TestSMSSecretDomainsAndValidation(t *testing.T) {
	key := bytes.Repeat([]byte{42}, 32)
	plain := []byte("123456")
	sealed, e := smsCrypt(key, plain, "body:+8613800138000:challenge", true)
	if e != nil || bytes.Contains(sealed, plain) {
		t.Fatal("payload not encrypted", e)
	}
	out, e := smsCrypt(key, sealed, "body:+8613800138000:challenge", false)
	if e != nil || !bytes.Equal(out, plain) {
		t.Fatal("decrypt failed", e)
	}
	for _, domain := range []string{"token", "body:+8613800138001:challenge", "body:+8613800138000:other"} {
		if _, e = smsCrypt(key, sealed, domain, false); e == nil {
			t.Fatal("wrong domain accepted")
		}
	}
	for _, v := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/#fragment", "file:///tmp/secret"} {
		if ValidSMSEndpoint(v) {
			t.Fatal("invalid endpoint", v)
		}
	}
	for _, v := range []string{"https://example.com/sms", "http://127.0.0.1:1234/sms", "http://[::1]:1234/sms"} {
		if !ValidSMSEndpoint(v) {
			t.Fatal("valid endpoint rejected", v)
		}
	}
	for _, v := range []string{"13800138000", "+013800138000", "+8613800138000\n", "+1"} {
		if ValidSMSRecipient(v) {
			t.Fatal("invalid recipient", v)
		}
	}
}
