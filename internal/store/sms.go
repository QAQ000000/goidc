package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

var smsProviders sync.Map

func AuthorizeSMSTest(ctx context.Context, tx pgx.Tx, actor int64, hash string, expected int64) error {
	if e := recheckSettingsManager(ctx, tx, actor, hash); e != nil {
		return e
	}
	var super bool
	if e := tx.QueryRow(ctx, `SELECT role='super_admin' AND status='active' FROM users WHERE id=$1 FOR UPDATE`, actor).Scan(&super); e != nil {
		return e
	}
	if !super {
		return conflict("permission_denied", "需要超级管理员权限")
	}
	var version int64
	if e := tx.QueryRow(ctx, `SELECT version FROM sms_settings WHERE id=1 FOR UPDATE`).Scan(&version); e != nil {
		return e
	}
	if version != expected {
		return conflict("version_conflict", "短信配置已变更，请重新加载")
	}
	return nil
}
func RegisterSMSProviderName(name string)    { smsProviders.Store(name, true) }
func SMSProviderRegistered(name string) bool { _, ok := smsProviders.Load(name); return ok }

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
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	if seal {
		n := make([]byte, g.NonceSize())
		if _, e = rand.Read(n); e != nil {
			return nil, e
		}
		return g.Seal(n, n, input, []byte("sms:"+domain)), nil
	}
	if len(input) < g.NonceSize() {
		return nil, fmt.Errorf("SMS encryption unavailable")
	}
	return g.Open(nil, input[:g.NonceSize()], input[g.NonceSize():], []byte("sms:"+domain))
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
	Enabled                   bool
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
	if e = tx.QueryRow(ctx, `SELECT role='super_admin' AND status='active' FROM users WHERE id=$1`, actor).Scan(&super); e != nil || !super {
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
	if e = Audit(ctx, tx, actor, "sms.settings_updated", "sms_settings", 1, map[string]any{"version": version + 1}); e != nil {
		return SMSSettings{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return SMSSettings{}, e
	}
	return s.SMSSettings(ctx)
}
func (s *Store) ResolveSMSConfig(ctx context.Context) (SMSRuntimeConfig, error) {
	var o SMSRuntimeConfig
	var sealed []byte
	e := s.Pool.QueryRow(ctx, `SELECT enabled,provider,endpoint,token_encrypted FROM sms_settings WHERE id=1`).Scan(&o.Enabled, &o.Provider, &o.Endpoint, &sealed)
	if e != nil || !o.Enabled {
		return o, e
	}
	raw, e := smsCrypt(s.ProvisionEncryptionKey, sealed, "token", false)
	if e != nil {
		return SMSRuntimeConfig{}, fmt.Errorf("SMS credentials unavailable")
	}
	o.Token = string(raw)
	return o, nil
}

type SMSJob struct {
	ID                    int64
	Recipient, Body, Kind string
	VerificationID        string
	Attempts              int
	ExpiresAt             time.Time
}

func (s *Store) QueueSMSVerification(ctx context.Context, tx pgx.Tx, recipient, code, purpose, challengeID string, expiresAt time.Time) error {
	return s.QueueSMS(ctx, tx, recipient, "验证码："+code, purpose, challengeID, expiresAt)
}
func (s *Store) QueueSMS(ctx context.Context, tx pgx.Tx, recipient, body, kind, challengeID string, expiresAt time.Time) error {
	if !ValidSMSRecipient(recipient) || len(body) > 4096 || !expiresAt.After(time.Now()) {
		return conflict("invalid_sms", "短信内容无效")
	}
	var enabled bool
	if e := tx.QueryRow(ctx, `SELECT enabled FROM sms_settings WHERE id=1`).Scan(&enabled); e != nil {
		return e
	}
	if !enabled {
		return conflict("sms_unavailable", "短信渠道未启用")
	}
	sealed, e := smsCrypt(s.ProvisionEncryptionKey, []byte(body), "body:"+recipient+":"+challengeID, true)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO sms_outbox(recipient,body_encrypted,kind,challenge_id,expires_at,verification_id) VALUES($1,$2,$3,$4,$5,NULLIF($4,''))`, recipient, sealed, kind, challengeID, expiresAt)
	return e
}
func (s *Store) RecoverStaleSMSJobs(ctx context.Context) error {
	_, e := s.Pool.Exec(ctx, `UPDATE sms_outbox SET status='expired',body_encrypted=''::bytea,locked_at=NULL WHERE status IN ('pending','sending') AND (expires_at<=now() OR EXISTS(SELECT 1 FROM verification_challenges v WHERE v.id=sms_outbox.verification_id AND (v.consumed_at IS NOT NULL OR v.expires_at<=now()))); UPDATE sms_outbox SET status='pending',locked_at=NULL,last_error='worker lease expired' WHERE status='sending' AND locked_at<now()-interval '5 minutes'`)
	return e
}
func (s *Store) ClaimSMSJob(ctx context.Context) (*SMSJob, error) {
	var o SMSJob
	var sealed []byte
	var challenge string
	e := s.Pool.QueryRow(ctx, `WITH c AS(SELECT id FROM sms_outbox WHERE status='pending' AND available_at<=now() AND expires_at>now() AND (verification_id IS NULL OR EXISTS(SELECT 1 FROM verification_challenges v WHERE v.id=sms_outbox.verification_id AND v.consumed_at IS NULL AND v.expires_at>now())) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE sms_outbox s SET status='sending',attempts=attempts+1,locked_at=now() FROM c WHERE s.id=c.id RETURNING s.id,s.recipient,s.body_encrypted,s.kind,s.attempts,s.expires_at,s.challenge_id`).Scan(&o.ID, &o.Recipient, &sealed, &o.Kind, &o.Attempts, &o.ExpiresAt, &challenge)
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
	e := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sms_outbox s WHERE s.id=$1 AND s.status='sending' AND s.attempts=$2 AND s.expires_at>now() AND (s.verification_id IS NULL OR EXISTS(SELECT 1 FROM verification_challenges v WHERE v.id=s.verification_id AND v.consumed_at IS NULL AND v.expires_at>now())))`, job.ID, job.Attempts).Scan(&active)
	return active, e
}
func (s *Store) CompleteSMSJob(ctx context.Context, o SMSJob, sendErr error) error {
	if sendErr == nil {
		_, e := s.Pool.Exec(ctx, `UPDATE sms_outbox SET status='sent',body_encrypted=''::bytea,locked_at=NULL,sent_at=now(),last_error='' WHERE id=$1 AND status='sending' AND attempts=$2`, o.ID, o.Attempts)
		return e
	}
	_, e := s.Pool.Exec(ctx, `UPDATE sms_outbox SET status=CASE WHEN expires_at<=now() THEN 'expired' WHEN attempts>=5 THEN 'failed' ELSE 'pending' END, body_encrypted=CASE WHEN expires_at<=now() OR attempts>=5 THEN ''::bytea ELSE body_encrypted END,locked_at=NULL,last_error='SMS delivery failed',available_at=now()+interval '1 minute' WHERE id=$1 AND status='sending' AND attempts=$2`, o.ID, o.Attempts)
	return e
}
