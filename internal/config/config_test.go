package config

import "testing"

func TestEpayConfigurationRequiresCompleteCredentials(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://app@localhost/example_test")
	t.Setenv("MFA_ENCRYPTION_KEY", "")
	t.Setenv("EPAY_API_URL", "")
	t.Setenv("EPAY_PID", "")
	t.Setenv("EPAY_KEY", "")
	t.Setenv("EPAY_TYPES", "")
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
