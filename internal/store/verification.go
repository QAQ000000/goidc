package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/secure"
	"github.com/jackc/pgx/v5"
)

func VerificationID(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s *Store) verificationHash(id, code string) (string, error) {
	if len(s.ProvisionEncryptionKey) != 32 {
		return "", conflict("verification_unconfigured", "验证码加密尚未配置")
	}
	h := hmac.New(sha256.New, s.ProvisionEncryptionKey)
	h.Write([]byte("zjmfgo:verification:v1:" + id + ":" + code))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Store) VerificationPayload(id string, raw []byte, seal bool) ([]byte, error) {
	if len(s.ProvisionEncryptionKey) != 32 {
		return nil, conflict("verification_unconfigured", "验证码加密尚未配置")
	}
	aadBytes := []byte("zjmfgo:verification-delivery:v1:" + id)
	if seal {
		return secure.Seal(s.ProvisionEncryptionKey, aadBytes, raw)
	}
	out, err := secure.Open(s.ProvisionEncryptionKey, aadBytes, raw)
	if err != nil {
		return nil, fmt.Errorf("verification delivery unavailable")
	}
	return out, err
}

type VerificationChallenge struct {
	ID, Purpose, Channel, Recipient string
	UserID                          *int64
	ExpiresAt                       time.Time
}

// The caller locks the account first, then this per-recipient scope.
func (s *Store) IssueVerification(ctx context.Context, tx pgx.Tx, purpose, channel, recipient string, userID *int64, deliver bool) (string, error) {
	if err := lockOperationKey(ctx, tx, "verification-issue", purpose+":"+channel+":"+recipient); err != nil {
		return "", err
	}
	var recent bool
	var count int
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM verification_challenges WHERE purpose=$1 AND channel=$2 AND recipient=$3 AND created_at>clock_timestamp()-interval '60 seconds'),(SELECT count(*) FROM verification_challenges WHERE purpose=$1 AND channel=$2 AND recipient=$3 AND created_at>clock_timestamp()-interval '1 hour')`, purpose, channel, recipient).Scan(&recent, &count); err != nil {
		return "", err
	}
	if recent || count >= 5 {
		return "", conflict("verification_rate_limited", "请稍后再发送验证码")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	publicID := hex.EncodeToString(raw)
	id := VerificationID(publicID)
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	hash, err := s.verificationHash(id, code)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, "UPDATE verification_challenges SET consumed_at=clock_timestamp() WHERE purpose=$1 AND channel=$2 AND recipient=$3 AND consumed_at IS NULL", purpose, channel, recipient); err != nil {
		return "", err
	}
	var expires time.Time
	minutes := 10
	if channel == "sms" {
		minutes = 5
	}
	if err = tx.QueryRow(ctx, `INSERT INTO verification_challenges(id,purpose,channel,recipient,user_id,code_hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,clock_timestamp()+$7*interval '1 minute') RETURNING expires_at`, id, purpose, channel, recipient, userID, hash, minutes).Scan(&expires); err != nil {
		return "", err
	}
	if !deliver {
		return publicID, nil
	}
	if channel == "sms" {
		if err = s.QueueSMSVerification(ctx, tx, recipient, code, purpose, id, expires); err != nil {
			return "", err
		}
	} else {
		kind := "verification_code"
		if purpose == "password_reset" {
			kind = "password_reset"
		}
		var subject, body string
		var active bool
		if err = tx.QueryRow(ctx, "SELECT subject,body,active FROM notification_templates WHERE channel='email' AND kind=$1", kind).Scan(&subject, &body, &active); err != nil {
			return "", err
		}
		if !active || !strings.Contains(body, "{{code}}") || strings.Contains(body, "{{reset_link}}") {
			return "", conflict("verification_template_unavailable", "验证码模板未启用或缺少验证码")
		}
		body = strings.ReplaceAll(strings.ReplaceAll(body, "{{code}}", code), "{{expires_minutes}}", "10")
		sealed, sealErr := s.VerificationPayload(id, []byte(body), true)
		if sealErr != nil {
			return "", sealErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO email_outbox(recipient,subject,body,body_encrypted,kind,expires_at,verification_id,channel_instance_id) VALUES($1,$2,'',$3,$4,$5,$6,(SELECT id FROM channel_instances WHERE category='mail' AND implementation_id='smtp' AND enabled=true ORDER BY sort_order,id LIMIT 1))`, recipient, subject, sealed, kind, expires, id); err != nil {
			return "", err
		}
	}
	return publicID, nil
}

// Invalid attempts must be committed by the caller, including lockout.
func (s *Store) ConsumeVerification(ctx context.Context, tx pgx.Tx, rawID, code, purpose string, userID *int64) (VerificationChallenge, bool, error) {
	var out VerificationChallenge
	var hash string
	var consumed *time.Time
	var attempts int
	id := VerificationID(rawID)
	err := tx.QueryRow(ctx, `SELECT id,purpose,channel,recipient,user_id,expires_at,code_hash,consumed_at,attempts FROM verification_challenges WHERE id=$1 FOR UPDATE`, id).Scan(&out.ID, &out.Purpose, &out.Channel, &out.Recipient, &out.UserID, &out.ExpiresAt, &hash, &consumed, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if consumed != nil || attempts >= 5 || out.Purpose != purpose || time.Now().After(out.ExpiresAt) || (userID != nil && (out.UserID == nil || *out.UserID != *userID)) {
		return out, false, nil
	}
	expected, err := s.verificationHash(id, code)
	if err != nil {
		return out, false, err
	}
	valid := len(code) == 6 && hmac.Equal([]byte(expected), []byte(hash))
	_, err = tx.Exec(ctx, `UPDATE verification_challenges SET attempts=attempts+1,consumed_at=CASE WHEN $2 OR attempts>=4 THEN clock_timestamp() ELSE NULL END WHERE id=$1`, id, valid)
	return out, valid, err
}
