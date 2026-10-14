package config

import (
	"net"
	"testing"
)

func resetSecurityEnv(t *testing.T) {
	t.Helper()
	t.Setenv("COOKIE_SECURE", "")
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	t.Setenv("PUBLIC_ORIGIN", "http://localhost:3088")
	t.Setenv("PUBLIC_ORIGINS", "")
}

func TestMockPaymentRequiresExplicitEnablement(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://app@localhost/example_test")
	t.Setenv("PAYMENT_MOCK_ENABLED", "")
	cfg, err := Load()
	if err != nil || cfg.PaymentMockEnabled {
		t.Fatal("mock payment must default to disabled", err)
	}
	for _, enabled := range []bool{false, true} {
		value := "false"
		if enabled {
			value = "true"
		}
		t.Setenv("PAYMENT_MOCK_ENABLED", value)
		cfg, err = Load()
		if err != nil || cfg.PaymentMockEnabled != enabled {
			t.Fatal("explicit mock payment configuration ignored", value, err)
		}
	}
	t.Setenv("PAYMENT_MOCK_ENABLED", "enabled")
	if _, err = Load(); err == nil {
		t.Fatal("invalid mock payment configuration accepted")
	}
}

func TestEpayConfigurationRequiresCompleteCredentials(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://app@localhost/example_test")
	t.Setenv("MFA_ENCRYPTION_KEY", "")
	t.Setenv("EPAY_API_URL", "")
	t.Setenv("EPAY_PID", "")
	t.Setenv("EPAY_KEY", "")
	t.Setenv("EPAY_TYPES", "")
	resetSecurityEnv(t)
	if _, err := Load(); err != nil {
		t.Fatal("optional epay changed default configuration", err)
	}
	t.Setenv("EPAY_API_URL", "https://pay.example")
	if _, err := Load(); err == nil {
		t.Fatal("partial epay configuration accepted")
	}
	t.Setenv("EPAY_PID", "10001")
	t.Setenv("EPAY_KEY", "test-secret")
	t.Setenv("PUBLIC_ORIGIN", "https://zjmf.example")
	t.Setenv("COOKIE_SECURE", "true")
	if cfg, err := Load(); err != nil || len(cfg.EpayTypes) != 1 || cfg.EpayTypes[0] != "alipay" {
		t.Fatal("valid epay configuration", cfg.EpayTypes, err)
	}
	t.Setenv("EPAY_TYPES", "alipay,wxpay")
	if cfg, err := Load(); err != nil || len(cfg.EpayTypes) != 2 {
		t.Fatal("valid epay type list", err)
	}
	for _, value := range []string{"unknown", "alipay,alipay", "alipay,"} {
		t.Setenv("EPAY_TYPES", value)
		if _, err := Load(); err == nil {
			t.Fatal("invalid type list accepted", value)
		}
	}
}

func TestPublicOrigins(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://app@localhost/example_test")
	t.Setenv("PUBLIC_ORIGIN", "http://localhost:3088/")
	t.Setenv("PUBLIC_ORIGINS", "http://localhost:11750, http://localhost:3088/")
	cfg, err := Load()
	if err != nil || len(cfg.PublicOrigins) != 2 || cfg.PublicOrigins[0] != "http://localhost:3088" || cfg.PublicOrigins[1] != "http://localhost:11750" {
		t.Fatalf("origins not normalized: %#v, %v", cfg.PublicOrigins, err)
	}
	t.Setenv("PUBLIC_ORIGINS", "https://example.test/path")
	if _, err := Load(); err == nil {
		t.Fatal("path-bearing origin accepted")
	}
}

func TestCookieSecureParsingAcceptsCommonSpellings(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://app@localhost/example_test")
	resetSecurityEnv(t)
	for _, value := range []string{"true", "TRUE", "True", "1", "yes", "YES", "on", "enabled"} {
		t.Setenv("COOKIE_SECURE", value)
		cfg, err := Load()
		if err != nil || !cfg.SecureCookie {
			t.Fatalf("truthy COOKIE_SECURE=%q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"", "false", "FALSE", "0", "no", "off", "disabled"} {
		t.Setenv("COOKIE_SECURE", value)
		cfg, err := Load()
		if err != nil || cfg.SecureCookie {
			t.Fatalf("falsey COOKIE_SECURE=%q misread as secure: %v", value, err)
		}
	}
	t.Setenv("COOKIE_SECURE", "maybe")
	if _, err := Load(); err == nil {
		t.Fatal("unparseable COOKIE_SECURE silently accepted")
	}
}

func TestHTTPSOriginRequiresSecureCookie(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://app@localhost/example_test")
	resetSecurityEnv(t)
	t.Setenv("PUBLIC_ORIGIN", "https://zjmf.example")
	if _, err := Load(); err == nil {
		t.Fatal("https PUBLIC_ORIGIN started without COOKIE_SECURE")
	}
	for _, value := range []string{"true", "1", "yes"} {
		t.Setenv("COOKIE_SECURE", value)
		cfg, err := Load()
		if err != nil || !cfg.SecureCookie {
			t.Fatalf("https origin with COOKIE_SECURE=%q rejected: %v", value, err)
		}
	}
	// An https entry in the additional PUBLIC_ORIGINS list is a browser origin
	// only; it does not force the cookie flag, matching the previous behaviour.
	resetSecurityEnv(t)
	t.Setenv("PUBLIC_ORIGINS", "https://admin.example")
	if _, err := Load(); err != nil {
		t.Fatal("PUBLIC_ORIGINS entry changed cookie requirements", err)
	}
}

func TestTrustedProxyCIDRs(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://app@localhost/example_test")
	resetSecurityEnv(t)
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	cfg, err := Load()
	if err != nil || len(cfg.TrustedProxyCIDRs) != 0 {
		t.Fatal("empty TRUSTED_PROXY_CIDRS must trust no proxy", err)
	}
	t.Setenv("TRUSTED_PROXY_CIDRS", "10.0.0.0/8, 192.168.0.0/16 ,2001:db8::/32")
	cfg, err = Load()
	if err != nil || len(cfg.TrustedProxyCIDRs) != 3 {
		t.Fatalf("valid CIDR list rejected: %d, %v", len(cfg.TrustedProxyCIDRs), err)
	}
	if !cfg.TrustedProxyCIDRs[0].Contains(net.ParseIP("10.1.2.3")) || cfg.TrustedProxyCIDRs[0].Contains(net.ParseIP("11.0.0.1")) {
		t.Fatal("CIDR membership misparsed")
	}
	// A bare address is accepted as a single-host CIDR.
	t.Setenv("TRUSTED_PROXY_CIDRS", "127.0.0.1")
	cfg, err = Load()
	if err != nil || len(cfg.TrustedProxyCIDRs) != 1 || !cfg.TrustedProxyCIDRs[0].Contains(net.ParseIP("127.0.0.1")) || cfg.TrustedProxyCIDRs[0].Contains(net.ParseIP("127.0.0.2")) {
		t.Fatal("bare trusted proxy address misparsed", err)
	}
	for _, value := range []string{"10.0.0.0/33", "not-a-cidr", "10.0.0.0/8,,/8"} {
		t.Setenv("TRUSTED_PROXY_CIDRS", value)
		if _, err := Load(); err == nil {
			t.Fatal("invalid TRUSTED_PROXY_CIDRS accepted", value)
		}
	}
}
