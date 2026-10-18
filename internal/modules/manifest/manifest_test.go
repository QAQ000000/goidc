package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestDefaultProfileMatchesCompiledModules(t *testing.T) {
	raw, err := os.ReadFile("../../../audit/module-build-profile.json")
	if err != nil {
		t.Fatal(err)
	}
	var profile struct {
		Profile         string   `json:"profile"`
		ContractVersion int      `json:"contract_version"`
		Modules         []Module `json:"modules"`
	}
	if err = json.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	api, worker := Current("api"), Current("worker")
	if api.Profile != profile.Profile || api.ContractVersion != profile.ContractVersion || worker.Profile != api.Profile || worker.ContractVersion != api.ContractVersion || !reflect.DeepEqual(api.Modules, worker.Modules) {
		t.Fatalf("API/worker assembly contract differs: %+v / %+v", api, worker)
	}
	required := map[string]bool{"bot/telegram_bot": false, "captcha/builtin_png": false}
	for i := range api.Modules {
		key := string(api.Modules[i].Category) + "/" + api.Modules[i].ID
		if _, needed := required[key]; needed {
			if !api.Modules[i].FactoryAvailable {
				t.Fatalf("required module factory missing: %s", key)
			}
			required[key] = true
		}
		if len(api.Modules[i].DescriptorSHA256) != 64 {
			t.Fatalf("missing schema digest: %+v", api.Modules[i])
		}
		api.Modules[i].DescriptorSHA256 = ""
	}
	for key, present := range required {
		if !present {
			t.Errorf("required module missing from default profile: %s", key)
		}
	}
	if !reflect.DeepEqual(api.Modules, profile.Modules) {
		t.Fatalf("actual composition differs from required profile:\n%+v\n%+v", api.Modules, profile.Modules)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestManifestCLIIsConfigurationIndependentAndStrict(t *testing.T) {
	t.Setenv("DATABASE_URL", "secret-marker-not-a-database-url")
	t.Setenv("NOKVM_PROFILES", "secret-marker-invalid-profile")
	var buffer bytes.Buffer
	handled, err := WriteCLI([]string{"--module-manifest"}, &buffer, "worker")
	if err != nil || !handled {
		t.Fatalf("manifest depended on runtime configuration: %v", err)
	}
	if bytes.Contains(buffer.Bytes(), []byte("secret-marker")) {
		t.Fatal("runtime configuration leaked into public manifest")
	}
	var result Manifest
	if err = json.Unmarshal(buffer.Bytes(), &result); err != nil || result.Binary != "worker" || result.Format != Format {
		t.Fatalf("invalid manifest: %+v %v", result, err)
	}
	if handled, err = WriteCLI(nil, &buffer, "api"); handled || err != nil {
		t.Fatalf("normal startup intercepted: %v %v", handled, err)
	}
	if handled, err = WriteCLI([]string{"--module-manifest", "unexpected"}, &buffer, "api"); !handled || err == nil {
		t.Fatal("mixed manifest arguments accepted")
	}
	if handled, err = WriteCLI([]string{"--module-manifest"}, failingWriter{}, "api"); !handled || err == nil {
		t.Fatal("output failure was ignored")
	}
}
