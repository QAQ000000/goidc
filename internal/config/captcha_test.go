package config

import (
	"strings"
	"testing"
)

func TestCaptchaDeploymentConfiguration(t *testing.T) {
	t.Setenv("CAPTCHA_DEFAULT", "")
	t.Setenv("CAPTCHA_PROFILES", "")
	manager, err := loadCaptcha()
	if err != nil {
		t.Fatal(err)
	}
	binding, provider, err := manager.Active()
	if err != nil || provider == nil || binding.ProfileID != "builtin" || binding.ImplementationID != "builtin_png" || len(binding.Fingerprint) != 64 {
		t.Fatal("default captcha missing", err)
	}
	t.Setenv("CAPTCHA_DEFAULT", "alternate")
	t.Setenv("CAPTCHA_PROFILES", `[{"id":"alternate","implementation_id":"builtin_png","config":{}}]`)
	manager, err = loadCaptcha()
	if err != nil {
		t.Fatal(err)
	}
	binding, _, err = manager.Active()
	if err != nil || binding.ProfileID != "alternate" {
		t.Fatal("explicit profile not selected", err)
	}
}

func TestCaptchaDeploymentRejectsInvalidWithoutLeaking(t *testing.T) {
	t.Setenv("CAPTCHA_DEFAULT", "builtin")
	for _, raw := range []string{
		`null`, `[] {}`, `[{"password":"secret-marker"}]`,
		`[{"id":"builtin","implementation_id":"builtin_png"}]`,
		`[{"id":"other","implementation_id":"uncompiled","secrets":{"key":"secret-marker"}}]`,
		`[{"id":"other","implementation_id":"builtin_png","config":{"unexpected":"secret-marker"}}]`,
		strings.Repeat("secret-marker", 20000),
	} {
		t.Setenv("CAPTCHA_PROFILES", raw)
		if _, err := loadCaptcha(); err == nil || strings.Contains(err.Error(), "secret-marker") {
			t.Fatal("invalid deployment accepted or echoed", err)
		}
	}
	t.Setenv("CAPTCHA_PROFILES", "")
	t.Setenv("CAPTCHA_DEFAULT", "missing")
	if _, err := loadCaptcha(); err == nil {
		t.Fatal("missing default accepted")
	}
}
