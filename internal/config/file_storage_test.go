package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestFileStorageDeploymentConfiguration(t *testing.T) {
	t.Setenv("FILE_STORAGE_DEFAULT", "")
	t.Setenv("FILE_STORAGE_PROFILES", "")
	manager, err := loadFileStorage()
	if err != nil {
		t.Fatal(err)
	}
	id, backend, _, err := manager.Default()
	if err != nil || id != "database" || backend != nil {
		t.Fatal("unconfigured deployment must preserve database writes")
	}
	t.Setenv("FILE_STORAGE_DEFAULT", "primary")
	t.Setenv("FILE_STORAGE_PROFILES", fmt.Sprintf(`[{"id":"primary","implementation":"local","config":{"root":%q}}]`, t.TempDir()))
	manager, err = loadFileStorage()
	if err != nil {
		t.Fatal(err)
	}
	id, backend, fingerprint, err := manager.Default()
	if err != nil || id != "primary" || backend == nil || len(fingerprint) != 64 {
		t.Fatal("explicit local storage was not selected")
	}
}

func TestFileStorageDeploymentRejectsInvalidWithoutLeaking(t *testing.T) {
	t.Setenv("FILE_STORAGE_DEFAULT", "missing")
	for _, raw := range []string{
		`[]`, `null`, `[] {}`, `[{"password":"secret-marker"}]`,
		`[{"id":"s3","implementation":"s3","config":{"endpoint":"https://user:secret-marker@example.test"},"secrets":{"secret_key":"secret-marker"}}]`,
		strings.Repeat("secret-marker", 20000),
	} {
		t.Setenv("FILE_STORAGE_PROFILES", raw)
		if _, err := loadFileStorage(); err == nil || strings.Contains(err.Error(), "secret-marker") {
			t.Fatal("invalid deployment configuration accepted or echoed")
		}
	}
}
