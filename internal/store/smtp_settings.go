package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/mail"
	"strings"
	"time"
)

type SMTPSettings struct {
	Configured           bool      `json:"configured"`
	EncryptionConfigured bool      `json:"encryption_configured"`
	Enabled              bool      `json:"enabled"`
	Host                 string    `json:"host"`
	Port                 int       `json:"port"`
	Username             string    `json:"username"`
	From                 string    `json:"from"`
	PasswordConfigured   bool      `json:"password_configured"`
	Version              int64     `json:"version"`
	UpdatedAt            time.Time `json:"updated_at"`
}
type SMTPRuntimeConfig struct {
	Host                     string
	Port                     int
	Username, Password, From string
}

func smtpSeal(key []byte, value string) ([]byte, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	n := make([]byte, g.NonceSize())
	if _, e = rand.Read(n); e != nil {
		return nil, e
	}
	raw, _ := json.Marshal(value)
	return g.Seal(n, n, raw, []byte("smtp:password")), nil
}
func smtpOpen(key, sealed []byte) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	b, e := aes.NewCipher(key)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return "", e
	}
	if len(sealed) < g.NonceSize() {
		return "", fmt.Errorf("invalid smtp secret")
	}
	raw, e := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], []byte("smtp:password"))
	if e != nil {
		return "", e
	}
	var out string
	e = json.Unmarshal(raw, &out)
	return out, e
}
func (s *Store) SMTPSettings(ctx context.Context, fallback SMTPRuntimeConfig) (SMTPSettings, error) {
	var o SMTPSettings
	var sealed []byte
	err := s.Pool.QueryRow(ctx, "SELECT configured,enabled,host,port,username,password_encrypted,from_address,version,updated_at FROM smtp_settings WHERE id=1").Scan(&o.Configured, &o.Enabled, &o.Host, &o.Port, &o.Username, &sealed, &o.From, &o.Version, &o.UpdatedAt)
	o.PasswordConfigured = len(sealed) > 0
	o.EncryptionConfigured = len(s.ProvisionEncryptionKey) == 32
	if !o.Configured {
		o.Host = fallback.Host
		o.Port = fallback.Port
		o.Username = fallback.Username
		o.From = fallback.From
		o.Enabled = fallback.Host != "" && fallback.From != ""
		o.PasswordConfigured = fallback.Password != ""
	}
	return o, err
}
func (s *Store) UpdateSMTPSettings(ctx context.Context, actor int64, sessionHash string, in SMTPSettings, password *string, clear bool, expected int64, fallback SMTPRuntimeConfig) (SMTPSettings, error) {
	if strings.ContainsAny(in.Host+in.Username+in.From, "\r\n") || strings.ContainsAny(in.Host, " /\\:\t") || len(in.Host) > 253 || len(in.Username) > 320 || len(in.From) > 320 || password != nil && len(*password) > 4096 || clear && in.Enabled {
		return SMTPSettings{}, conflict("invalid_smtp", "SMTP配置无效")
	}
	if in.Port < 1 || in.Port > 65535 || strings.TrimSpace(in.Host) == "" && in.Enabled {
		return SMTPSettings{}, conflict("invalid_smtp", "SMTP主机无效")
	}
	if _, e := mail.ParseAddress(in.From); e != nil && in.Enabled {
		return SMTPSettings{}, conflict("invalid_smtp", "发件地址无效")
	}
	tx, e := settingsManagementTx(ctx, s, actor)
	if e != nil {
		return SMTPSettings{}, e
	}
	defer tx.Rollback(ctx)
	var old SMTPSettings
	var sealed []byte
	if e = tx.QueryRow(ctx, "SELECT configured,enabled,host,port,username,password_encrypted,from_address,version,updated_at FROM smtp_settings WHERE id=1 FOR UPDATE").Scan(&old.Configured, &old.Enabled, &old.Host, &old.Port, &old.Username, &sealed, &old.From, &old.Version, &old.UpdatedAt); e != nil {
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
	var out SMTPSettings
	e = tx.QueryRow(ctx, "UPDATE smtp_settings SET configured=true,enabled=$1,host=$2,port=$3,username=$4,password_encrypted=$5,from_address=$6,version=version+1,updated_at=now() WHERE id=1 RETURNING enabled,host,port,username,password_encrypted,from_address,version,updated_at", in.Enabled, strings.TrimSpace(in.Host), in.Port, strings.TrimSpace(in.Username), sealed, strings.TrimSpace(in.From)).Scan(&out.Enabled, &out.Host, &out.Port, &out.Username, &sealed, &out.From, &out.Version, &out.UpdatedAt)
	out.PasswordConfigured = len(sealed) > 0
	out.Configured = true
	out.EncryptionConfigured = len(s.ProvisionEncryptionKey) == 32
	if e != nil {
		return old, e
	}
	if e = Audit(ctx, tx, actor, "smtp.settings_updated", "smtp_settings", 1, map[string]any{"version": out.Version}); e != nil {
		return old, e
	}
	return out, tx.Commit(ctx)
}
func (s *Store) ResolveSMTPConfig(ctx context.Context, fallback SMTPRuntimeConfig) (SMTPRuntimeConfig, error) {
	var enabled bool
	var host string
	var port int
	var user string
	var sealed []byte
	var from string
	var configured bool
	err := s.Pool.QueryRow(ctx, "SELECT enabled,host,port,username,password_encrypted,from_address,configured FROM smtp_settings WHERE id=1").Scan(&enabled, &host, &port, &user, &sealed, &from, &configured)
	if err == pgx.ErrNoRows {
		return fallback, nil
	}
	if err != nil {
		return SMTPRuntimeConfig{}, err
	}
	if !configured {
		return fallback, nil
	}
	if !enabled {
		return SMTPRuntimeConfig{}, nil
	}
	pass, e := smtpOpen(s.ProvisionEncryptionKey, sealed)
	if e != nil {
		return SMTPRuntimeConfig{}, fmt.Errorf("SMTP credentials unavailable")
	}
	return SMTPRuntimeConfig{Host: host, Port: port, Username: user, Password: pass, From: from}, nil
}
