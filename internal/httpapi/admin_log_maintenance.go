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
	f := adminLogFilters{where: []string{"a.action='auth.login_succeeded'", "u.role IN ('admin','super_admin')"}}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		f.add("%"+q+"%", "(u.username ILIKE $%d OR u.email ILIKE $%d OR u.display_name ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = "(u.username ILIKE $" + itoa(idx) + " OR u.email ILIKE $" + itoa(idx) + " OR u.display_name ILIKE $" + itoa(idx) + ")"
	}
	if username := strings.TrimSpace(r.URL.Query().Get("username")); username != "" {
		f.add("%"+username+"%", "(u.username ILIKE $%d OR u.display_name ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = "(u.username ILIKE $" + itoa(idx) + " OR u.display_name ILIKE $" + itoa(idx) + ")"
	}
	if ip := strings.TrimSpace(r.URL.Query().Get("ip")); ip != "" {
		f.add("%"+ip+"%", "coalesce(a.payload->>'remote_addr','') ILIKE $%d")
	}
	if err := addLogDateFilters(r, &f, "a.created_at"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_log_date", err.Error())
		return
	}
	where := strings.Join(f.where, " AND ")
	base := ` FROM audit_events a JOIN users u ON u.id=a.actor_id
LEFT JOIN LATERAL (SELECT created_at FROM audit_events l WHERE l.action='auth.logout' AND l.actor_id=a.actor_id AND l.created_at>=a.created_at ORDER BY l.created_at LIMIT 1) logout ON true WHERE ` + where
	var total int
	if err := s.db.Pool.QueryRow(r.Context(), "SELECT count(*)"+base, f.args...).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	args := append(append([]any{}, f.args...), limit, offset)
	query := `SELECT a.id,a.created_at AS login_time,a.created_at AS last_access,logout.created_at AS logout_time,
u.username,u.display_name,coalesce(a.payload->>'remote_addr','') AS ip` + base +
		" ORDER BY a.id DESC LIMIT $" + itoa(len(args)-1) + " OFFSET $" + itoa(len(args))
	rows, err := s.db.Pool.Query(r.Context(), query, args...)
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
	write(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
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
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var savedHash string
	var savedResult []byte
	err = tx.QueryRow(r.Context(), `SELECT request_hash,result FROM log_cleanup_operations WHERE actor_id=$1 AND idempotency_key=$2 FOR UPDATE`, actor, key).Scan(&savedHash, &savedResult)
	if err == nil {
		if savedHash != requestHash {
			writeError(w, http.StatusConflict, "idempotency_conflict", "幂等键已用于其他清理请求")
			return
		}
		var result map[string]any
		if json.Unmarshal(savedResult, &result) != nil {
			writeError(w, http.StatusInternalServerError, "invalid_operation_result", "清理结果无效")
			return
		}
		result["replayed"] = true
		if err = tx.Commit(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, http.StatusOK, result)
		return
	}
	if err != pgx.ErrNoRows {
		s.fail(w, r, err)
		return
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
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"log_type": input.LogType, "before": before, "deleted": deleted, "replayed": false}
	encoded, _ := json.Marshal(result)
	if _, err = tx.Exec(r.Context(), `INSERT INTO log_cleanup_operations(actor_id,idempotency_key,request_hash,result) VALUES($1,$2,$3,$4)`, actor, key, requestHash, encoded); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, actor, "log.cleanup.completed", "log_cleanup", 0, map[string]any{"log_type": input.LogType, "before": before, "deleted": deleted, "idempotency_key": key}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}
