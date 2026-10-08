package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type CertificationStore struct{}

func sealCertification(key []byte, id string, payload any) ([]byte, string, error) {
	if len(key) != 32 {
		return nil, "", errors.New("certification encryption unavailable")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, "", err
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		return nil, "", err
	}
	aad := "zjmfgo:certification:v1:" + id
	nonce := make([]byte, g.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, "", err
	}
	return g.Seal(nonce, nonce, raw, []byte(aad)), aad, nil
}

func openCertification(key []byte, aad string, sealed []byte) (map[string]any, error) {
	if len(key) != 32 || len(sealed) < 12 {
		return nil, errors.New("certification data unavailable")
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	raw, err := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], []byte(aad))
	if err != nil {
		return nil, errors.New("certification data unavailable")
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, errors.New("certification data unavailable")
	}
	return out, nil
}

func validateCertificationPayload(kind string, payload map[string]any) error {
	if kind != "individual" && kind != "company" {
		return conflict("invalid_certification", "认证类型无效")
	}
	if payload == nil {
		return conflict("invalid_certification", "认证资料不能为空")
	}
	required := []string{"name", "id_number"}
	if kind == "company" {
		required = append(required, "company_name", "company_registration_number")
	}
	for _, key := range required {
		value, ok := payload[key].(string)
		if !ok || strings.TrimSpace(value) == "" || len(value) > 240 {
			return conflict("invalid_certification", "认证资料不完整")
		}
	}
	if len(payload) > 20 {
		return conflict("invalid_certification", "认证资料字段过多")
	}
	return nil
}

type CertificationSummary struct {
	ID                                        int64 `json:"id"`
	Type, Status, ProviderID, RejectionReason string
	Version                                   int64
	UpdatedAt                                 time.Time
}

func (s *Store) Certification(ctx context.Context, userID int64) (map[string]any, error) {
	var p CertificationSummary
	err := s.Pool.QueryRow(ctx, `SELECT id,certification_type,status,provider_id,rejection_reason,version,updated_at FROM certification_profiles WHERE user_id=$1`, userID).Scan(&p.ID, &p.Type, &p.Status, &p.ProviderID, &p.RejectionReason, &p.Version, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{"status": "unverified", "certification_type": "individual", "provider_id": "manual", "version": 0}, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": p.ID, "certification_type": p.Type, "status": p.Status, "provider_id": p.ProviderID, "rejection_reason": p.RejectionReason, "version": p.Version, "updated_at": p.UpdatedAt}, nil
}

func (s *Store) SubmitCertification(ctx context.Context, userID int64, kind string, payload map[string]any) (map[string]any, error) {
	if err := validateCertificationPayload(kind, payload); err != nil {
		return nil, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var profileID, version int64
	var status string
	err = tx.QueryRow(ctx, `INSERT INTO certification_profiles(user_id,certification_type,status,provider_id) VALUES($1,$2,'pending','manual') ON CONFLICT(user_id) DO UPDATE SET certification_type=EXCLUDED.certification_type,status='pending',provider_id='manual',rejection_reason='',version=certification_profiles.version+1,updated_at=now() RETURNING id,status,version`, userID, kind).Scan(&profileID, &status, &version)
	if err != nil {
		return nil, err
	}
	var submissionID int64
	sealed, aad, err := sealCertification(s.ProvisionEncryptionKey, fmt.Sprintf("%d", profileID), payload)
	if err != nil {
		return nil, conflict("certification_unconfigured", "认证加密尚未配置")
	}
	err = tx.QueryRow(ctx, `INSERT INTO certification_submissions(profile_id,user_id,certification_type,provider_id,status,payload_encrypted,payload_aad) VALUES($1,$2,$3,'manual','pending',$4,$5) RETURNING id`, profileID, userID, kind, sealed, aad).Scan(&submissionID)
	if err != nil {
		return nil, err
	}
	if err = Audit(ctx, tx, userID, "certification.submitted", "certification_submission", submissionID, map[string]any{"type": kind}); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"id": submissionID, "profile_id": profileID, "status": status, "version": version}, nil
}

func (s *Store) AdminCertifications(ctx context.Context, status string, limit, offset int) ([]map[string]any, error) {
	args := []any{limit, offset}
	where := ""
	if status != "" {
		where = " WHERE p.status=$3"
		args = append(args, status)
	}
	rows, err := s.Pool.Query(ctx, `SELECT p.id,p.user_id,u.username,u.display_name,u.email,p.certification_type,p.status,p.provider_id,p.rejection_reason,p.version,p.updated_at,(SELECT max(id) FROM certification_submissions x WHERE x.profile_id=p.id) AS submission_id FROM certification_profiles p JOIN users u ON u.id=p.user_id`+where+` ORDER BY p.updated_at DESC,p.id DESC LIMIT $1 OFFSET $2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToMap)
}

func (s *Store) AdminCertificationDetail(ctx context.Context, id int64) (map[string]any, error) {
	var userID, submissionID int64
	var kind, status, provider, reason string
	var version int64
	var sealed []byte
	var aad string
	var submitted time.Time
	err := s.Pool.QueryRow(ctx, `SELECT p.user_id,p.certification_type,p.status,p.provider_id,p.rejection_reason,p.version,sub.id,sub.payload_encrypted,sub.payload_aad,sub.submitted_at FROM certification_profiles p LEFT JOIN LATERAL (SELECT id,payload_encrypted,payload_aad,submitted_at FROM certification_submissions WHERE profile_id=p.id ORDER BY id DESC LIMIT 1) sub ON true WHERE p.id=$1`, id).Scan(&userID, &kind, &status, &provider, &reason, &version, &submissionID, &sealed, &aad, &submitted)
	if err != nil {
		return nil, err
	}
	payload, err := openCertification(s.ProvisionEncryptionKey, aad, sealed)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "user_id": userID, "certification_type": kind, "status": status, "provider_id": provider, "rejection_reason": reason, "version": version, "submission_id": submissionID, "submitted_at": submitted, "payload": payload}, nil
}

func (s *Store) ReviewCertification(ctx context.Context, reviewerID, id int64, decision, note string, expected int64) error {
	if decision != "approved" && decision != "rejected" {
		return conflict("invalid_certification_review", "审核结果无效")
	}
	if len(note) > 2000 {
		return conflict("invalid_certification_review", "审核备注过长")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var profileID, submissionID, version int64
	var userID int64
	var status string
	err = tx.QueryRow(ctx, `SELECT p.id,p.user_id,p.version,p.status,(SELECT id FROM certification_submissions WHERE profile_id=p.id ORDER BY id DESC LIMIT 1) FROM certification_profiles p WHERE p.id=$1 FOR UPDATE`, id).Scan(&profileID, &userID, &version, &status, &submissionID)
	if err != nil {
		return err
	}
	if expected > 0 && expected != version {
		return conflict("version_conflict", "认证状态已变更，请刷新")
	}
	if status != "pending" {
		return conflict("certification_not_pending", "认证当前不在待审核状态")
	}
	if _, err = tx.Exec(ctx, `UPDATE certification_profiles SET status=$2,rejection_reason=CASE WHEN $2='rejected' THEN $3 ELSE '' END,version=version+1,updated_at=now() WHERE id=$1`, id, decision, note); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE certification_submissions SET status=$2,reviewed_at=now(),reviewed_by=$3,review_note=$4 WHERE id=$1`, submissionID, decision, reviewerID, note); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO certification_reviews(submission_id,profile_id,reviewer_id,decision,note) VALUES($1,$2,$3,$4,$5)`, submissionID, profileID, reviewerID, decision, note); err != nil {
		return err
	}
	if err = Audit(ctx, tx, reviewerID, "certification.reviewed", "certification", id, map[string]any{"user_id": userID, "decision": decision}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
