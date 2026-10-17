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

// certificationEnabled reads the master switch behind legacy `certifi_open`.
// The settings row is seeded by migration 093, but a missing row means
// "closed" (legacy install default is '0') instead of an error: the switch
// must never be assumed on.
func certificationEnabled(ctx context.Context, q sqlQueryer) (bool, error) {
	var enabled bool
	err := q.QueryRow(ctx, "SELECT enabled FROM certification_settings WHERE id=1").Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return enabled, nil
}

// CertificationGate rejects certification writes while the master switch is
// off, mirroring the legacy CERTIFI_CLOSE check that every submit endpoint
// performs before reading any payload.
func (s *Store) CertificationGate(ctx context.Context) error {
	return certificationGate(ctx, s.Pool)
}

func certificationGate(ctx context.Context, q sqlQueryer) error {
	enabled, err := certificationEnabled(ctx, q)
	if err != nil {
		return err
	}
	if !enabled {
		return conflict("certification_disabled", "实名认证已关闭")
	}
	return nil
}

// certificationPhoneMatch mirrors legacy `certifi_isbindphone` including its
// three lenient behaviours: only a payload that actually carries a phone is
// checked, only an account with a bound phone can be rejected, and an account
// without a bound phone always passes.
func certificationPhoneMatch(ctx context.Context, q sqlQueryer, userID int64, payload map[string]any) error {
	var required bool
	if err := q.QueryRow(ctx, "SELECT require_phone_match FROM certification_settings WHERE id=1").Scan(&required); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if !required {
		return nil
	}
	phone, _ := payload["phone"].(string)
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return nil
	}
	var bound string
	if err := q.QueryRow(ctx, "SELECT phone FROM users WHERE id=$1", userID).Scan(&bound); err != nil {
		return err
	}
	if bound == "" {
		return nil
	}
	if bound != phone {
		return conflict("certification_phone_mismatch", "手机号与绑定手机不一致")
	}
	return nil
}

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
	enabled, err := certificationEnabled(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	var p CertificationSummary
	err = s.Pool.QueryRow(ctx, `SELECT id,certification_type,status,provider_id,rejection_reason,version,updated_at FROM certification_profiles WHERE user_id=$1`, userID).Scan(&p.ID, &p.Type, &p.Status, &p.ProviderID, &p.RejectionReason, &p.Version, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{"enabled": enabled, "status": "unverified", "certification_type": "individual", "provider_id": "manual", "version": 0}, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"enabled": enabled, "id": p.ID, "certification_type": p.Type, "status": p.Status, "provider_id": p.ProviderID, "rejection_reason": p.RejectionReason, "version": p.Version, "updated_at": p.UpdatedAt}, nil
}

func (s *Store) SubmitCertification(ctx context.Context, userID int64, kind string, payload map[string]any) (map[string]any, error) {
	if err := certificationGate(ctx, s.Pool); err != nil {
		return nil, err
	}
	if err := validateCertificationPayload(kind, payload); err != nil {
		return nil, err
	}
	// Legacy only enforces the bind-phone match on personal submissions
	// (personCertifiPost / personRealNameAuth); the company submit path never
	// checked certifi_isbindphone, so keep that asymmetry.
	if kind == "individual" {
		if err := certificationPhoneMatch(ctx, s.Pool, userID, payload); err != nil {
			return nil, err
		}
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

// ConvertCertificationToCompanyTx converts an approved individual profile to
// a company submission without deleting the previous personal history.
// The caller owns the transaction and must journal idempotency atomically.
func (s *Store) ConvertCertificationToCompanyTx(ctx context.Context, tx pgx.Tx, userID int64, payload map[string]any, expected int64) (map[string]any, error) {
	if err := certificationGate(ctx, tx); err != nil {
		return nil, err
	}
	if err := validateCertificationPayload("company", payload); err != nil {
		return nil, err
	}
	// personToCompany() delegates to companyCertifiPost(), which in legacy
	// never applies certifi_isbindphone; only the personal submit path does.
	var profileID, version int64
	var kind, status string
	if err := tx.QueryRow(ctx, `SELECT id,certification_type,status,version FROM certification_profiles WHERE user_id=$1 FOR UPDATE`, userID).Scan(&profileID, &kind, &status, &version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, conflict("certification_conversion_unavailable", "未找到可转换的个人认证")
		}
		return nil, err
	}
	if expected < 1 || expected != version {
		return nil, conflict("version_conflict", "认证状态已变更，请重新加载")
	}
	if kind != "individual" || status != "approved" {
		return nil, conflict("certification_conversion_unavailable", "仅已通过的个人认证可以转为企业认证")
	}
	sealed, aad, err := sealCertification(s.ProvisionEncryptionKey, fmt.Sprintf("%d", profileID), payload)
	if err != nil {
		return nil, conflict("certification_unconfigured", "认证加密尚未配置")
	}
	var submissionID int64
	if err = tx.QueryRow(ctx, `INSERT INTO certification_submissions(profile_id,user_id,certification_type,provider_id,status,payload_encrypted,payload_aad) VALUES($1,$2,'company','manual','pending',$3,$4) RETURNING id`, profileID, userID, sealed, aad).Scan(&submissionID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE certification_profiles SET certification_type='company',status='pending',provider_id='manual',rejection_reason='',version=version+1,updated_at=now() WHERE id=$1`, profileID); err != nil {
		return nil, err
	}
	if err = Audit(ctx, tx, userID, "certification.company_conversion_requested", "certification_profile", profileID, map[string]any{"previous_type": kind, "previous_status": status, "submission_id": submissionID, "version": version + 1}); err != nil {
		return nil, err
	}
	return map[string]any{"id": submissionID, "profile_id": profileID, "status": "pending", "version": version + 1}, nil
}

// AdminCertifications lists certification profiles visible to the reviewing
// administrator. The listing is filtered by the same customer scope the detail,
// history and review paths enforce, so an administrator whose scope excludes a
// customer never sees that customer's identity documents.
func (s *Store) AdminCertifications(ctx context.Context, actorID int64, status string, limit, offset int) ([]map[string]any, error) {
	args := []any{actorID, limit, offset}
	where := " WHERE admin_customer_allowed($1,p.user_id)"
	if status != "" {
		where += " AND p.status=$4"
		args = append(args, status)
	}
	rows, err := s.Pool.Query(ctx, `SELECT p.id,p.user_id,u.username,u.display_name,u.email,p.certification_type,p.status,p.provider_id,p.rejection_reason,p.version,p.updated_at,(SELECT max(id) FROM certification_submissions x WHERE x.profile_id=p.id) AS submission_id FROM certification_profiles p JOIN users u ON u.id=p.user_id`+where+` ORDER BY p.updated_at DESC,p.id DESC LIMIT $2 OFFSET $3`, args...)
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
	if err := s.ReviewCertificationTx(ctx, tx, reviewerID, id, decision, note, expected); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LockCertificationReview authorizes both a new decision and receipt replay.
// Read scope in a separate statement after waiting for the profile lock: a
// predicate in the locking SELECT would retain the statement's earlier snapshot.
// Staff changes take 70108190 exclusively; sales assignment takes the customer
// advisory lock exclusively. Hold both shared locks until the decision commits.
func (s *Store) LockCertificationReview(ctx context.Context, tx pgx.Tx, reviewerID, id int64) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return err
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT coalesce(u.status='active' AND (u.role='super_admin' OR (u.role='admin' AND r.active AND 'certifications.manage'=ANY(r.permissions))),false) FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1`, reviewerID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return conflict("permission_denied", "没有审核认证的权限")
	}
	var customerID int64
	if err := tx.QueryRow(ctx, "SELECT user_id FROM certification_profiles WHERE id=$1 FOR UPDATE", id).Scan(&customerID); err != nil {
		return err
	}
	if err := LockAdminCustomer(ctx, tx, reviewerID, customerID); err != nil {
		return err
	}
	return LockFileCredential(ctx, tx, reviewerID)
}

// ReviewCertificationTx applies a decision and its audit in the caller's
// transaction. Store callers enforce the same authorization as HTTP callers.
func (s *Store) ReviewCertificationTx(ctx context.Context, tx pgx.Tx, reviewerID, id int64, decision, note string, expected int64) error {
	if decision != "approved" && decision != "rejected" {
		return conflict("invalid_certification_review", "审核结果无效")
	}
	if len(note) > 2000 {
		return conflict("invalid_certification_review", "审核备注过长")
	}
	if err := s.LockCertificationReview(ctx, tx, reviewerID, id); err != nil {
		return err
	}
	var profileID, version int64
	var submissionID *int64
	var userID int64
	var status string
	var sealed []byte
	var aad *string
	err := tx.QueryRow(ctx, `SELECT p.id,p.user_id,p.version,p.status,(SELECT id FROM certification_submissions WHERE profile_id=p.id ORDER BY id DESC LIMIT 1),(SELECT payload_encrypted FROM certification_submissions WHERE profile_id=p.id ORDER BY id DESC LIMIT 1),(SELECT payload_aad FROM certification_submissions WHERE profile_id=p.id ORDER BY id DESC LIMIT 1) FROM certification_profiles p WHERE p.id=$1`, id).Scan(&profileID, &userID, &version, &status, &submissionID, &sealed, &aad)
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
	if submissionID != nil {
		if _, err = tx.Exec(ctx, `UPDATE certification_submissions SET status=$2,reviewed_at=now(),reviewed_by=$3,review_note=$4 WHERE id=$1`, *submissionID, decision, reviewerID, note); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO certification_reviews(submission_id,profile_id,reviewer_id,decision,note) VALUES($1,$2,$3,$4,$5)`, *submissionID, profileID, reviewerID, decision, note); err != nil {
			return err
		}
	}
	if decision == "approved" {
		if err = s.applyCertifiedNameTx(ctx, tx, reviewerID, userID, profileID, submissionID, sealed, aad); err != nil {
			return err
		}
	}
	if err = Audit(ctx, tx, reviewerID, "certification.reviewed", "certification", id, map[string]any{"user_id": userID, "decision": decision}); err != nil {
		return err
	}
	return nil
}

// applyCertifiedNameTx implements legacy `certifi_realname`/auto_update_name:
// an approved review writes the certified customer name to display_name
// (legacy clients.username -> users.display_name). It runs inside the review
// transaction so the decision and the name change commit atomically.
func (s *Store) applyCertifiedNameTx(ctx context.Context, tx pgx.Tx, reviewerID, userID, profileID int64, submissionID *int64, sealed []byte, aad *string) error {
	var auto bool
	if err := tx.QueryRow(ctx, "SELECT auto_update_name FROM certification_settings WHERE id=1").Scan(&auto); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if !auto || submissionID == nil || len(sealed) == 0 || aad == nil {
		return nil
	}
	payload, err := openCertification(s.ProvisionEncryptionKey, *aad, sealed)
	if err != nil {
		// The approval itself must not depend on this optional convenience.
		// Record the skipped writeback so operators can see it happened.
		return Audit(ctx, tx, reviewerID, "certification.name_update_skipped", "certification", profileID, map[string]any{"user_id": userID, "reason": "payload_unavailable"})
	}
	name, _ := payload["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return Audit(ctx, tx, reviewerID, "certification.name_update_skipped", "certification", profileID, map[string]any{"user_id": userID, "reason": "invalid_display_name"})
	}
	var current string
	if err = tx.QueryRow(ctx, `SELECT display_name FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&current); err != nil {
		return err
	}
	if current == name {
		return nil
	}
	// Match customer management's users -> customer_profiles lock order and
	// preserve contact-derived defaults for accounts without a profile yet.
	if _, err = tx.Exec(ctx, "INSERT INTO customer_profiles(customer_id,company_name,phone) SELECT $1,COALESCE((SELECT company_name FROM customer_contacts WHERE customer_id=$1 AND company_name<>'' ORDER BY id LIMIT 1),''),COALESCE((SELECT phone_number FROM customer_contacts WHERE customer_id=$1 AND phone_number<>'' ORDER BY id LIMIT 1),'') ON CONFLICT DO NOTHING", userID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE customer_profiles SET version=version+1,updated_at=now() WHERE customer_id=$1", userID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE users SET display_name=$2,updated_at=now() WHERE id=$1", userID, name); err != nil {
		return err
	}
	return Audit(ctx, tx, reviewerID, "certification.name_updated", "certification", profileID, map[string]any{"user_id": userID, "field": "display_name", "before": current, "display_name": name})
}
