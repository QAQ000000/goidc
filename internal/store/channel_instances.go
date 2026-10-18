package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/secure"
	"github.com/jackc/pgx/v5"
)

type ChannelInstance struct {
	ID                  string                    `json:"id"`
	Category            string                    `json:"category"`
	ImplementationID    string                    `json:"implementation_id"`
	DisplayName         string                    `json:"display_name"`
	Enabled             bool                      `json:"enabled"`
	SortOrder           int                       `json:"sort_order"`
	Config              map[string]any            `json:"config"`
	SecretConfigured    bool                      `json:"secret_configured"`
	Version             int64                     `json:"version"`
	CreatedAt           time.Time                 `json:"created_at"`
	UpdatedAt           time.Time                 `json:"updated_at"`
	SchemaVersion       int                       `json:"schema_version"`
	SecureFields        map[string]bool           `json:"secure_fields"`
	FactoryAvailable    bool                      `json:"factory_available"`
	ConfigurationStatus string                    `json:"configuration_status"`
	ValidationErrors    []channel.ValidationError `json:"validation_errors,omitempty"`
}

type ChannelInstanceInput struct {
	ID                    string            `json:"id"`
	Category              string            `json:"category"`
	ImplementationID      string            `json:"implementation_id"`
	DisplayName           string            `json:"display_name"`
	Enabled               bool              `json:"enabled"`
	SortOrder             int               `json:"sort_order"`
	Config                map[string]any    `json:"config"`
	Secret                *string           `json:"secret"`
	ClearSecret           bool              `json:"clear_secret"`
	ExpectedVersion       int64             `json:"expected_version"`
	SchemaVersion         int               `json:"schema_version"`
	ExpectedSchemaVersion int               `json:"expected_schema_version"`
	SecureConfig          map[string]string `json:"secure_config"`
	ClearSecureFields     []string          `json:"clear_secure_fields"`
}

func channelSeal(key []byte, instanceID, raw string, seal bool) ([]byte, error) {
	if len(key) != 32 {
		return nil, conflict("channel_encryption_unconfigured", "渠道凭据加密密钥未配置")
	}
	aadBytes := []byte("zjmfgo:channel-instance:v1:" + instanceID)
	if seal {
		return secure.Seal(key, aadBytes, []byte(raw))
	}
	out, err := secure.Open(key, aadBytes, []byte(raw))
	if err != nil {
		return nil, errors.New("channel secret unavailable")
	}
	return out, err
}

func channelID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "ci-" + hex.EncodeToString([]byte(fmt.Sprint(time.Now().UnixNano())))[:24]
	}
	return "ci-" + hex.EncodeToString(raw)
}

func channelConfigJSON(config map[string]any) ([]byte, error) {
	if config == nil {
		config = map[string]any{}
	}
	return json.Marshal(config)
}

func validateChannelInput(in ChannelInstanceInput, creating bool) (channel.Descriptor, []byte, error) {
	category := channel.Category(strings.TrimSpace(in.Category))
	id := strings.TrimSpace(in.ImplementationID)
	desc, ok := channel.Lookup(category, id)
	if !ok {
		return channel.Descriptor{}, nil, conflict("channel_not_registered", "渠道实现未注册")
	}
	if desc.InstanceMode != "channel" {
		return channel.Descriptor{}, nil, conflict("channel_external_configuration", "请使用该模块的专用配置入口")
	}
	if !channel.HasFactory(category, id) {
		return channel.Descriptor{}, nil, conflict("channel_factory_unavailable", "当前构建未提供模块运行工厂")
	}
	if in.SchemaVersion != 0 && in.SchemaVersion != desc.SchemaVersion {
		return channel.Descriptor{}, nil, conflict("channel_schema_incompatible", "模块配置定义已变更，请重新加载")
	}
	if creating && strings.TrimSpace(in.ID) != "" && (len(in.ID) > 64 || strings.ContainsAny(in.ID, " \t\r\n")) {
		return channel.Descriptor{}, nil, conflict("invalid_channel", "渠道实例 ID 无效")
	}
	if strings.TrimSpace(in.DisplayName) == "" || len(in.DisplayName) > 160 || in.SortOrder < -100000 || in.SortOrder > 100000 {
		return channel.Descriptor{}, nil, conflict("invalid_channel", "渠道实例名称或排序无效")
	}
	normalized, validationErrors := channel.NormalizeConfig(desc, in.Config)
	if len(validationErrors) > 0 {
		return channel.Descriptor{}, nil, conflict("invalid_channel_config", "渠道配置字段、类型或取值无效")
	}
	encoded, err := channelConfigJSON(normalized)
	if err != nil || len(encoded) > 128<<10 {
		return channel.Descriptor{}, nil, conflict("invalid_channel_config", "渠道配置无效")
	}
	if in.ClearSecret && (in.Secret != nil && *in.Secret != "" || in.Enabled) {
		return channel.Descriptor{}, nil, conflict("invalid_channel_secret", "启用渠道前不能清除密钥")
	}
	if in.Secret != nil && len(*in.Secret) > 4096 {
		return channel.Descriptor{}, nil, conflict("invalid_channel_secret", "渠道密钥过长")
	}
	// Secret presence is checked after create/update has resolved the stored
	// value. An update with a nil Secret intentionally keeps the existing
	// ciphertext, so validating that condition here would reject the normal
	// "leave blank to keep" form submission.
	return desc, encoded, nil
}

func (s *Store) ListChannelInstances(ctx context.Context, category string) ([]ChannelInstance, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+channelInstanceColumns+" FROM channel_instances WHERE ($1='' OR category=$1) ORDER BY category,sort_order,id", strings.TrimSpace(category))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ChannelInstance{}
	for rows.Next() {
		rec, scanErr := scanChannelRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, describeChannelRecord(rec))
	}
	return result, rows.Err()
}

func (s *Store) GetChannelInstance(ctx context.Context, id string) (ChannelInstance, error) {
	rec, err := scanChannelRecord(s.Pool.QueryRow(ctx, "SELECT "+channelInstanceColumns+" FROM channel_instances WHERE id=$1", id))
	if err != nil {
		return ChannelInstance{}, err
	}
	return describeChannelRecord(rec), nil
}

// ChannelInstanceSecret preserves the single-credential compatibility boundary.
// Multi-credential modules use ChannelInstanceSecrets instead.
func (s *Store) ChannelInstanceSecret(ctx context.Context, id string) (string, error) {
	rec, err := scanChannelRecord(s.Pool.QueryRow(ctx, "SELECT "+channelInstanceColumns+" FROM channel_instances WHERE id=$1", id))
	if err != nil {
		return "", err
	}
	return s.openChannelSingle(rec)
}

func (s *Store) CreateChannelInstance(ctx context.Context, actor int64, sessionHash string, in ChannelInstanceInput) (ChannelInstance, error) {
	desc, config, err := validateChannelInput(in, true)
	if err != nil {
		return ChannelInstance{}, err
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		id = channelID()
	}
	rec := channelRecord{item: ChannelInstance{ID: id}, fields: map[string][]byte{}}
	secret, aad, fields, err := s.mergeChannelCredentials(rec, desc, in)
	if err != nil {
		return ChannelInstance{}, err
	}
	tx, err := settingsManagementTx(ctx, s, actor)
	if err != nil {
		return ChannelInstance{}, err
	}
	defer tx.Rollback(ctx)
	if err = recheckSettingsManager(ctx, tx, actor, sessionHash); err != nil {
		return ChannelInstance{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,sort_order,config,secret_ciphertext,secret_aad,schema_version,secret_fields) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, desc.Category, desc.ID, strings.TrimSpace(in.DisplayName), in.Enabled, in.SortOrder, config, secret, aad, desc.SchemaVersion, fields); err != nil {
		return ChannelInstance{}, err
	}
	if err = Audit(ctx, tx, actor, "channel.instance_created", "channel_instance", 0, map[string]any{"id": id, "category": desc.Category, "implementation_id": desc.ID, "schema_version": desc.SchemaVersion}); err != nil {
		return ChannelInstance{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ChannelInstance{}, err
	}
	return s.GetChannelInstance(ctx, id)
}

func (s *Store) UpdateChannelInstance(ctx context.Context, actor int64, sessionHash, id string, in ChannelInstanceInput) (ChannelInstance, error) {
	if strings.TrimSpace(id) == "" || in.ExpectedVersion < 1 {
		return ChannelInstance{}, conflict("invalid_channel", "渠道实例或版本无效")
	}
	disableOnly := onlyDisableChannel(in)
	tx, err := settingsManagementTx(ctx, s, actor)
	if err != nil {
		return ChannelInstance{}, err
	}
	defer tx.Rollback(ctx)
	if err = recheckSettingsManager(ctx, tx, actor, sessionHash); err != nil {
		return ChannelInstance{}, err
	}
	rec, err := scanChannelRecord(tx.QueryRow(ctx, "SELECT "+channelInstanceColumns+" FROM channel_instances WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return ChannelInstance{}, err
	}
	old := rec.item
	if old.Version != in.ExpectedVersion {
		return old, conflict("version_conflict", "渠道配置已变更，请重新加载")
	}
	if disableOnly {
		if _, err = tx.Exec(ctx, "UPDATE channel_instances SET enabled=false,version=version+1,updated_at=now() WHERE id=$1", id); err != nil {
			return old, err
		}
		if err = s.disableLegacyChannel(ctx, tx, id); err != nil {
			return old, err
		}
		if err = Audit(ctx, tx, actor, "channel.instance_updated", "channel_instance", 0, map[string]any{"id": id, "version": old.Version + 1, "enabled": false}); err != nil {
			return old, err
		}
		if err = tx.Commit(ctx); err != nil {
			return old, err
		}
		return s.GetChannelInstance(ctx, id)
	}
	if in.Category != "" && in.Category != old.Category || in.ImplementationID != "" && in.ImplementationID != old.ImplementationID {
		return old, conflict("channel_identity_immutable", "请新建实例以更换模块实现")
	}
	in.ID, in.Category, in.ImplementationID = id, old.Category, old.ImplementationID
	if in.DisplayName == "" {
		in.DisplayName = old.DisplayName
	}
	if in.Config == nil {
		in.Config = old.Config
	}
	desc, config, err := validateChannelInput(in, false)
	if err != nil {
		return old, err
	}
	if err = checkChannelSchema(in, old.SchemaVersion, desc); err != nil {
		return old, err
	}
	secret, aad, fields, err := s.mergeChannelCredentials(rec, desc, in)
	if err != nil {
		return old, err
	}
	if _, err = tx.Exec(ctx, `UPDATE channel_instances SET display_name=$2,enabled=$3,sort_order=$4,config=$5,secret_ciphertext=$6,secret_aad=$7,secret_fields=$8,version=version+1,updated_at=now() WHERE id=$1`, id, strings.TrimSpace(in.DisplayName), in.Enabled, in.SortOrder, config, secret, aad, fields); err != nil {
		return old, err
	}
	if err = s.syncLegacyChannelInstance(ctx, tx, id, desc, configFromJSON(config), in.Enabled, secret, aad); err != nil {
		return old, err
	}
	if err = Audit(ctx, tx, actor, "channel.instance_updated", "channel_instance", 0, map[string]any{"id": id, "version": old.Version + 1, "category": desc.Category, "implementation_id": desc.ID, "schema_version": desc.SchemaVersion, "secret_changed": (in.Secret != nil && *in.Secret != "") || in.ClearSecret || len(in.SecureConfig) > 0 || len(in.ClearSecureFields) > 0}); err != nil {
		return old, err
	}
	if err = tx.Commit(ctx); err != nil {
		return old, err
	}
	return s.GetChannelInstance(ctx, id)
}

func (s *Store) DeleteChannelInstance(ctx context.Context, actor int64, sessionHash, id string, expected int64) error {
	if strings.HasSuffix(id, "-default") {
		return conflict("channel_legacy", "兼容渠道实例不能删除")
	}
	tx, err := settingsManagementTx(ctx, s, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = recheckSettingsManager(ctx, tx, actor, sessionHash); err != nil {
		return err
	}
	var version int64
	if version, err = LockVersion(ctx, tx, "channel_instances", id, expected, "渠道配置已变更，请重新加载"); err != nil {
		return err
	}
	var referenced bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_transactions WHERE channel_instance_id=$1) OR EXISTS(SELECT 1 FROM email_outbox WHERE channel_instance_id=$1) OR EXISTS(SELECT 1 FROM sms_outbox WHERE channel_instance_id=$1) OR EXISTS(SELECT 1 FROM notification_routes WHERE instance_id=$1) OR EXISTS(SELECT 1 FROM notification_targets WHERE instance_id=$1) OR EXISTS(SELECT 1 FROM notification_binding_challenges WHERE instance_id=$1) OR EXISTS(SELECT 1 FROM notification_deliveries WHERE instance_id=$1) OR EXISTS(SELECT 1 FROM certification_settings WHERE default_instance_id=$1) OR EXISTS(SELECT 1 FROM certification_submissions WHERE channel_instance_id=$1)`, id).Scan(&referenced); err != nil {
		return err
	}
	if referenced {
		return conflict("channel_in_use", "已有业务记录绑定此渠道实例，不能删除")
	}
	if _, err = tx.Exec(ctx, "DELETE FROM channel_instances WHERE id=$1", id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actor, "channel.instance_deleted", "channel_instance", 0, map[string]any{"id": id, "version": version}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func configString(config map[string]any, key string) string {
	value := config[key]
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func configInt(config map[string]any, key string) int {
	switch value := config[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		var out int
		_, _ = fmt.Sscan(fmt.Sprint(value), &out)
		return out
	}
}

func configStrings(config map[string]any, key string) []string {
	value, ok := config[key].([]any)
	if !ok {
		if values, ok := config[key].([]string); ok {
			return values
		}
		return nil
	}
	result := make([]string, 0, len(value))
	for _, item := range value {
		if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func (s *Store) legacySecretCiphertext(instanceID, oldAAD string, sealed []byte, aad string, seal func(string) ([]byte, error)) ([]byte, error) {
	if len(sealed) == 0 {
		return []byte{}, nil
	}
	if aad == oldAAD {
		return sealed, nil
	}
	plain, err := channelSeal(s.ProvisionEncryptionKey, instanceID, string(sealed), false)
	if err != nil {
		return nil, err
	}
	return seal(string(plain))
}

func (s *Store) syncLegacyChannelInstance(ctx context.Context, tx pgx.Tx, id string, desc channel.Descriptor, config map[string]any, enabled bool, sealed []byte, aad string) error {
	switch id {
	case "payment-default":
		keyCipher, err := s.legacySecretCiphertext(id, "zjmfgo:payment-settings:epay-key:v1", sealed, aad, func(secret string) ([]byte, error) {
			encrypted, e := s.paymentSecret([]byte(secret), true)
			return []byte(encrypted), e
		})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE payment_settings SET enabled=$1,api_url=$2,pid=$3,types=$4,key_ciphertext=$5,configured=true,version=version+1,updated_at=now() WHERE id=1`, enabled, configString(config, "api_url"), configString(config, "pid"), configStrings(config, "types"), keyCipher)
		return err
	case "payment-mock-default":
		_, err := tx.Exec(ctx, "UPDATE payment_settings SET mock_enabled=$1,configured=true,version=version+1,updated_at=now() WHERE id=1", enabled)
		return err
	case "mail-smtp-default":
		passwordCipher, err := s.legacySecretCiphertext(id, "zjmfgo:smtp-settings:password:v1", sealed, aad, func(secret string) ([]byte, error) { return smtpSeal(s.ProvisionEncryptionKey, secret) })
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE smtp_settings SET enabled=$1,host=$2,port=$3,username=$4,password_encrypted=$5,from_address=$6,tls_mode=$7,from_name=$8,configured=true,version=version+1,updated_at=now() WHERE id=1`, enabled, configString(config, "host"), configInt(config, "port"), configString(config, "username"), passwordCipher, configString(config, "from"), configString(config, "tls_mode"), configString(config, "from_name"))
		return err
	case "sms-default":
		tokenCipher, err := s.legacySecretCiphertext(id, "sms:token", sealed, aad, func(secret string) ([]byte, error) {
			return smsCrypt(s.ProvisionEncryptionKey, []byte(secret), "token", true)
		})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE sms_settings SET enabled=$1,provider=$2,endpoint=$3,token_encrypted=$4,configured=true,version=version+1,updated_at=now() WHERE id=1`, enabled, desc.ID, configString(config, "endpoint"), tokenCipher)
		return err
	}
	return nil
}

// syncChannelInstanceFromLegacy keeps the compatibility singleton and the
// compiled-in instance row consistent when an administrator still uses one of
// the older dedicated settings pages. The plaintext secret is immediately
// re-encrypted with the instance ID as AAD.
func (s *Store) syncChannelInstanceFromLegacy(ctx context.Context, tx pgx.Tx, id, category, implementation string, enabled bool, config map[string]any, secret string) error {
	raw, err := channelConfigJSON(config)
	if err != nil {
		return err
	}
	sealed := []byte{}
	aad := ""
	if secret != "" {
		sealed, err = channelSeal(s.ProvisionEncryptionKey, id, secret, true)
		if err != nil {
			return err
		}
		aad = "zjmfgo:channel-instance:v1:" + id
	}
	_, err = tx.Exec(ctx, `UPDATE channel_instances
SET category=$2,implementation_id=$3,enabled=$4,config=$5,secret_ciphertext=$6,secret_aad=$7,version=version+1,updated_at=now()
WHERE id=$1`, id, category, implementation, enabled, raw, sealed, aad)
	return err
}
