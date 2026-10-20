package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/secure"
	"github.com/jackc/pgx/v5"
)

func AuthorizeSMSTest(ctx context.Context, tx pgx.Tx, actor int64, hash string, expected int64, channelInstanceID ...string) error {
	if e := recheckSettingsManager(ctx, tx, actor, hash); e != nil {
		return e
	}
	var super bool
	if e := tx.QueryRow(ctx, `SELECT role='super_admin' AND status='active' FROM administrators WHERE id=$1 FOR UPDATE`, actor).Scan(&super); e != nil {
		return e
	}
	if !super {
		return conflict("permission_denied", "需要超级管理员权限")
	}
	var version int64
	instanceID := ""
	if len(channelInstanceID) > 0 {
		instanceID = strings.TrimSpace(channelInstanceID[0])
	}
	if instanceID == "" {
		if e := tx.QueryRow(ctx, `SELECT version FROM sms_settings WHERE id=1 FOR UPDATE`).Scan(&version); e != nil {
			return e
		}
	} else {
		var category, implementation string
		var enabled bool
		if e := tx.QueryRow(ctx, `SELECT category,implementation_id,enabled,version FROM channel_instances WHERE id=$1 FOR UPDATE`, instanceID).Scan(&category, &implementation, &enabled, &version); e != nil {
			return e
		}
		if category != string(channel.SMS) || !enabled || !channel.HasFactory(channel.SMS, implementation) {
			return conflict("invalid_sms", "短信渠道未启用或实现不可用")
		}
	}
	if version != expected {
		return conflict("version_conflict", "短信配置已变更，请重新加载")
	}
	return nil
}
func SMSProviderRegistered(name string) bool {
	desc, ok := channel.Lookup(channel.SMS, strings.TrimSpace(name))
	return ok && channel.HasFactory(channel.SMS, name) && len(desc.Capabilities) > 0
}

var e164Pattern = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

func ValidSMSRecipient(phone string) bool { return e164Pattern.MatchString(phone) }
func ValidSMSEndpoint(endpoint string) bool {
	u, e := url.Parse(endpoint)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || len(endpoint) > 2048 {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && ip != nil && ip.IsLoopback()
}
func smsCrypt(key, input []byte, domain string, seal bool) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("SMS encryption unavailable")
	}
	aadBytes := []byte("sms:" + domain)
	if seal {
		return secure.Seal(key, aadBytes, input)
	}
	out, err := secure.Open(key, aadBytes, input)
	if errors.Is(err, secure.ErrInvalidCiphertext) {
		return nil, fmt.Errorf("SMS encryption unavailable")
	}
	return out, err
}

type SMSSettings struct {
	Configured           bool      `json:"configured"`
	EncryptionConfigured bool      `json:"encryption_configured"`
	Enabled              bool      `json:"enabled"`
	Provider             string    `json:"provider"`
	Endpoint             string    `json:"endpoint"`
	TokenConfigured      bool      `json:"token_configured"`
	Version              int64     `json:"version"`
	UpdatedAt            time.Time `json:"updated_at"`
}
type SMSRuntimeConfig struct {
	Provider, Endpoint, Token string
	// API, Sign and Key are used by provider-specific adapters. Token remains
	// the generic HTTP provider credential for backwards compatibility.
	API, Sign, Key string
	Enabled        bool
	// ModuleConfig carries the complete schema-defined configuration to a
	// compiled factory. The fields above remain the built-in adapters' legacy
	// compatibility view; future providers do not need new fields here.
	ModuleConfig module.Config `json:"-"`
}

func (s *Store) SMSSettings(ctx context.Context) (SMSSettings, error) {
	var o SMSSettings
	var sealed []byte
	e := s.Pool.QueryRow(ctx, `SELECT configured,enabled,provider,endpoint,token_encrypted,version,updated_at FROM sms_settings WHERE id=1`).Scan(&o.Configured, &o.Enabled, &o.Provider, &o.Endpoint, &sealed, &o.Version, &o.UpdatedAt)
	o.TokenConfigured = len(sealed) > 0
	o.EncryptionConfigured = len(s.ProvisionEncryptionKey) == 32
	return o, e
}
func (s *Store) UpdateSMSSettings(ctx context.Context, actor int64, hash string, in SMSSettings, token *string, clear bool, expected int64) (SMSSettings, error) {
	if len(in.Provider) > 80 || strings.ContainsAny(in.Provider, "\r\n\x00") || token != nil && (len(*token) > 4096 || strings.ContainsAny(*token, "\r\n\x00")) || clear && token != nil && *token != "" || in.Endpoint != "" && !ValidSMSEndpoint(in.Endpoint) || in.Enabled && (!SMSProviderRegistered(in.Provider) || !ValidSMSEndpoint(in.Endpoint) || clear) {
		return SMSSettings{}, conflict("invalid_sms", "短信配置无效")
	}
	tx, e := settingsManagementTx(ctx, s, actor)
	if e != nil {
		return SMSSettings{}, e
	}
	defer tx.Rollback(ctx)
	if e = recheckSettingsManager(ctx, tx, actor, hash); e != nil {
		return SMSSettings{}, e
	}
	var super bool
	if e = tx.QueryRow(ctx, `SELECT role='super_admin' AND status='active' FROM administrators WHERE id=$1`, actor).Scan(&super); e != nil || !super {
		return SMSSettings{}, conflict("permission_denied", "需要超级管理员权限")
	}
	var version int64
	var sealed []byte
	if e = tx.QueryRow(ctx, `SELECT version,token_encrypted FROM sms_settings WHERE id=1 FOR UPDATE`).Scan(&version, &sealed); e != nil {
		return SMSSettings{}, e
	}
	if version != expected {
		return SMSSettings{}, conflict("version_conflict", "短信配置已变更，请重新加载")
	}
	if clear {
		sealed = []byte{}
	} else if token != nil && *token != "" {
		sealed, e = smsCrypt(s.ProvisionEncryptionKey, []byte(*token), "token", true)
		if e != nil {
			return SMSSettings{}, conflict("sms_encryption_unconfigured", "凭据加密密钥未配置")
		}
	}
	if in.Enabled && (len(sealed) == 0 || len(s.ProvisionEncryptionKey) != 32) {
		return SMSSettings{}, conflict("invalid_sms", "启用短信需要令牌和加密密钥")
	}
	_, e = tx.Exec(ctx, `UPDATE sms_settings SET configured=true,enabled=$1,provider=$2,endpoint=$3,token_encrypted=$4,version=version+1,updated_at=now() WHERE id=1`, in.Enabled, in.Provider, in.Endpoint, sealed)
	if e != nil {
		return SMSSettings{}, e
	}
	tokenValue := ""
	if len(sealed) > 0 {
		plain, openErr := smsCrypt(s.ProvisionEncryptionKey, sealed, "token", false)
		if openErr != nil {
			return SMSSettings{}, conflict("sms_encryption_unconfigured", "凭据加密密钥未配置")
		}
		tokenValue = string(plain)
	}
	if e = s.syncChannelInstanceFromLegacy(ctx, tx, "sms-default", "sms", in.Provider, in.Enabled, map[string]any{"endpoint": in.Endpoint}, tokenValue); e != nil {
		return SMSSettings{}, e
	}
	if e = Audit(ctx, tx, actor, "sms.settings_updated", "sms_settings", 1, map[string]any{"version": version + 1}); e != nil {
		return SMSSettings{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return SMSSettings{}, e
	}
	return s.SMSSettings(ctx)
}
func (s *Store) ResolveSMSConfig(ctx context.Context) (SMSRuntimeConfig, error) {
	var instanceID string
	lookupErr := s.Pool.QueryRow(ctx, `SELECT id FROM channel_instances WHERE category='sms' AND enabled=true ORDER BY sort_order,id LIMIT 1`).Scan(&instanceID)
	if lookupErr == nil {
		return s.ResolveSMSConfigForInstance(ctx, instanceID)
	}
	if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return SMSRuntimeConfig{}, lookupErr
	}
	var o SMSRuntimeConfig
	var sealed []byte
	e := s.Pool.QueryRow(ctx, `SELECT enabled,provider,endpoint,token_encrypted FROM sms_settings WHERE id=1`).Scan(&o.Enabled, &o.Provider, &o.Endpoint, &sealed)
	if e != nil {
		return o, e
	}
	if !o.Enabled {
		return o, nil
	}
	var implementation string
	var schemaVersion int
	contractErr := s.Pool.QueryRow(ctx, `SELECT implementation_id,schema_version FROM channel_instances WHERE id='sms-default'`).Scan(&implementation, &schemaVersion)
	if contractErr != nil && !errors.Is(contractErr, pgx.ErrNoRows) {
		return SMSRuntimeConfig{}, contractErr
	}
	if contractErr == nil {
		desc, registered := channel.Lookup(channel.SMS, implementation)
		if !registered || !channel.HasFactory(channel.SMS, implementation) || desc.InstanceMode != "channel" {
			return SMSRuntimeConfig{}, conflict("channel_not_registered", "短信渠道实现未注册")
		}
		if schemaVersion != desc.SchemaVersion {
			return SMSRuntimeConfig{}, conflict("channel_schema_incompatible", "渠道配置版本不兼容，需要显式迁移")
		}
	}
	raw, e := smsCrypt(s.ProvisionEncryptionKey, sealed, "token", false)
	if e != nil {
		return SMSRuntimeConfig{}, fmt.Errorf("SMS credentials unavailable")
	}
	o.Token = string(raw)
	return o, nil
}

// ResolveSMSConfigForInstance resolves the implementation selected for an
// outbox row. Empty IDs retain compatibility with legacy singleton rows.
func (s *Store) ResolveSMSConfigForInstance(ctx context.Context, instanceID string) (SMSRuntimeConfig, error) {
	if strings.TrimSpace(instanceID) == "" {
		return s.ResolveSMSConfig(ctx)
	}
	config, implementation, err := s.RuntimeChannelInstance(ctx, instanceID, channel.SMS)
	if err != nil {
		return SMSRuntimeConfig{}, fmt.Errorf("SMS channel unavailable")
	}
	out := SMSRuntimeConfig{
		Provider:     implementation,
		Endpoint:     configString(config.Values, "endpoint"),
		API:          configString(config.Values, "api"),
		Sign:         configString(config.Values, "sign"),
		Token:        config.Secrets["token"],
		Key:          config.Secrets["key"],
		Enabled:      true,
		ModuleConfig: config,
	}
	if out.Token == "" {
		out.Token = config.Secret
	}
	if out.Key == "" {
		out.Key = config.Secret
	}
	return out, nil
}

type SMSJob struct {
	ID                    int64
	CorrelationID         string
	Recipient, Body, Kind string
	TemplateID            string
	VerificationID        string
	Attempts              int
	ExpiresAt             time.Time
	ChannelInstanceID     string
	LockedAt              time.Time
}

func (s *Store) QueueSMSVerification(ctx context.Context, tx pgx.Tx, recipient, code, purpose, challengeID string, expiresAt time.Time) error {
	kind := "verification_code"
	if purpose == "password_reset" {
		kind = "password_reset"
	}
	var body, templateID string
	err := tx.QueryRow(ctx, `SELECT body,provider_template_id FROM notification_templates WHERE channel='sms' AND kind=$1 AND active=true`, kind).Scan(&body, &templateID)
	if err == nil {
		minutes := max(1, int((time.Until(expiresAt)+time.Minute-1)/time.Minute))
		body = strings.ReplaceAll(strings.ReplaceAll(body, "{{code}}", code), "{{expires_minutes}}", fmt.Sprint(minutes))
	} else {
		body = "验证码：" + code
		templateID = ""
	}
	return s.queueSMSWithTemplate(ctx, tx, recipient, body, purpose, challengeID, expiresAt, templateID)
}
func (s *Store) QueueSMS(ctx context.Context, tx pgx.Tx, recipient, body, kind, challengeID string, expiresAt time.Time, channelInstanceID ...string) error {
	_, err := s.queueSMSWithTemplateID(ctx, tx, recipient, body, kind, challengeID, expiresAt, "", channelInstanceID...)
	return err
}

// QueueSMSWithID is used by bulk notification workflows that need to retain
// the outbox row associated with each recipient for progress reporting.
func (s *Store) QueueSMSWithID(ctx context.Context, tx pgx.Tx, recipient, body, kind, challengeID string, expiresAt time.Time, channelInstanceID ...string) (int64, error) {
	return s.queueSMSWithTemplateID(ctx, tx, recipient, body, kind, challengeID, expiresAt, "", channelInstanceID...)
}
func (s *Store) queueSMSWithTemplate(ctx context.Context, tx pgx.Tx, recipient, body, kind, challengeID string, expiresAt time.Time, templateID string, channelInstanceID ...string) error {
	_, err := s.queueSMSWithTemplateID(ctx, tx, recipient, body, kind, challengeID, expiresAt, templateID, channelInstanceID...)
	return err
}
func (s *Store) queueSMSWithTemplateID(ctx context.Context, tx pgx.Tx, recipient, body, kind, challengeID string, expiresAt time.Time, templateID string, channelInstanceID ...string) (int64, error) {
	if !ValidSMSRecipient(recipient) || len(body) > 4096 || !expiresAt.After(time.Now()) {
		return 0, conflict("invalid_sms", "短信内容无效")
	}
	instanceID := ""
	if len(channelInstanceID) > 0 {
		instanceID = strings.TrimSpace(channelInstanceID[0])
		if instanceID != "" {
			var valid bool
			if e := tx.QueryRow(ctx, `SELECT category='sms' AND enabled FROM channel_instances WHERE id=$1`, instanceID).Scan(&valid); e != nil {
				return 0, e
			}
			if !valid {
				return 0, conflict("sms_unavailable", "短信渠道未启用")
			}
		}
	} else {
		var enabled bool
		if e := tx.QueryRow(ctx, `SELECT enabled FROM sms_settings WHERE id=1`).Scan(&enabled); e != nil {
			return 0, e
		}
		if !enabled {
			if e := tx.QueryRow(ctx, `SELECT COALESCE((SELECT id FROM channel_instances WHERE category='sms' AND enabled=true ORDER BY sort_order,id LIMIT 1),'')`).Scan(&instanceID); e != nil {
				return 0, e
			}
			if instanceID == "" {
				return 0, conflict("sms_unavailable", "短信渠道未启用")
			}
		}
	}
	sealed, e := smsCrypt(s.ProvisionEncryptionKey, []byte(body), "body:"+recipient+":"+challengeID, true)
	if e != nil {
		return 0, e
	}
	var id int64
	e = tx.QueryRow(ctx, `INSERT INTO sms_outbox(recipient,body_encrypted,kind,challenge_id,expires_at,verification_id,channel_instance_id,template_id) VALUES($1,$2,$3,$4,$5,NULLIF($4,''),COALESCE(NULLIF($6,''),(SELECT id FROM channel_instances WHERE category='sms' AND enabled=true ORDER BY sort_order,id LIMIT 1)),$7) RETURNING id`, recipient, sealed, kind, challengeID, expiresAt, instanceID, templateID).Scan(&id)
	return id, e
}
func (s *Store) RecoverStaleSMSJobs(ctx context.Context) error {
	if _, err := s.Pool.Exec(ctx, `DELETE FROM sms_delivery_evidence WHERE expires_at<=now()`); err != nil {
		return err
	}
	return s.recoverOutboxJobs(ctx, "sms_outbox")
}
func (s *Store) ClaimSMSJob(ctx context.Context) (*SMSJob, error) {
	if err := s.expireLegacyDeadlineJobs(ctx, "sms_outbox"); err != nil {
		return nil, err
	}
	var o SMSJob
	var sealed []byte
	var challenge string
	e := s.Pool.QueryRow(ctx, `WITH c AS(SELECT id FROM sms_outbox WHERE status='pending' AND attempts<max_attempts AND available_at<=now() AND expires_at>now() AND (verification_id IS NULL OR EXISTS(SELECT 1 FROM verification_challenges v WHERE v.id=sms_outbox.verification_id AND v.consumed_at IS NULL AND v.expires_at>now())) AND `+legacyDeadlineEligibleSQL("sms_outbox", "sms")+` ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE sms_outbox s SET status='sending',attempts=attempts+1,locked_at=now() FROM c WHERE s.id=c.id RETURNING s.id,s.recipient,s.body_encrypted,s.kind,s.attempts,s.expires_at,s.challenge_id,coalesce(s.channel_instance_id,''),coalesce(s.template_id,''),s.locked_at,coalesce(s.correlation_id,'')`).Scan(&o.ID, &o.Recipient, &sealed, &o.Kind, &o.Attempts, &o.ExpiresAt, &challenge, &o.ChannelInstanceID, &o.TemplateID, &o.LockedAt, &o.CorrelationID)
	if e != nil {
		return nil, e
	}
	o.VerificationID = challenge
	raw, e := smsCrypt(s.ProvisionEncryptionKey, sealed, "body:"+o.Recipient+":"+challenge, false)
	if e != nil {
		_ = s.CompleteSMSJob(ctx, o, fmt.Errorf("SMS payload unavailable"))
		return nil, fmt.Errorf("SMS payload unavailable")
	}
	o.Body = string(raw)
	return &o, nil
}

// SMSJobActive rechecks challenge consumption immediately before delivery.
func (s *Store) SMSJobActive(ctx context.Context, job SMSJob) (bool, error) {
	var active bool
	e := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sms_outbox s WHERE s.id=$1 AND s.status='sending' AND s.attempts=$2 AND s.locked_at=$3 AND s.expires_at>now() AND (s.verification_id IS NULL OR EXISTS(SELECT 1 FROM verification_challenges v WHERE v.id=s.verification_id AND v.consumed_at IS NULL AND v.expires_at>now())) AND `+legacyDeadlineEligibleSQL("s", "sms")+`)`, job.ID, job.Attempts, job.LockedAt).Scan(&active)
	return active, e
}
func (s *Store) CompleteSMSJob(ctx context.Context, o SMSJob, sendErr error) error {
	if sendErr == nil {
		_, e := s.Pool.Exec(ctx, `UPDATE sms_outbox SET status='sent',body_encrypted=''::bytea,locked_at=NULL,sent_at=now(),last_error='' WHERE id=$1 AND status='sending' AND attempts=$2 AND locked_at=$3`, o.ID, o.Attempts, o.LockedAt)
		return e
	}
	return s.failOutboxJob(ctx, "sms_outbox", o.ID, o.Attempts, o.LockedAt, "SMS delivery failed")
}
