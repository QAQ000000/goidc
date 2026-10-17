package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/secure"
	"github.com/jackc/pgx/v5"
)

type SMTPSettings struct {
	Configured           bool      `json:"configured"`
	EncryptionConfigured bool      `json:"encryption_configured"`
	Enabled              bool      `json:"enabled"`
	Host                 string    `json:"host"`
	Port                 int       `json:"port"`
	Username             string    `json:"username"`
	From                 string    `json:"from"`
	TLSMode              string    `json:"tls_mode"`
	FromName             string    `json:"from_name"`
	PasswordConfigured   bool      `json:"password_configured"`
	Version              int64     `json:"version"`
	UpdatedAt            time.Time `json:"updated_at"`
}
type SMTPRuntimeConfig struct {
	Host                     string
	Port                     int
	Username, Password, From string
	TLSMode, FromName        string
}

func smtpSeal(key []byte, value string) ([]byte, error) {
	if len(key) != 32 {
		return nil, conflict("smtp_encryption_unconfigured", "凭据加密密钥未配置")
	}
	raw, _ := json.Marshal(value)
	return secure.Seal(key, []byte("smtp:password"), raw)
}
func smtpOpen(key, sealed []byte) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	if len(key) != 32 {
		return "", fmt.Errorf("SMTP credentials unavailable")
	}
	raw, err := secure.Open(key, []byte("smtp:password"), sealed)
	if errors.Is(err, secure.ErrInvalidCiphertext) {
		return "", fmt.Errorf("invalid smtp secret")
	}
	if err != nil {
		return "", err
	}
	var out string
	err = json.Unmarshal(raw, &out)
	return out, err
}
func (s *Store) SMTPSettings(ctx context.Context, fallback SMTPRuntimeConfig) (SMTPSettings, error) {
	var o SMTPSettings
	var sealed []byte
	err := s.Pool.QueryRow(ctx, "SELECT configured,enabled,host,port,username,password_encrypted,from_address,tls_mode,from_name,version,updated_at FROM smtp_settings WHERE id=1").Scan(&o.Configured, &o.Enabled, &o.Host, &o.Port, &o.Username, &sealed, &o.From, &o.TLSMode, &o.FromName, &o.Version, &o.UpdatedAt)
	o.PasswordConfigured = len(sealed) > 0
	o.EncryptionConfigured = len(s.ProvisionEncryptionKey) == 32
	if !o.Configured {
		o.Host = fallback.Host
		o.Port = fallback.Port
		o.Username = fallback.Username
		o.From = fallback.From
		o.TLSMode, o.FromName = fallback.TLSMode, fallback.FromName
		o.Enabled = fallback.Host != "" && fallback.From != ""
		o.PasswordConfigured = fallback.Password != ""
	}
	if o.TLSMode == "" {
		if o.Port == 465 {
			o.TLSMode = "implicit_tls"
		} else {
			o.TLSMode = "starttls"
		}
	}
	return o, err
}
func (s *Store) UpdateSMTPSettings(ctx context.Context, actor int64, sessionHash string, in SMTPSettings, password *string, clear bool, expected int64, fallback SMTPRuntimeConfig) (SMTPSettings, error) {
	if len(in.FromName) > 200 || strings.ContainsAny(in.FromName, "\r\n\x00") {
		return SMTPSettings{}, conflict("invalid_smtp", "发件名称无效")
	}
	if strings.ContainsAny(in.Host+in.Username+in.From, "\r\n\x00\t") || strings.ContainsAny(in.Host, " /\\:") || len(in.Host) > 253 || len(in.Username) > 320 || len(in.From) > 320 || password != nil && len(*password) > 4096 || clear && (in.Enabled || password != nil && *password != "") {
		return SMTPSettings{}, conflict("invalid_smtp", "SMTP配置无效")
	}
	if in.Port < 1 || in.Port > 65535 || strings.TrimSpace(in.Host) == "" && in.Enabled {
		return SMTPSettings{}, conflict("invalid_smtp", "SMTP主机无效")
	}
	if in.TLSMode == "" {
		if in.Port == 465 {
			in.TLSMode = "implicit_tls"
		} else {
			in.TLSMode = "starttls"
		}
	}
	if in.TLSMode != "implicit_tls" && in.TLSMode != "starttls" && in.TLSMode != "none" || in.TLSMode == "none" && (in.Username != "" || password != nil && *password != "") {
		return SMTPSettings{}, conflict("invalid_smtp", "SMTP TLS模式无效")
	}
	address, addressErr := mail.ParseAddress(strings.TrimSpace(in.From))
	if strings.TrimSpace(in.From) != "" && (addressErr != nil || address.Address != strings.TrimSpace(in.From)) || in.Enabled && strings.TrimSpace(in.From) == "" {
		return SMTPSettings{}, conflict("invalid_smtp", "发件地址无效")
	}
	tx, e := settingsManagementTx(ctx, s, actor)
	if e != nil {
		return SMTPSettings{}, e
	}
	defer tx.Rollback(ctx)
	var old SMTPSettings
	var sealed []byte
	if e = tx.QueryRow(ctx, "SELECT configured,enabled,host,port,username,password_encrypted,from_address,tls_mode,from_name,version,updated_at FROM smtp_settings WHERE id=1 FOR UPDATE").Scan(&old.Configured, &old.Enabled, &old.Host, &old.Port, &old.Username, &sealed, &old.From, &old.TLSMode, &old.FromName, &old.Version, &old.UpdatedAt); e != nil {
		return old, e
	}
	if e = recheckSettingsManager(ctx, tx, actor, sessionHash); e != nil {
		return old, e
	}
	var super bool
	if e = tx.QueryRow(ctx, "SELECT role='super_admin' AND status='active' FROM users WHERE id=$1", actor).Scan(&super); e != nil || !super {
		return SMTPSettings{}, conflict("permission_denied", "需要超级管理员权限")
	}
	if old.Version != expected {
		return old, conflict("version_conflict", "SMTP配置已变更，请重新加载")
	}
	if clear {
		sealed = []byte{}
	} else if password != nil && *password != "" {
		sealed, e = smtpSeal(s.ProvisionEncryptionKey, *password)
		if e != nil {
			return old, e
		}
	}
	if !old.Configured && !clear && len(sealed) == 0 && fallback.Password != "" && (password == nil || *password == "") {
		sealed, e = smtpSeal(s.ProvisionEncryptionKey, fallback.Password)
		if e != nil {
			return SMTPSettings{}, conflict("smtp_encryption_unconfigured", "凭据加密密钥未配置")
		}
	}
	if in.Enabled && ((strings.TrimSpace(in.Username) == "") != (len(sealed) == 0)) {
		return SMTPSettings{}, conflict("invalid_smtp", "SMTP用户名和密码必须成对配置")
	}
	if in.TLSMode == "none" && len(sealed) > 0 {
		return SMTPSettings{}, conflict("invalid_smtp", "明文SMTP不可配置凭据")
	}
	var out SMTPSettings
	e = tx.QueryRow(ctx, "UPDATE smtp_settings SET configured=true,enabled=$1,host=$2,port=$3,username=$4,password_encrypted=$5,from_address=$6,tls_mode=$7,from_name=$8,version=version+1,updated_at=now() WHERE id=1 RETURNING enabled,host,port,username,password_encrypted,from_address,tls_mode,from_name,version,updated_at", in.Enabled, strings.TrimSpace(in.Host), in.Port, strings.TrimSpace(in.Username), sealed, strings.TrimSpace(in.From), in.TLSMode, strings.TrimSpace(in.FromName)).Scan(&out.Enabled, &out.Host, &out.Port, &out.Username, &sealed, &out.From, &out.TLSMode, &out.FromName, &out.Version, &out.UpdatedAt)
	out.PasswordConfigured = len(sealed) > 0
	out.Configured = true
	out.EncryptionConfigured = len(s.ProvisionEncryptionKey) == 32
	if e != nil {
		return old, e
	}
	passwordValue := ""
	if len(sealed) > 0 {
		passwordValue, e = smtpOpen(s.ProvisionEncryptionKey, sealed)
		if e != nil {
			return old, conflict("smtp_encryption_unconfigured", "凭据加密密钥未配置")
		}
	}
	if e = s.syncChannelInstanceFromLegacy(ctx, tx, "mail-smtp-default", "mail", "smtp", out.Enabled, map[string]any{
		"host":      out.Host,
		"port":      out.Port,
		"username":  out.Username,
		"from":      out.From,
		"tls_mode":  out.TLSMode,
		"from_name": out.FromName,
	}, passwordValue); e != nil {
		return old, e
	}
	if e = Audit(ctx, tx, actor, "smtp.settings_updated", "smtp_settings", 1, map[string]any{"version": out.Version}); e != nil {
		return old, e
	}
	return out, tx.Commit(ctx)
}

func (s *Store) QueueSMTPTest(ctx context.Context, actor int64, sessionHash, recipient string, expected int64, channelInstanceID ...string) (int64, error) {
	recipient = strings.TrimSpace(recipient)
	a, e := mail.ParseAddress(recipient)
	if e != nil || a.Address != recipient || len(recipient) > 254 || expected < 1 || strings.ContainsAny(recipient, "\r\n\x00") {
		return 0, conflict("invalid_smtp", "试发参数无效")
	}
	tx, e := settingsManagementTx(ctx, s, actor)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback(ctx)
	if e = recheckSettingsManager(ctx, tx, actor, sessionHash); e != nil {
		return 0, e
	}
	var super bool
	if e = tx.QueryRow(ctx, "SELECT role='super_admin' AND status='active' FROM users WHERE id=$1", actor).Scan(&super); e != nil || !super {
		return 0, conflict("permission_denied", "需要超级管理员权限")
	}
	var version int64
	var configured, enabled bool
	if e = tx.QueryRow(ctx, "SELECT version,configured,enabled FROM smtp_settings WHERE id=1 FOR UPDATE").Scan(&version, &configured, &enabled); e != nil {
		return 0, e
	}
	if version != expected {
		return 0, conflict("version_conflict", "SMTP配置已变更，请重新加载")
	}
	instanceID := ""
	if len(channelInstanceID) > 0 {
		instanceID = strings.TrimSpace(channelInstanceID[0])
		if instanceID != "" {
			var valid bool
			if e = tx.QueryRow(ctx, `SELECT category='mail' AND implementation_id='smtp' AND enabled FROM channel_instances WHERE id=$1`, instanceID).Scan(&valid); e != nil {
				return 0, e
			}
			if !valid {
				return 0, conflict("invalid_smtp", "邮件渠道未启用")
			}
		}
	}
	if instanceID == "" && (!configured || !enabled) {
		return 0, conflict("invalid_smtp", "请先保存并启用SMTP配置")
	}
	var id int64
	if e = tx.QueryRow(ctx, "INSERT INTO email_outbox(recipient,subject,body,kind,channel_instance_id) VALUES($1,'SMTP test','SMTP configuration test message.','system',COALESCE(NULLIF($2,''),(SELECT id FROM channel_instances WHERE category='mail' AND implementation_id='smtp' AND enabled=true ORDER BY sort_order,id LIMIT 1))) RETURNING id", recipient, instanceID).Scan(&id); e != nil {
		return 0, e
	}
	if e = Audit(ctx, tx, actor, "smtp.test_queued", "email_outbox", id, map[string]any{"version": version}); e != nil {
		return 0, e
	}
	return id, tx.Commit(ctx)
}
func (s *Store) ResolveSMTPConfig(ctx context.Context, fallback SMTPRuntimeConfig) (SMTPRuntimeConfig, error) {
	var enabled bool
	var host string
	var port int
	var user string
	var sealed []byte
	var from string
	var configured bool
	var tlsMode, fromName string
	err := s.Pool.QueryRow(ctx, "SELECT enabled,host,port,username,password_encrypted,from_address,tls_mode,from_name,configured FROM smtp_settings WHERE id=1").Scan(&enabled, &host, &port, &user, &sealed, &from, &tlsMode, &fromName, &configured)
	if err == pgx.ErrNoRows {
		return fallback, nil
	}
	if err != nil {
		return SMTPRuntimeConfig{}, err
	}
	if !configured || !enabled {
		var instanceID string
		if lookupErr := s.Pool.QueryRow(ctx, `SELECT id FROM channel_instances WHERE category='mail' AND implementation_id='smtp' AND enabled=true ORDER BY sort_order,id LIMIT 1`).Scan(&instanceID); lookupErr == nil {
			return s.ResolveSMTPConfigForInstance(ctx, instanceID, fallback)
		}
		if !configured {
			return fallback, nil
		}
		return SMTPRuntimeConfig{}, nil
	}
	pass, e := smtpOpen(s.ProvisionEncryptionKey, sealed)
	if e != nil {
		return SMTPRuntimeConfig{}, fmt.Errorf("SMTP credentials unavailable")
	}
	return SMTPRuntimeConfig{Host: host, Port: port, Username: user, Password: pass, From: from, TLSMode: tlsMode, FromName: fromName}, nil
}

// ResolveSMTPConfigForInstance resolves the SMTP channel selected for an
// outbox row. Empty IDs retain compatibility with legacy singleton rows.
func (s *Store) ResolveSMTPConfigForInstance(ctx context.Context, instanceID string, fallback SMTPRuntimeConfig) (SMTPRuntimeConfig, error) {
	if strings.TrimSpace(instanceID) == "" {
		return s.ResolveSMTPConfig(ctx, fallback)
	}
	var category, implementation string
	var enabled bool
	var rawConfig []byte
	if err := s.Pool.QueryRow(ctx, `SELECT category,implementation_id,enabled,config FROM channel_instances WHERE id=$1`, instanceID).Scan(&category, &implementation, &enabled, &rawConfig); err != nil {
		return SMTPRuntimeConfig{}, fmt.Errorf("SMTP channel unavailable")
	}
	if category != "mail" || implementation != "smtp" {
		return SMTPRuntimeConfig{}, fmt.Errorf("SMTP channel unavailable")
	}
	if !enabled {
		return SMTPRuntimeConfig{}, nil
	}
	var config map[string]any
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return SMTPRuntimeConfig{}, fmt.Errorf("SMTP channel unavailable")
	}
	out := SMTPRuntimeConfig{
		Host:     configString(config, "host"),
		Port:     configInt(config, "port"),
		Username: configString(config, "username"),
		From:     configString(config, "from"),
		TLSMode:  configString(config, "tls_mode"),
		FromName: configString(config, "from_name"),
	}
	if out.Port == 0 {
		if out.TLSMode == "implicit_tls" {
			out.Port = 465
		} else {
			out.Port = 587
		}
	}
	if out.Username != "" {
		secret, err := s.ChannelInstanceSecret(ctx, instanceID)
		if err != nil || secret == "" {
			return SMTPRuntimeConfig{}, fmt.Errorf("SMTP credentials unavailable")
		}
		out.Password = secret
	}
	if strings.TrimSpace(out.Host) == "" || strings.TrimSpace(out.From) == "" {
		return SMTPRuntimeConfig{}, fmt.Errorf("SMTP channel unavailable")
	}
	return out, nil
}
