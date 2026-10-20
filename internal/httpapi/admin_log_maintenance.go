package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type logCleanupInput struct {
	LogType      string `json:"log_type"`
	Before       string `json:"before"`
	Acknowledged bool   `json:"acknowledged"`
}

func (s *Server) adminLoginLog(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	page, err := s.db.AccountAdministratorLoginLog(r.Context(), r.URL.Query(), limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, page)
}

func itoa(value int) string {
	// Avoid fmt in the query builders and keep placeholder generation explicit.
	return strconv.Itoa(value)
}

func (s *Server) cleanupLogs(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "请提供有效的幂等键")
		return
	}
	var input logCleanupInput
	if !decode(w, r, &input) {
		return
	}
	input.LogType = strings.TrimSpace(input.LogType)
	if input.LogType != "audit" && input.LogType != "email" && input.LogType != "sms" && input.LogType != "scheduler" {
		writeError(w, http.StatusBadRequest, "invalid_log_type", "日志类型无效")
		return
	}
	if !input.Acknowledged {
		writeError(w, http.StatusBadRequest, "confirmation_required", "请确认清理不可恢复的历史日志")
		return
	}
	before, err := time.Parse(time.RFC3339, strings.TrimSpace(input.Before))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_log_date", "截止时间必须使用 RFC3339 格式")
		return
	}
	if !before.Before(time.Now().Add(-24 * time.Hour)) {
		writeError(w, http.StatusBadRequest, "log_date_too_recent", "只能清理至少保留 24 小时以前的日志")
		return
	}
	payload, _ := json.Marshal(input)
	hashBytes := sha256.Sum256(payload)
	requestHash := hex.EncodeToString(hashBytes[:])
	actor := identityFrom(r).ID
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var replay map[string]any
		if replayed, err := store.ReplayOperation(r.Context(), tx, actor, "log-cleanup", key, requestHash, &replay); err != nil {
			return txResponse{}, err
		} else if replayed {
			replay["replayed"] = true

			return txResponse{http.StatusOK, replay}, nil
		}
		var deleted int64
		switch input.LogType {
		case "audit":
			err = tx.QueryRow(r.Context(), `WITH deleted AS (DELETE FROM audit_events WHERE created_at < $1 AND action <> 'log.cleanup.completed' RETURNING 1) SELECT count(*) FROM deleted`, before).Scan(&deleted)
		case "email":
			err = tx.QueryRow(r.Context(), `WITH deleted AS (DELETE FROM email_outbox WHERE created_at < $1 AND status IN ('sent','expired','dead_letter') RETURNING 1) SELECT count(*) FROM deleted`, before).Scan(&deleted)
		case "sms":
			err = tx.QueryRow(r.Context(), `WITH deleted AS (DELETE FROM sms_outbox WHERE created_at < $1 AND status IN ('sent','expired','dead_letter') RETURNING 1) SELECT count(*) FROM deleted`, before).Scan(&deleted)
		case "scheduler":
			err = tx.QueryRow(r.Context(), `WITH deleted AS (DELETE FROM scheduler_runs WHERE started_at < $1 AND status <> 'running' RETURNING 1) SELECT count(*) FROM deleted`, before).Scan(&deleted)
		}
		if err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"log_type": input.LogType, "before": before, "deleted": deleted, "replayed": false}
		if err = store.SaveOperation(r.Context(), tx, actor, "log-cleanup", key, requestHash, result); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, actor, "log.cleanup.completed", "log_cleanup", 0, map[string]any{"log_type": input.LogType, "before": before, "deleted": deleted, "idempotency_key": key}); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, result}, nil
	})
}
