package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *Server) certification(w http.ResponseWriter, r *http.Request) {
	out, err := s.db.Certification(r.Context(), identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) submitCertification(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Type = strings.TrimSpace(strings.ToLower(in.Type))
	out, err := s.db.SubmitCertification(r.Context(), identityFrom(r).ID, in.Type, in.Payload)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusAccepted, out)
}

func (s *Server) convertCertificationToCompany(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Payload         map[string]any `json:"payload"`
		ExpectedVersion int64          `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "invalid_certification_conversion", "请提供有效认证版本")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "请提供长度为 8 到 128 的幂等键")
		return
	}
	hashBytes, _ := json.Marshal(struct {
		Payload         map[string]any
		ExpectedVersion int64
	}{in.Payload, in.ExpectedVersion})
	hash := sha256.Sum256(hashBytes)
	actor := identityFrom(r).ID
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	operation := "certification.convert_company"
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hex.EncodeToString(hash[:])); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	out, err := s.db.ConvertCertificationToCompanyTx(r.Context(), tx, actor, in.Payload, in.ExpectedVersion)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, operation, key, hex.EncodeToString(hash[:]), out); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusAccepted, out)
}

func (s *Server) adminCertifications(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && status != "unverified" && status != "pending" && status != "approved" && status != "rejected" {
		writeError(w, 400, "invalid_certification_status", "认证状态无效")
		return
	}
	limit, offset := pagination(r)
	out, err := s.db.AdminCertifications(r.Context(), status, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if out == nil {
		out = []map[string]any{}
	}
	write(w, 200, out)
}

func (s *Server) adminCertificationDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var allowed bool
	if err := s.db.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM certification_profiles p WHERE p.id=$1 AND admin_customer_allowed($2,p.user_id))`, id, identityFrom(r).ID).Scan(&allowed); err != nil {
		s.fail(w, r, err)
		return
	}
	if !allowed {
		writeError(w, http.StatusNotFound, "not_found", "认证记录不存在")
		return
	}
	out, err := s.db.AdminCertificationDetail(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}

func (s *Server) reviewCertification(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Decision        string `json:"decision"`
		Note            string `json:"note"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Decision = strings.TrimSpace(strings.ToLower(in.Decision))
	if len(in.Note) > 2000 {
		writeError(w, 400, "invalid_certification_review", "审核备注过长")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "请提供长度为 8 到 128 的幂等键")
		return
	}
	hashBytes, _ := json.Marshal(struct {
		ID              int64
		Decision        string
		Note            string
		ExpectedVersion int64
	}{id, in.Decision, in.Note, in.ExpectedVersion})
	hash := sha256.Sum256(hashBytes)
	operation := "certification.review:" + strconv.FormatInt(id, 10)
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, identityFrom(r).ID, operation, key, hex.EncodeToString(hash[:])); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	if err := s.db.ReviewCertificationTx(r.Context(), tx, identityFrom(r).ID, id, in.Decision, in.Note, in.ExpectedVersion); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "status": in.Decision}
	if err = saveSettingOperationTx(r, tx, identityFrom(r).ID, operation, key, hex.EncodeToString(hash[:]), result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}

func (s *Server) adminCertificationHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `SELECT r.id,r.submission_id,r.reviewer_id,coalesce(u.display_name,u.username) AS reviewer_name,r.decision,r.note,r.created_at
FROM certification_reviews r JOIN users u ON u.id=r.reviewer_id JOIN certification_profiles p ON p.id=r.profile_id
WHERE r.profile_id=$1 AND admin_customer_allowed($2,p.user_id) ORDER BY r.id DESC`, id, identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	write(w, http.StatusOK, items)
}

func certificationID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}
