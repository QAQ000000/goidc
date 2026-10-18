package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
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
	if err := s.db.CertificationGate(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	key, ok := mutationKey(w, r, "", false)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(in)
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		ctx := requestFileContext(r)
		var replay map[string]any
		var replayed bool
		var err error
		if key != "" {
			replayed, err = store.ReplayOperation(ctx, tx, actor, "certification:submit", key, hash, &replay)
			if err != nil {
				return txResponse{}, err
			}
		}
		// A same-key request may have waited for another transaction. Recheck
		// its credential and the master switch before returning a receipt.
		if err = store.LockFileCredential(ctx, tx, actor); err != nil {
			return txResponse{}, err
		}
		var enabled bool
		if err = tx.QueryRow(ctx, "SELECT enabled FROM certification_settings WHERE id=1 FOR SHARE").Scan(&enabled); err != nil && err != pgx.ErrNoRows {
			return txResponse{}, err
		}
		if !enabled {
			return txResponse{}, &store.Conflict{Code: "certification_disabled", Message: "实名认证已关闭"}
		}
		if replayed {
			replay["replayed"] = true
			return txResponse{http.StatusOK, replay}, nil
		}
		out, err := s.db.SubmitCertificationTx(ctx, tx, actor, in.Type, in.Payload)
		if err != nil {
			return txResponse{}, err
		}
		if key != "" {
			if err = store.SaveOperation(ctx, tx, actor, "certification:submit", key, hash, out); err != nil {
				return txResponse{}, err
			}
		}
		return txResponse{http.StatusAccepted, out}, nil
	})
}

func (s *Server) convertCertificationToCompany(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Payload         map[string]any `json:"payload"`
		ExpectedVersion int64          `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	// Legacy checks certifi_open before anything else, so a closed switch is
	// reported ahead of payload validation and idempotent replays.
	if err := s.db.CertificationGate(r.Context()); err != nil {
		s.fail(w, r, err)
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
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		ctx := requestFileContext(r)
		operation := "certification.convert_company"
		replay, replayed, err := loadSettingReplayTx(r, tx, actor, operation, key, hex.EncodeToString(hash[:]))
		if err != nil {
			return txResponse{}, err
		}
		// Receipt serialization can outlive the request's initial authorization
		// and master-switch read, for both new conversions and replays.
		if err = store.LockFileCredential(ctx, tx, actor); err != nil {
			return txResponse{}, err
		}
		var enabled bool
		if err = tx.QueryRow(ctx, "SELECT enabled FROM certification_settings WHERE id=1 FOR SHARE").Scan(&enabled); err != nil && err != pgx.ErrNoRows {
			return txResponse{}, err
		}
		if !enabled {
			return txResponse{}, &store.Conflict{Code: "certification_disabled", Message: "实名认证已关闭"}
		}
		if replayed {
			return txResponse{http.StatusOK, replay}, nil
		}
		out, err := s.db.ConvertCertificationToCompanyTx(ctx, tx, actor, in.Payload, in.ExpectedVersion)
		if err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, operation, key, hex.EncodeToString(hash[:]), out); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusAccepted, out}, nil
	})
}

func (s *Server) adminCertifications(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && status != "unverified" && status != "pending" && status != "approved" && status != "rejected" {
		writeError(w, 400, "invalid_certification_status", "认证状态无效")
		return
	}
	limit, offset := pagination(r)
	out, err := s.db.AdminCertifications(r.Context(), identityFrom(r).ID, status, limit, offset)
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
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		ctx := requestFileContext(r)
		if err = s.db.LockCertificationReview(ctx, tx, identityFrom(r).ID, id); err != nil {
			return txResponse{}, err
		}
		if replay, replayed, e := loadSettingReplayTx(r, tx, identityFrom(r).ID, operation, key, hex.EncodeToString(hash[:])); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{http.StatusOK, replay}, nil
		}
		if err := s.db.ReviewCertificationTx(ctx, tx, identityFrom(r).ID, id, in.Decision, in.Note, in.ExpectedVersion); err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "status": in.Decision}
		if err = saveSettingOperationTx(r, tx, identityFrom(r).ID, operation, key, hex.EncodeToString(hash[:]), result); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, result}, nil
	})
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
