package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/jackc/pgx/v5"
)

type ChannelInstance struct {
	ID               string         `json:"id"`
	Category         string         `json:"category"`
	ImplementationID string         `json:"implementation_id"`
	DisplayName      string         `json:"display_name"`
	Enabled          bool           `json:"enabled"`
	SortOrder        int            `json:"sort_order"`
	Config           map[string]any `json:"config"`
	SecretConfigured bool           `json:"secret_configured"`
	Version          int64          `json:"version"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

type ChannelInstanceInput struct {
	ID               string         `json:"id"`
	Category         string         `json:"category"`
	ImplementationID string         `json:"implementation_id"`
	DisplayName      string         `json:"display_name"`
	Enabled          bool           `json:"enabled"`
	SortOrder        int            `json:"sort_order"`
	Config           map[string]any `json:"config"`
	Secret           *string        `json:"secret"`
	ClearSecret      bool           `json:"clear_secret"`
	ExpectedVersion  int64          `json:"expected_version"`
}

func channelSeal(key []byte, instanceID, raw string, seal bool) ([]byte, error) {
	if len(key) != 32 {
		return nil, conflict("channel_encryption_unconfigured", "渠道凭据加密密钥未配置")
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	aad := []byte("zjmfgo:channel-instance:v1:" + instanceID)
	if seal {
		nonce := make([]byte, g.NonceSize())
		if _, err = rand.Read(nonce); err != nil {
			return nil, err
		}
		return g.Seal(nonce, nonce, []byte(raw), aad), nil
	}
	if len(raw) < g.NonceSize() {
		return nil, errors.New("channel secret unavailable")
	}
	plain, err := g.Open(nil, []byte(raw[:g.NonceSize()]), []byte(raw[g.NonceSize():]), aad)
	if err != nil {
		return nil, errors.New("channel secret unavailable")
	}
	return plain, nil
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
	if creating && strings.TrimSpace(in.ID) != "" && (len(in.ID) > 64 || strings.ContainsAny(in.ID, " \t\r\n")) {
		return channel.Descriptor{}, nil, conflict("invalid_channel", "渠道实例 ID 无效")
	}
	if strings.TrimSpace(in.DisplayName) == "" || len(in.DisplayName) > 160 || in.SortOrder < -100000 || in.SortOrder > 100000 {
		return channel.Descriptor{}, nil, conflict("invalid_channel", "渠道实例名称或排序无效")
	}
	for _, field := range desc.Fields {
		if !field.Required || field.Secret {
			continue
		}
		value, exists := in.Config[field.Key]
		if !exists || strings.TrimSpace(fmt.Sprint(value)) == "" {
			return channel.Descriptor{}, nil, conflict("invalid_channel_config", "缺少渠道必填配置")
		}
	}
	encoded, err := channelConfigJSON(in.Config)
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

func hasSecretField(desc channel.Descriptor) bool {
	for _, field := range desc.Fields {
		if field.Secret {
			return true
		}
	}
	return false
}

func (s *Store) ListChannelInstances(ctx context.Context, category string) ([]ChannelInstance, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,category,implementation_id,display_name,enabled,sort_order,config,secret_ciphertext,version,created_at,updated_at FROM channel_instances WHERE ($1='' OR category=$1) ORDER BY category,sort_order,id`, strings.TrimSpace(category))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ChannelInstance{}
	for rows.Next() {
		var item ChannelInstance
		var config, secret []byte
		if err = rows.Scan(&item.ID, &item.Category, &item.ImplementationID, &item.DisplayName, &item.Enabled, &item.SortOrder, &config, &secret, &item.Version, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(config, &item.Config); err != nil {
			return nil, err
		}
		item.SecretConfigured = len(secret) > 0
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) GetChannelInstance(ctx context.Context, id string) (ChannelInstance, error) {
	var out ChannelInstance
	var config, secret []byte
	err := s.Pool.QueryRow(ctx, `SELECT id,category,implementation_id,display_name,enabled,sort_order,config,secret_ciphertext,version,created_at,updated_at FROM channel_instances WHERE id=$1`, id).Scan(&out.ID, &out.Category, &out.ImplementationID, &out.DisplayName, &out.Enabled, &out.SortOrder, &config, &secret, &out.Version, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(config, &out.Config); err != nil {
		return out, err
	}
	out.SecretConfigured = len(secret) > 0
	return out, nil
}

// ChannelInstanceSecret is intentionally only used by compiled-in adapters.
// API responses never include the decrypted value.
func (s *Store) ChannelInstanceSecret(ctx context.Context, id string) (string, error) {
	var sealed []byte
	var aad string
	var category, implementation string
	if err := s.Pool.QueryRow(ctx, "SELECT category,implementation_id,secret_ciphertext,secret_aad FROM channel_instances WHERE id=$1", id).Scan(&category, &implementation, &sealed, &aad); err != nil {
		return "", err
	}
	if len(sealed) == 0 {
		return "", nil
	}
	if aad != "" && aad != "zjmfgo:channel-instance:v1:"+id {
		var plain string
		var err error
		switch {
		case category == string(channel.Payment) && implementation == "epay" && aad == "zjmfgo:payment-settings:epay-key:v1":
			plain, err = s.paymentSecret(sealed, false)
		case category == string(channel.Mail) && implementation == "smtp" && aad == "zjmfgo:smtp-settings:password:v1":
			plain, err = smtpOpen(s.ProvisionEncryptionKey, sealed)
		case category == string(channel.SMS) && aad == "sms:token":
			var raw []byte
			raw, err = smsCrypt(s.ProvisionEncryptionKey, sealed, "token", false)
			plain = string(raw)
		default:
			return "", errors.New("channel secret unavailable")
		}
		if err != nil {
			return "", errors.New("channel secret unavailable")
		}
		return plain, nil
	}
	plain, err := channelSeal(s.ProvisionEncryptionKey, id, string(sealed), false)
	if err != nil {
		return "", err
	}
	return string(plain), nil
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
	secret := []byte{}
	if in.Secret != nil && *in.Secret != "" {
		sealed, sealErr := channelSeal(s.ProvisionEncryptionKey, id, *in.Secret, true)
		if sealErr != nil {
			return ChannelInstance{}, sealErr
		}
		secret = sealed
	}
	if in.Enabled && len(secret) == 0 && hasSecretField(desc) {
		return ChannelInstance{}, conflict("invalid_channel_secret", "启用渠道需要密钥")
	}
	tx, err := settingsManagementTx(ctx, s, actor)
	if err != nil {
		return ChannelInstance{}, err
	}
	defer tx.Rollback(ctx)
	if err = recheckSettingsManager(ctx, tx, actor, sessionHash); err != nil {
		return ChannelInstance{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,sort_order,config,secret_ciphertext,secret_aad) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, desc.Category, desc.ID, strings.TrimSpace(in.DisplayName), in.Enabled, in.SortOrder, config, secret, "zjmfgo:channel-instance:v1:"+id); err != nil {
		return ChannelInstance{}, err
	}
	if err = Audit(ctx, tx, actor, "channel.instance_created", "channel_instance", 0, map[string]any{"id": id, "category": desc.Category, "implementation_id": desc.ID}); err != nil {
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
	in.ID = id
	var old ChannelInstance
	var oldSecret []byte
	var oldAAD string
	tx, err := settingsManagementTx(ctx, s, actor)
	if err != nil {
		return old, err
	}
	defer tx.Rollback(ctx)
	if err = recheckSettingsManager(ctx, tx, actor, sessionHash); err != nil {
		return old, err
	}
	if err = tx.QueryRow(ctx, `SELECT id,category,implementation_id,display_name,enabled,sort_order,config,secret_ciphertext,secret_aad,version,created_at,updated_at FROM channel_instances WHERE id=$1 FOR UPDATE`, id).Scan(&old.ID, &old.Category, &old.ImplementationID, &old.DisplayName, &old.Enabled, &old.SortOrder, &old.Config, &oldSecret, &oldAAD, &old.Version, &old.CreatedAt, &old.UpdatedAt); err != nil {
		return old, err
	}
	if old.Version != in.ExpectedVersion {
		return old, conflict("version_conflict", "渠道配置已变更，请重新加载")
	}
	if in.Category == "" {
		in.Category, in.ImplementationID = old.Category, old.ImplementationID
	}
	if in.ImplementationID == "" {
		in.ImplementationID = old.ImplementationID
	}
	if in.DisplayName == "" {
		in.DisplayName = old.DisplayName
	}
	if in.Config == nil {
		in.Config = old.Config
	}
	desc, config, validationErr := validateChannelInput(in, false)
	if validationErr != nil {
		return old, validationErr
	}
	secret := oldSecret
	aad := oldAAD
	if in.ClearSecret {
		secret, aad = []byte{}, ""
	} else if in.Secret != nil && *in.Secret != "" {
		sealed, sealErr := channelSeal(s.ProvisionEncryptionKey, id, *in.Secret, true)
		if sealErr != nil {
			return old, sealErr
		}
		secret, aad = sealed, "zjmfgo:channel-instance:v1:"+id
	}
	if in.Enabled && len(secret) == 0 && hasSecretField(desc) {
		return old, conflict("invalid_channel_secret", "启用渠道需要密钥")
	}
	if _, err = tx.Exec(ctx, `UPDATE channel_instances SET category=$2,implementation_id=$3,display_name=$4,enabled=$5,sort_order=$6,config=$7,secret_ciphertext=$8,secret_aad=$9,version=version+1,updated_at=now() WHERE id=$1`, id, desc.Category, desc.ID, strings.TrimSpace(in.DisplayName), in.Enabled, in.SortOrder, config, secret, aad); err != nil {
		return old, err
	}
	if err = s.syncLegacyChannelInstance(ctx, tx, id, desc, in.Config, in.Enabled, secret, aad); err != nil {
		return old, err
	}
	if err = Audit(ctx, tx, actor, "channel.instance_updated", "channel_instance", 0, map[string]any{"id": id, "version": old.Version + 1, "category": desc.Category, "implementation_id": desc.ID, "secret_changed": (in.Secret != nil && *in.Secret != "") || in.ClearSecret}); err != nil {
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
	if err = tx.QueryRow(ctx, "SELECT version FROM channel_instances WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
		return err
	}
	if version != expected {
		return conflict("version_conflict", "渠道配置已变更，请重新加载")
	}
	var referenced bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_transactions WHERE channel_instance_id=$1) OR EXISTS(SELECT 1 FROM email_outbox WHERE channel_instance_id=$1) OR EXISTS(SELECT 1 FROM sms_outbox WHERE channel_instance_id=$1)`, id).Scan(&referenced); err != nil {
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
	return strings.TrimSpace(fmt.Sprint(config[key]))
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
