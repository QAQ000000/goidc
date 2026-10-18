package filestorage

import (
	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
)

var backends = module.NewRegistry[Backend](channel.Storage)

func init() {
	backends.MustRegister(channel.Descriptor{
		ID: "local", Name: "本地私有文件存储", Category: channel.Storage,
		Capabilities:  []string{"put", "open", "delete", "seek"},
		SchemaVersion: 1, ContractVersion: channel.ContractVersion, InstanceMode: "profile",
		Fields: []channel.Field{{Key: "root", Label: "已存在的绝对根目录", Type: channel.Text, Required: true}},
	}, func(cfg module.Config) (Backend, error) {
		values, _, err := normalizeConfiguration("local", cfg)
		if err != nil {
			return nil, ErrConfiguration
		}
		return newLocal(values["root"].(string))
	})
	backends.MustRegister(channel.Descriptor{
		ID: "s3", Name: "S3 私有对象存储", Category: channel.Storage,
		Capabilities:  []string{"put", "open", "delete", "seek"},
		SchemaVersion: 1, ContractVersion: channel.ContractVersion, InstanceMode: "profile",
		Fields: []channel.Field{
			{Key: "endpoint", Label: "HTTPS Endpoint", Type: channel.Text, Required: true},
			{Key: "region", Label: "区域", Type: channel.Text, Required: true, Default: "us-east-1"},
			{Key: "bucket", Label: "私有 Bucket", Type: channel.Text, Required: true},
			{Key: "prefix", Label: "对象键前缀", Type: channel.Text},
			{Key: "access_key", Label: "Access Key", Type: channel.Text, Required: true, Secret: true},
			{Key: "secret_key", Label: "Secret Key", Type: channel.Text, Required: true, Secret: true},
			{Key: "session_token", Label: "Session Token", Type: channel.Text, Secret: true},
		},
	}, func(cfg module.Config) (Backend, error) {
		values, secrets, err := normalizeConfiguration("s3", cfg)
		if err != nil {
			return nil, ErrConfiguration
		}
		return newS3(values, secrets)
	})
	channel.MustRegisterConfigValidator(channel.Storage, "local", func(values map[string]any, _ map[string]bool) []channel.ValidationError {
		if _, err := canonicalLocalRoot(values["root"].(string)); err != nil {
			return []channel.ValidationError{{Field: "root", Code: "invalid_directory"}}
		}
		return nil
	})
	channel.MustRegisterConfigValidator(channel.Storage, "s3", func(values map[string]any, _ map[string]bool) []channel.ValidationError {
		if _, err := parseS3Config(values, nil, false); err != nil {
			return []channel.ValidationError{{Field: "endpoint", Code: "invalid_storage_configuration"}}
		}
		return nil
	})
}

func normalizeConfiguration(implementation string, cfg module.Config) (map[string]any, map[string]string, error) {
	entry, exists := backends.Lookup(implementation)
	if !exists || entry.Descriptor.SchemaVersion != 1 || entry.Descriptor.InstanceMode != "profile" || cfg.Secret != "" || cfg.Payload != nil {
		return nil, nil, ErrConfiguration
	}
	configured := make(map[string]bool, len(cfg.Secrets))
	allowedSecrets := make(map[string]bool)
	for _, key := range channel.SecretKeys(entry.Descriptor) {
		allowedSecrets[key] = true
	}
	secrets := make(map[string]string, len(cfg.Secrets))
	for key, value := range cfg.Secrets {
		if !allowedSecrets[key] {
			return nil, nil, ErrConfiguration
		}
		configured[key] = value != ""
		secrets[key] = value
	}
	values, issues := channel.ValidateConfiguration(entry.Descriptor, cfg.Values, configured, true)
	if len(issues) != 0 {
		return nil, nil, ErrConfiguration
	}
	if implementation == "s3" {
		if _, err := parseS3Config(values, secrets, true); err != nil {
			return nil, nil, ErrConfiguration
		}
	}
	return values, secrets, nil
}
