package config

import (
	"strings"
	"testing"
)

func TestNOKVMConfigurationRejectsPartialAndDuplicateProfiles(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://app@localhost/example_test")
	for _, key := range []string{"MFA_ENCRYPTION_KEY", "EPAY_API_URL", "EPAY_PID", "EPAY_KEY", "EPAY_TYPES", "NOKVM_PROFILES", "PROVISION_ENCRYPTION_KEY"} {
		t.Setenv(key, "")
	}
	if cfg, err := Load(); err != nil || len(cfg.NOKVMProfiles) != 0 {
		t.Fatal("optional supplier configuration", err)
	}
	profile := `{"id":"primary","name":"主供应商","url":"https://supplier.example","token":"private-token"}`
	t.Setenv("NOKVM_PROFILES", "["+profile+"]")
	if _, err := Load(); err == nil {
		t.Fatal("accepted supplier without encryption key")
	}
	t.Setenv("PROVISION_ENCRYPTION_KEY", strings.Repeat("01", 32))
	if cfg, err := Load(); err != nil || len(cfg.NOKVMProfiles) != 1 {
		t.Fatal("valid supplier profile", err)
	}
	t.Setenv("NOKVM_PROFILES", "["+profile+","+profile+"]")
	if _, err := Load(); err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatal("duplicate profile or unsafe error", err)
	}
	t.Setenv("NOKVM_PROFILES", `[{"id":"bad","name":"bad","url":"http://supplier.example","token":"private-token"}]`)
	if _, err := Load(); err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatal("unsafe supplier URL or unsafe error", err)
	}
}
