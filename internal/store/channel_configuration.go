package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/secure"
	"github.com/jackc/pgx/v5"
)

const channelInstanceColumns = `id,category,implementation_id,display_name,enabled,sort_order,config,secret_ciphertext,secret_aad,version,created_at,updated_at,schema_version,secret_fields`

type channelRecord struct {
	item   ChannelInstance
	secret []byte
	aad    string
	fields map[string][]byte
}

func scanChannelRecord(row interface{ Scan(...any) error }) (channelRecord, error) {
	var rec channelRecord
	var config, fields []byte
	i := &rec.item
	err := row.Scan(&i.ID, &i.Category, &i.ImplementationID, &i.DisplayName, &i.Enabled, &i.SortOrder, &config, &rec.secret, &rec.aad, &i.Version, &i.CreatedAt, &i.UpdatedAt, &i.SchemaVersion, &fields)
	if err != nil {
		return rec, err
	}
	if err = json.Unmarshal(config, &i.Config); err != nil {
		return rec, err
	}
	if err = json.Unmarshal(fields, &rec.fields); err != nil {
		return rec, err
	}
	if rec.fields == nil {
		rec.fields = map[string][]byte{}
	}
	return rec, nil
}

func credentialFlags(rec channelRecord, desc channel.Descriptor) map[string]bool {
	flags := map[string]bool{}
	for _, key := range channel.SecretKeys(desc) {
		flags[key] = len(rec.fields[key]) > 0
	}
	keys := channel.SecretKeys(desc)
	if len(keys) == 1 {
		flags[keys[0]] = len(rec.secret) > 0
	}
	return flags
}

func describeChannelRecord(rec channelRecord) ChannelInstance {
	i := rec.item
	i.SecureFields = map[string]bool{}
	i.SecretConfigured = len(rec.secret) > 0 || len(rec.fields) > 0
	desc, ok := channel.Lookup(channel.Category(i.Category), i.ImplementationID)
	if !ok {
		i.ConfigurationStatus = "implementation_missing"
		i.Config = map[string]any{}
		return i
	}
	i.FactoryAvailable = channel.HasFactory(desc.Category, desc.ID)
	i.SecureFields = credentialFlags(rec, desc)
	normalized, validationErrors := channel.ValidateConfiguration(desc, i.Config, i.SecureFields, i.Enabled)
	i.ValidationErrors = validationErrors
	i.Config = normalized
	// A previously misfiled credential must never be echoed by read APIs.
	for _, field := range desc.Fields {
		if field.Secret {
			delete(i.Config, field.Key)
		}
	}
	switch {
	case desc.InstanceMode != "channel":
		i.ConfigurationStatus = "external"
	case !i.FactoryAvailable:
		i.ConfigurationStatus = "implementation_missing"
	case i.SchemaVersion != desc.SchemaVersion:
		i.ConfigurationStatus = "schema_incompatible"
		i.Config = map[string]any{}
	case len(i.ValidationErrors) > 0:
		i.ConfigurationStatus = "invalid"
	default:
		i.ConfigurationStatus = "valid"
	}
	return i
}

func (s *Store) openChannelSingle(rec channelRecord) (string, error) {
	if len(rec.secret) == 0 {
		return "", nil
	}
	i := rec.item
	if rec.aad != "" && rec.aad != "zjmfgo:channel-instance:v1:"+i.ID {
		var value string
		var err error
		switch {
		case i.ID == "payment-default" && i.Category == string(channel.Payment) && i.ImplementationID == "epay" && rec.aad == "zjmfgo:payment-settings:epay-key:v1":
			value, err = s.paymentSecret(rec.secret, false)
		case i.ID == "mail-smtp-default" && i.Category == string(channel.Mail) && i.ImplementationID == "smtp" && rec.aad == "zjmfgo:smtp-settings:password:v1":
			value, err = smtpOpen(s.ProvisionEncryptionKey, rec.secret)
		case i.ID == "sms-default" && i.Category == string(channel.SMS) && rec.aad == "sms:token":
			var raw []byte
			raw, err = smsCrypt(s.ProvisionEncryptionKey, rec.secret, "token", false)
			value = string(raw)
		default:
			return "", errors.New("channel secret unavailable")
		}
		if err != nil {
			return "", errors.New("channel secret unavailable")
		}
		return value, nil
	}
	plain, err := channelSeal(s.ProvisionEncryptionKey, i.ID, string(rec.secret), false)
	if err != nil {
		return "", errors.New("channel secret unavailable")
	}
	return string(plain), nil
}

func (s *Store) openChannelSecrets(rec channelRecord, desc channel.Descriptor) (map[string]string, error) {
	out := map[string]string{}
	keys := channel.SecretKeys(desc)
	if len(keys) == 1 {
		value, err := s.openChannelSingle(rec)
		if err != nil {
			return nil, err
		}
		if value != "" {
			out[keys[0]] = value
		}
		return out, nil
	}
	for _, key := range keys {
		sealed := rec.fields[key]
		if len(sealed) == 0 {
			continue
		}
		if len(s.ProvisionEncryptionKey) != 32 {
			return nil, errors.New("channel secret unavailable")
		}
		plain, err := secure.Open(s.ProvisionEncryptionKey, credentialAAD(rec.item.ID, key), sealed)
		if err != nil {
			return nil, errors.New("channel secret unavailable")
		}
		out[key] = string(plain)
	}
	return out, nil
}

func credentialAAD(id, key string) []byte {
	return []byte("zjmfgo:channel-credential:v1:" + id + ":" + key)
}

func (s *Store) ChannelInstanceSecrets(ctx context.Context, id string) (map[string]string, error) {
	rec, err := scanChannelRecord(s.Pool.QueryRow(ctx, "SELECT "+channelInstanceColumns+" FROM channel_instances WHERE id=$1", id))
	if err != nil {
		return nil, err
	}
	desc, ok := channel.Lookup(channel.Category(rec.item.Category), rec.item.ImplementationID)
	if !ok {
		return nil, conflict("channel_not_registered", "渠道实现未注册")
	}
	return s.openChannelSecrets(rec, desc)
}

// RuntimeChannelInstance resolves one immutable row snapshot. This performs no
// network operations and never returns secret material through an HTTP DTO.
func (s *Store) RuntimeChannelInstance(ctx context.Context, id string, category channel.Category) (module.Config, string, error) {
	rec, err := scanChannelRecord(s.Pool.QueryRow(ctx, "SELECT "+channelInstanceColumns+" FROM channel_instances WHERE id=$1", id))
	if err != nil {
		return module.Config{}, "", err
	}
	return s.runtimeChannelRecord(rec, category)
}

// RuntimeChannelInstanceTx shares the caller's connection and locks the
// configuration through commit. Callers lock their actor/credential first.
func (s *Store) RuntimeChannelInstanceTx(ctx context.Context, tx pgx.Tx, id string, category channel.Category) (module.Config, string, error) {
	rec, err := scanChannelRecord(tx.QueryRow(ctx, "SELECT "+channelInstanceColumns+" FROM channel_instances WHERE id=$1 FOR SHARE", id))
	if err != nil {
		return module.Config{}, "", err
	}
	return s.runtimeChannelRecord(rec, category)
}

func (s *Store) runtimeChannelRecord(rec channelRecord, category channel.Category) (module.Config, string, error) {
	i := rec.item
	desc, ok := channel.Lookup(category, i.ImplementationID)
	if i.Category != string(category) || !i.Enabled || !ok || desc.InstanceMode != "channel" || !channel.HasFactory(category, i.ImplementationID) {
		return module.Config{}, "", conflict("channel_unavailable", "渠道未启用或实现不可用")
	}
	if i.SchemaVersion != desc.SchemaVersion {
		return module.Config{}, "", conflict("channel_schema_incompatible", "渠道配置版本不兼容，需要显式迁移")
	}
	values, validationErrors := channel.ValidateConfiguration(desc, i.Config, credentialFlags(rec, desc), true)
	if len(validationErrors) > 0 {
		return module.Config{}, "", conflict("invalid_channel_config", "渠道配置无效")
	}
	secrets, err := s.openChannelSecrets(rec, desc)
	if err != nil {
		return module.Config{}, "", conflict("channel_credentials_unavailable", "渠道凭据不可用")
	}
	if len(channel.ValidateSecretValues(desc, secrets)) > 0 {
		return module.Config{}, "", conflict("invalid_channel_secret", "渠道凭据格式无效")
	}
	cfg := module.Config{Values: values, Secrets: secrets}
	if keys := channel.SecretKeys(desc); len(keys) == 1 {
		cfg.Secret = secrets[keys[0]]
	}
	return cfg, i.ImplementationID, nil
}

func (s *Store) mergeChannelCredentials(rec channelRecord, desc channel.Descriptor, in ChannelInstanceInput) ([]byte, string, []byte, error) {
	keys := channel.SecretKeys(desc)
	known := map[string]bool{}
	for _, key := range keys {
		known[key] = true
	}
	replacements := map[string]string{}
	clears := map[string]bool{}
	for key, value := range in.SecureConfig {
		if !known[key] || len(value) > 4096 {
			return nil, "", nil, conflict("invalid_channel_secret", "凭据字段或长度无效")
		}
		if value != "" {
			replacements[key] = value
		}
	}
	for _, key := range in.ClearSecureFields {
		if !known[key] || clears[key] {
			return nil, "", nil, conflict("invalid_channel_secret", "清除凭据字段无效")
		}
		clears[key] = true
	}
	if (in.Secret != nil && *in.Secret != "") || in.ClearSecret {
		if len(keys) != 1 {
			return nil, "", nil, conflict("invalid_channel_secret", "请按字段设置或清除凭据")
		}
		if len(in.SecureConfig) > 0 || len(in.ClearSecureFields) > 0 {
			return nil, "", nil, conflict("invalid_channel_secret", "不能混用两种凭据写入格式")
		}
		if in.ClearSecret {
			clears[keys[0]] = true
		}
		if in.Secret != nil && *in.Secret != "" {
			replacements[keys[0]] = *in.Secret
		}
	}
	for key := range clears {
		if replacements[key] != "" {
			return nil, "", nil, conflict("invalid_channel_secret", "不能同时替换和清除同一凭据")
		}
	}
	if len(channel.ValidateSecretValues(desc, replacements)) > 0 {
		return nil, "", nil, conflict("invalid_channel_secret", "凭据格式无效")
	}
	sealed, aad := rec.secret, rec.aad
	if sealed == nil {
		sealed = []byte{}
	}
	fields := map[string][]byte{}
	for key, value := range rec.fields {
		fields[key] = value
	}
	if len(keys) == 1 {
		key := keys[0]
		if clears[key] {
			sealed, aad = []byte{}, ""
		}
		if value := replacements[key]; value != "" {
			var err error
			sealed, err = channelSeal(s.ProvisionEncryptionKey, rec.item.ID, value, true)
			if err != nil {
				return nil, "", nil, err
			}
			aad = "zjmfgo:channel-instance:v1:" + rec.item.ID
		}
		fields = map[string][]byte{}
	} else {
		sealed, aad = []byte{}, ""
		for key := range clears {
			delete(fields, key)
		}
		for key, value := range replacements {
			if len(s.ProvisionEncryptionKey) != 32 {
				return nil, "", nil, conflict("channel_encryption_unconfigured", "渠道凭据加密密钥未配置")
			}
			cipher, err := secure.Seal(s.ProvisionEncryptionKey, credentialAAD(rec.item.ID, key), []byte(value))
			if err != nil {
				return nil, "", nil, errors.New("channel secret unavailable")
			}
			fields[key] = cipher
		}
	}
	flags := credentialFlags(channelRecord{item: rec.item, secret: sealed, fields: fields}, desc)
	if len(channel.ValidateSecrets(desc, flags, in.Enabled)) > 0 {
		return nil, "", nil, conflict("invalid_channel_secret", "启用渠道需要必填凭据")
	}
	if _, errs := channel.ValidateConfiguration(desc, in.Config, flags, in.Enabled); len(errs) > 0 {
		return nil, "", nil, conflict("invalid_channel_config", "启用前配置校验失败")
	}
	// VAPID keys form one identity. Validate the complete proposed pair locally,
	// including an unchanged encrypted private key during a public-key edit.
	if in.Enabled && desc.Category == channel.Push && desc.ID == "web_push" {
		proposed := channelRecord{item: rec.item, secret: sealed, aad: aad, fields: fields}
		secrets, err := s.openChannelSecrets(proposed, desc)
		if err != nil {
			return nil, "", nil, conflict("invalid_channel_secret", "浏览器推送凭据不可用")
		}
		if _, err = channel.New(channel.Push, desc.ID, module.Config{Values: in.Config, Secrets: secrets}); err != nil {
			return nil, "", nil, conflict("invalid_channel_config", "浏览器推送公私钥不匹配或配置无效")
		}
	}
	encoded, err := json.Marshal(fields)
	return sealed, aad, encoded, err
}

func onlyDisableChannel(in ChannelInstanceInput) bool {
	return !in.Enabled && in.Config == nil && in.DisplayName == "" && in.Category == "" && in.ImplementationID == "" &&
		in.SortOrder == 0 && in.SchemaVersion == 0 && in.Secret == nil && !in.ClearSecret && len(in.SecureConfig) == 0 && len(in.ClearSecureFields) == 0
}

func configFromJSON(raw []byte) map[string]any {
	var values map[string]any
	_ = json.Unmarshal(raw, &values)
	return values
}

func (s *Store) disableLegacyChannel(ctx context.Context, tx pgx.Tx, id string) error {
	var query string
	switch id {
	case "payment-default":
		query = "UPDATE payment_settings SET enabled=false,version=version+1,updated_at=now() WHERE id=1"
	case "payment-mock-default":
		query = "UPDATE payment_settings SET mock_enabled=false,version=version+1,updated_at=now() WHERE id=1"
	case "mail-smtp-default":
		query = "UPDATE smtp_settings SET enabled=false,version=version+1,updated_at=now() WHERE id=1"
	case "sms-default":
		query = "UPDATE sms_settings SET enabled=false,version=version+1,updated_at=now() WHERE id=1"
	default:
		return nil
	}
	_, err := tx.Exec(ctx, query)
	return err
}

// Kept deliberately separate from optimistic-lock versioning: changing a
// descriptor does not authorize an implicit migration of persisted values.
func checkChannelSchema(in ChannelInstanceInput, stored int, desc channel.Descriptor) error {
	if (in.ExpectedSchemaVersion != 0 && in.ExpectedSchemaVersion != stored) || stored != desc.SchemaVersion ||
		(in.SchemaVersion != 0 && in.SchemaVersion != desc.SchemaVersion) {
		return conflict("channel_schema_incompatible", "模块配置版本不兼容，请重新加载或执行显式迁移")
	}
	return nil
}
