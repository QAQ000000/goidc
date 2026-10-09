package legacy

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// LegacyCMFPrefix marks a ThinkCMF password hash kept only until the user's
// first successful Go login upgrades it to bcrypt.
const LegacyCMFPrefix = "legacy_cmf$"

// WrapCMFPasswordHash keeps the legacy hash distinguishable from bcrypt while
// avoiding a second password column or a runtime plugin dependency.
func WrapCMFPasswordHash(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return LegacyCMFPrefix + raw
}

func IsCMFPasswordHash(value string) bool {
	return strings.HasPrefix(value, LegacyCMFPrefix)
}

func ValidCMFPasswordHash(raw string) bool {
	if !strings.HasPrefix(raw, "###") || len(raw) != 35 {
		return false
	}
	for _, c := range raw[3:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// VerifyCMFPassword implements the active ThinkCMF scheme:
// "###" + md5(md5(authcode + password)). The old PHP helper contains an
// unreachable fallback, so imported records use this live format only.
func VerifyCMFPassword(password, wrapped, authCode string) bool {
	if !IsCMFPasswordHash(wrapped) || !ValidCMFPasswordHash(strings.TrimPrefix(wrapped, LegacyCMFPrefix)) || strings.TrimSpace(authCode) == "" {
		return false
	}
	stored := strings.TrimPrefix(wrapped, LegacyCMFPrefix)
	inner := md5.Sum([]byte(authCode + password))
	outer := md5.Sum([]byte(hex.EncodeToString(inner[:])))
	expected := "###" + hex.EncodeToString(outer[:])
	return len(stored) == len(expected) && subtle.ConstantTimeCompare([]byte(stored), []byte(expected)) == 1
}
