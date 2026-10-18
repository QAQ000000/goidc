package store

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/secure"
	"github.com/jackc/pgx/v5"
)

type PaymentSettings struct {
	Enabled              bool      `json:"enabled"`
	MockEnabled          bool      `json:"mock_enabled"`
	APIURL               string    `json:"api_url"`
	PID                  string    `json:"pid"`
	Types                []string  `json:"types"`
	KeyConfigured        bool      `json:"key_configured"`
	Configured           bool      `json:"configured"`
	EncryptionConfigured bool      `json:"encryption_configured"`
	Version              int64     `json:"version"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type PaymentConfiguration struct {
	PaymentSettings
	Key          string          `json:"-"`
	MockSecret   string          `json:"-"`
	PublicOrigin string          `json:"-"`
	Runtime      *PaymentRuntime `json:"-"`
}

type PaymentSettingsInput struct {
	Enabled         *bool    `json:"enabled"`
	MockEnabled     *bool    `json:"mock_enabled"`
	APIURL          string   `json:"api_url"`
	PID             string   `json:"pid"`
	Types           []string `json:"types"`
	Key             string   `json:"key"`
	ClearKey        bool     `json:"clear_key"`
	ExpectedVersion int64    `json:"expected_version"`
}

func (c PaymentConfiguration) Gateway(name string, existing bool) (payment.Gateway, bool) {
	owner, registered := payment.MethodOwner(name)
	if !registered || (c.Runtime != nil && owner != c.Runtime.Implementation) {
		return nil, false
	}
	if !existing && (!c.Enabled || (name == "mock" && !c.MockEnabled)) {
		return nil, false
	}
	if c.Runtime != nil && !c.Runtime.Legacy {
		gateway, err := payment.NewFor(c.Runtime.Implementation, name, c.Runtime.Config)
		return gateway, err == nil
	}
	types := c.Types
	if existing && c.Configured {
		types = []string{"alipay", "wxpay", "qqpay", "bank"}
	}
	return payment.For(name, c.MockSecret, payment.EpayConfig{APIURL: c.APIURL, PID: c.PID, Key: c.Key, PublicOrigin: c.PublicOrigin, Types: types})
}

const paymentSettingsColumns = `configured,enabled,mock_enabled,api_url,pid,types,key_ciphertext,version,updated_at`

func (s *Store) paymentConfiguration(row pgx.Row, fallback PaymentConfiguration) (PaymentConfiguration, []byte, error) {
	var out PaymentConfiguration
	var sealed []byte
	err := row.Scan(&out.Configured, &out.Enabled, &out.MockEnabled, &out.APIURL, &out.PID, &out.Types, &sealed, &out.Version, &out.UpdatedAt)
	if err != nil {
		return out, nil, err
	}
	out.MockSecret, out.PublicOrigin = fallback.MockSecret, fallback.PublicOrigin
	if !out.Configured {
		fallback.Configured = false
		fallback.Version = out.Version
		fallback.UpdatedAt = out.UpdatedAt
		out = fallback
	} else if len(sealed) > 0 {
		out.Key, err = s.paymentSecret(sealed, false)
		if err != nil {
			return PaymentConfiguration{}, nil, err
		}
	}
	if out.Types == nil {
		out.Types = []string{}
	}
	out.KeyConfigured = out.Key != ""
	out.EncryptionConfigured = len(s.ProvisionEncryptionKey) == 32
	return out, sealed, nil
}

func (s *Store) PaymentConfiguration(ctx context.Context, fallback PaymentConfiguration) (PaymentConfiguration, error) {
	return s.PaymentConfigurationTx(ctx, nil, fallback)
}

// PaymentConfigurationTx reads settings using an existing transaction when one
// is available. Callers holding a transaction must use this to avoid borrowing
// another pool connection while their transaction is open.
func (s *Store) PaymentConfigurationTx(ctx context.Context, tx pgx.Tx, fallback PaymentConfiguration) (PaymentConfiguration, error) {
	var row pgx.Row
	if tx != nil {
		row = tx.QueryRow(ctx, "SELECT "+paymentSettingsColumns+" FROM payment_settings WHERE id=1")
	} else {
		row = s.Pool.QueryRow(ctx, "SELECT "+paymentSettingsColumns+" FROM payment_settings WHERE id=1")
	}
	out, _, err := s.paymentConfiguration(row, fallback)
	return out, err
}

// PaymentConfigurationForInstance resolves an enabled compiled-in payment
// instance. The legacy singleton remains the fallback for old callers and
// historical transactions, while new intents can bind an exact instance.
func (s *Store) PaymentConfigurationForInstance(ctx context.Context, id string, fallback PaymentConfiguration) (PaymentConfiguration, error) {
	return s.PaymentConfigurationForInstanceTx(ctx, nil, id, fallback)
}

func validatePaymentModuleContract(implementation string, schemaVersion int) error {
	desc, registered := channel.Lookup(channel.Payment, implementation)
	if !registered || desc.InstanceMode != "channel" || !channel.HasFactory(channel.Payment, implementation) {
		return conflict("channel_not_registered", "支付渠道实现未注册")
	}
	if schemaVersion != desc.SchemaVersion {
		return conflict("channel_schema_incompatible", "渠道配置版本不兼容，需要显式迁移")
	}
	return nil
}

func (s *Store) paymentSecret(value []byte, seal bool) (string, error) {
	if len(s.ProvisionEncryptionKey) != 32 {
		return "", conflict("payment_encryption_unconfigured", "支付密钥加密尚未配置")
	}
	aad := []byte("zjmfgo:payment-settings:epay-key:v1")
	if seal {
		out, err := secure.Seal(s.ProvisionEncryptionKey, aad, value)
		return string(out), err
	}
	out, err := secure.Open(s.ProvisionEncryptionKey, aad, value)
	if err != nil {
		return "", errors.New("invalid encrypted payment configuration")
	}
	return string(out), err
}

func lockPaymentConfiguration(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108194)")
	return err
}

func recheckPaymentSettingsManager(ctx context.Context, tx pgx.Tx, actor int64, sessionHash string) error {
	if err := recheckSettingsManager(ctx, tx, actor, sessionHash); err != nil {
		return err
	}
	var role string
	if err := tx.QueryRow(ctx, "SELECT role FROM users WHERE id=$1", actor).Scan(&role); err != nil {
		return err
	}
	if role != "super_admin" {
		return conflict("permission_denied", "支付设置需要超级管理员权限")
	}
	return nil
}

func (s *Store) UpdatePaymentSettings(ctx context.Context, actor int64, sessionHash string, in PaymentSettingsInput, fallback PaymentConfiguration) (PaymentSettings, error) {
	var empty PaymentSettings
	if in.Enabled == nil || in.MockEnabled == nil || in.Types == nil || in.ExpectedVersion < 1 || (in.ClearKey && (in.Key != "" || *in.Enabled)) {
		return empty, conflict("invalid_payment_settings", "请提供完整支付设置和有效版本，删除密钥前请关闭支付")
	}
	in.APIURL = strings.TrimRight(strings.TrimSpace(in.APIURL), "/")
	in.PID = strings.TrimSpace(in.PID)
	for _, v := range []string{in.APIURL, in.PID, in.Key} {
		if !utf8.ValidString(v) || len(v) > 4096 || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return empty, conflict("invalid_payment_settings", "支付配置字符或长度无效")
		}
	}
	seen := map[string]bool{}
	for _, v := range in.Types {
		if !payment.ValidEpayType(v) || seen[v] {
			return empty, conflict("invalid_payment_settings", "支付渠道无效或重复")
		}
		seen[v] = true
	}
	tx, err := settingsManagementTx(ctx, s, actor)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	if err = lockPaymentConfiguration(ctx, tx); err != nil {
		return empty, err
	}
	previous, sealed, err := s.paymentConfiguration(tx.QueryRow(ctx, "SELECT "+paymentSettingsColumns+" FROM payment_settings WHERE id=1 FOR UPDATE"), fallback)
	if err != nil {
		return empty, err
	}
	if err = recheckPaymentSettingsManager(ctx, tx, actor, sessionHash); err != nil {
		return empty, err
	}
	if previous.Version != in.ExpectedVersion {
		return empty, conflict("version_conflict", "支付设置已变更，请重新加载")
	}
	key := previous.Key
	if in.Key != "" {
		key = in.Key
	}
	if in.ClearKey {
		key = ""
	}
	if len(in.Types) > 0 || in.APIURL != "" || in.PID != "" || key != "" {
		validationKey := key
		if validationKey == "" && (!*in.Enabled || len(in.Types) == 0) {
			validationKey = "validation-only"
		}
		g := payment.EpayGateway{EpayConfig: payment.EpayConfig{APIURL: in.APIURL, PID: in.PID, Key: validationKey, PublicOrigin: fallback.PublicOrigin, Types: []string{"alipay"}}, Type: "alipay"}
		if g.Validate() != nil {
			return empty, conflict("invalid_payment_settings", "易支付需要有效 HTTPS 或本地测试地址、商户编号和密钥")
		}
	}
	if in.APIURL != previous.APIURL || in.PID != previous.PID || key != previous.Key {
		var busy bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_transactions WHERE provider LIKE 'epay_%' AND status='pending' AND expires_at>clock_timestamp()) OR EXISTS(SELECT 1 FROM payment_reconciliation_checks c JOIN payment_transactions p ON p.id=c.payment_id WHERE p.provider LIKE 'epay_%' AND c.status='checking' AND c.lease_until>clock_timestamp())`).Scan(&busy); err != nil {
			return empty, err
		}
		if busy {
			return empty, conflict("payment_credentials_in_use", "仍有待支付订单或正在查单，不能更换或删除商户凭据")
		}
		if err = s.preserveLegacyPaymentCredentials(ctx, tx, previous); err != nil {
			return empty, err
		}
	}
	if key == "" {
		sealed = []byte{}
	} else if !previous.Configured || key != previous.Key {
		ciphertext, e := s.paymentSecret([]byte(key), true)
		if e != nil {
			return empty, e
		}
		sealed = []byte(ciphertext)
	}
	out, _, err := s.paymentConfiguration(tx.QueryRow(ctx, `UPDATE payment_settings SET configured=true,enabled=$1,mock_enabled=$2,api_url=$3,pid=$4,types=$5,key_ciphertext=$6,version=version+1,updated_at=now() WHERE id=1 RETURNING `+paymentSettingsColumns, *in.Enabled, *in.MockEnabled, in.APIURL, in.PID, in.Types, sealed), fallback)
	if err != nil {
		return empty, err
	}
	if err = s.syncChannelInstanceFromLegacy(ctx, tx, "payment-default", "payment", "epay", *in.Enabled, map[string]any{
		"api_url": in.APIURL,
		"pid":     in.PID,
		"types":   in.Types,
	}, key); err != nil {
		return empty, err
	}
	if err = s.syncChannelInstanceFromLegacy(ctx, tx, "payment-mock-default", "payment", "mock", *in.MockEnabled, map[string]any{"secret_source": "deployment"}, ""); err != nil {
		return empty, err
	}
	if err = Audit(ctx, tx, actor, "payment.settings_updated", "payment_settings", 1, map[string]any{"before": previous.PaymentSettings, "after": out.PaymentSettings, "key_changed": key != previous.Key, "key_cleared": in.ClearKey}); err != nil {
		return empty, err
	}
	return out.PaymentSettings, tx.Commit(ctx)
}

type paymentConfigVersionKey struct{}

func WithPaymentConfigurationVersion(ctx context.Context, version int64) context.Context {
	return context.WithValue(ctx, paymentConfigVersionKey{}, version)
}
func checkPaymentConfigurationVersion(ctx context.Context, tx pgx.Tx, provider string) error {
	owner, registered := payment.MethodOwner(provider)
	if !registered {
		return conflict("unsupported_payment_provider", "支付渠道实现未注册")
	}
	expected, ok := ctx.Value(paymentConfigVersionKey{}).(int64)
	if instanceID := paymentChannelInstance(ctx); instanceID != "" {
		var version int64
		var category, implementation string
		var enabled bool
		var schemaVersion int
		if err := tx.QueryRow(ctx, `SELECT version,category,implementation_id,enabled,schema_version FROM channel_instances WHERE id=$1 FOR SHARE`, instanceID).Scan(&version, &category, &implementation, &enabled, &schemaVersion); err != nil {
			return err
		}
		if ok && version != expected {
			return conflict("version_conflict", "支付渠道已变更，请重新加载")
		}
		deploymentMock := false
		if cfg, ok := ctx.Value(paymentConfigurationKey{}).(PaymentConfiguration); ok {
			deploymentMock = instanceID == "payment-mock-default" && !cfg.Configured && cfg.MockEnabled
		}
		if category != string(channel.Payment) || (!enabled && !deploymentMock) || implementation != owner {
			return conflict("payment_provider_disabled", "支付渠道已关闭")
		}
		if cfg, ok := ctx.Value(paymentConfigurationKey{}).(PaymentConfiguration); ok && cfg.Runtime != nil {
			runtime := cfg.Runtime
			if runtime.InstanceID != instanceID || runtime.Implementation != implementation || runtime.Version != version || runtime.SchemaVersion != schemaVersion {
				return conflict("version_conflict", "支付渠道已变更，请重新加载")
			}
			if runtime.Legacy {
				var singletonVersion int64
				if err := tx.QueryRow(ctx, "SELECT version FROM payment_settings WHERE id=1").Scan(&singletonVersion); err != nil {
					return err
				}
				if singletonVersion != runtime.SingletonVersion {
					return conflict("version_conflict", "支付设置已变更，请重新加载")
				}
			}
		} else if owner != "mock" && owner != "epay" {
			return conflict("payment_credentials_unavailable", "支付渠道实例快照缺失")
		}
		return validatePaymentModuleContract(implementation, schemaVersion)
	}
	if owner != "mock" && owner != "epay" {
		return conflict("payment_instance_required", "请选择明确的支付渠道实例")
	}
	if err := lockPaymentConfiguration(ctx, tx); err != nil {
		return err
	}
	var version int64
	var configured, enabled, mock bool
	var types []string
	if err := tx.QueryRow(ctx, "SELECT version,configured,enabled,mock_enabled,types FROM payment_settings WHERE id=1").Scan(&version, &configured, &enabled, &mock, &types); err != nil {
		return err
	}
	if ok && version != expected {
		return conflict("version_conflict", "支付设置已变更，请重新加载")
	}
	if configured {
		allowed := enabled && ((provider == "mock" && mock) || (strings.HasPrefix(provider, "epay_") && containsPaymentType(types, strings.TrimPrefix(provider, "epay_"))))
		if !allowed {
			return conflict("payment_provider_disabled", "支付渠道已关闭")
		}
	}
	// Legacy callers may omit an instance ID. Validate the migrated default's
	// contract without applying its empty field schema to deployment fallback
	// settings, and without changing historical snapshot resolution.
	defaultID := "payment-default"
	if provider == "mock" {
		defaultID = "payment-mock-default"
	}
	var implementation string
	var schemaVersion int
	err := tx.QueryRow(ctx, `SELECT implementation_id,schema_version FROM channel_instances WHERE id=$1`, defaultID).Scan(&implementation, &schemaVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		return validatePaymentModuleContract(implementation, schemaVersion)
	}
	return nil
}

func recheckPaymentQueryConfiguration(ctx context.Context, tx pgx.Tx) error {
	if err := lockPaymentConfiguration(ctx, tx); err != nil {
		return err
	}
	if expected, ok := ctx.Value(paymentConfigVersionKey{}).(int64); ok {
		var version int64
		if err := tx.QueryRow(ctx, "SELECT version FROM payment_settings WHERE id=1").Scan(&version); err != nil {
			return err
		}
		if version != expected {
			return conflict("version_conflict", "支付设置已变更，请重新查询")
		}
	}
	return nil
}
func containsPaymentType(types []string, kind string) bool {
	for _, v := range types {
		if v == kind {
			return true
		}
	}
	return false
}

func (s *Store) ProcessConfiguredAutomaticPaymentCheck(ctx context.Context, fallback PaymentConfiguration) (bool, error) {
	if _, err := s.ExpirePaymentIntents(ctx); err != nil {
		return false, err
	}
	check, err := s.ClaimAutomaticPaymentCheck(ctx, payment.QueryMethods())
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	gateway, ok, err := s.PaymentTransactionGateway(ctx, check.Provider, check.ProviderPaymentID, fallback)
	query, supported := gateway.(payment.QueryGateway)
	if err != nil || !ok || !supported {
		if failErr := s.FailPaymentCheck(ctx, check, "provider_unavailable"); failErr != nil {
			return true, failErr
		}
		return true, errors.New("payment credentials unavailable")
	}
	return s.processAutomaticPaymentCheck(ctx, check, query)
}
