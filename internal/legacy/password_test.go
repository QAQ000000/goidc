package legacy

import (
	"crypto/md5"
	"encoding/hex"
	"testing"
)

func TestCMFPasswordCompatibility(t *testing.T) {
	inner := md5.Sum([]byte("auth-codecorrect horse battery staple"))
	outer := md5.Sum([]byte(hex.EncodeToString(inner[:])))
	raw := "###" + hex.EncodeToString(outer[:])
	wrapped := WrapCMFPasswordHash(raw)
	if !ValidCMFPasswordHash(raw) || !IsCMFPasswordHash(wrapped) || !VerifyCMFPassword("correct horse battery staple", wrapped, "auth-code") {
		t.Fatal("valid ThinkCMF password was rejected")
	}
	if VerifyCMFPassword("wrong", wrapped, "auth-code") || VerifyCMFPassword("correct horse battery staple", wrapped, "wrong-code") {
		t.Fatal("invalid ThinkCMF password was accepted")
	}
}
