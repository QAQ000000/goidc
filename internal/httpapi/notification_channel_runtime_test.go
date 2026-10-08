package httpapi

import (
	"bytes"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func TestSMSJobUsesBoundChannelInstance(t *testing.T) {
	db, h := testServer(t)
	db.ProvisionEncryptionKey = bytes.Repeat([]byte{17}, 32)
	actor, _ := user(t, db, h, "sms-instance-root", "super_admin")
	var sessionHash string
	if err := db.Pool.QueryRow(t.Context(), "SELECT token_hash FROM sessions WHERE user_id=$1", actor).Scan(&sessionHash); err != nil {
		t.Fatal(err)
	}
	token := "legacy-token"
	if _, err := db.UpdateSMSSettings(t.Context(), actor, sessionHash, store.SMSSettings{Enabled: true, Provider: "generic_http_json", Endpoint: "http://127.0.0.1:19488/sms"}, &token, false, 1); err != nil {
		t.Fatal(err)
	}
	altToken := "instance-token"
	_, err := db.CreateChannelInstance(t.Context(), actor, sessionHash, store.ChannelInstanceInput{
		ID:               "sms-runtime-alt",
		Category:         "sms",
		ImplementationID: "generic_http_json",
		DisplayName:      "备用短信",
		Enabled:          true,
		Config:           map[string]any{"endpoint": "http://127.0.0.1:19489/sms"},
		Secret:           &altToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := db.ResolveSMSConfigForInstance(t.Context(), "sms-runtime-alt")
	if err != nil || !runtime.Enabled || runtime.Provider != "generic_http_json" || runtime.Endpoint != "http://127.0.0.1:19489/sms" || runtime.Token != altToken {
		t.Fatalf("instance runtime = %#v, err=%v", runtime, err)
	}
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueueSMS(t.Context(), tx, "+8613800138000", "验证码：123456", "password_reset", "", time.Now().Add(time.Minute)); err != nil {
		tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE sms_outbox SET channel_instance_id='sms-runtime-alt'"); err != nil {
		t.Fatal(err)
	}
	job, err := db.ClaimSMSJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if job.ChannelInstanceID != "sms-runtime-alt" {
		t.Fatalf("claimed channel instance = %q", job.ChannelInstanceID)
	}
}

func TestEmailJobUsesBoundChannelInstance(t *testing.T) {
	db, h := testServer(t)
	db.ProvisionEncryptionKey = bytes.Repeat([]byte{19}, 32)
	actor, _ := user(t, db, h, "mail-instance-root", "super_admin")
	var sessionHash string
	if err := db.Pool.QueryRow(t.Context(), "SELECT token_hash FROM sessions WHERE user_id=$1", actor).Scan(&sessionHash); err != nil {
		t.Fatal(err)
	}
	password := "instance-password"
	_, err := db.CreateChannelInstance(t.Context(), actor, sessionHash, store.ChannelInstanceInput{
		ID:               "mail-runtime-alt",
		Category:         "mail",
		ImplementationID: "smtp",
		DisplayName:      "备用 SMTP",
		Enabled:          true,
		Config:           map[string]any{"host": "smtp.example.test", "port": 587, "username": "mailer@example.test", "from": "mailer@example.test", "tls_mode": "starttls", "from_name": "测试邮件"},
		Secret:           &password,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := db.ResolveSMTPConfigForInstance(t.Context(), "mail-runtime-alt", store.SMTPRuntimeConfig{})
	if err != nil || runtime.Host != "smtp.example.test" || runtime.Port != 587 || runtime.Username != "mailer@example.test" || runtime.Password != password || runtime.FromName != "测试邮件" {
		t.Fatalf("instance runtime = %#v, err=%v", runtime, err)
	}
	if _, err = db.Pool.Exec(t.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind,channel_instance_id) VALUES('to@example.test','runtime test','message','system','mail-runtime-alt')`); err != nil {
		t.Fatal(err)
	}
	job, err := db.ClaimEmailJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if job.ChannelInstanceID != "mail-runtime-alt" {
		t.Fatalf("claimed channel instance = %q", job.ChannelInstanceID)
	}
}
