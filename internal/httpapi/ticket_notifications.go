package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

// Keys are optional for older clients; only keyed requests promise response-loss replay.
func ticketPublicMutationKey(w http.ResponseWriter, r *http.Request, body string) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	body = strings.TrimSpace(body)
	if header != "" && body != "" && header != body {
		writeError(w, 400, "invalid_idempotency_key", "请求键不一致")
		return "", false
	}
	if header != "" {
		body = header
	}
	if body != "" && (len(body) < 8 || len(body) > 128) {
		writeError(w, 400, "invalid_idempotency_key", "请求键长度必须为 8 到 128")
		return "", false
	}
	return body, true
}

// true means the request has been answered (including database errors).
func (s *Server) ticketReplay(w http.ResponseWriter, r *http.Request, tx pgx.Tx, key, operation, hash string, ticketID int64) bool {
	if key == "" {
		return false
	}
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("ticket-mutation:%d:%s:%s", identityFrom(r).ID, operation, key)); err != nil {
		s.fail(w, r, err)
		return true
	}
	var saved string
	var raw []byte
	err := tx.QueryRow(r.Context(), `SELECT request_hash,result FROM ticket_mutation_receipts WHERE actor_id=$1 AND operation=$2 AND idempotency_key=$3`, identityFrom(r).ID, operation, key).Scan(&saved, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		s.fail(w, r, err)
		return true
	}
	if saved != hash {
		writeError(w, 409, "idempotency_conflict", "相同请求键已用于不同内容")
		return true
	}
	// Creation replay must also enforce today's owner/department visibility.
	if ticketID == 0 {
		var result struct {
			ID int64 `json:"id"`
		}
		if err = json.Unmarshal(raw, &result); err != nil {
			s.fail(w, r, err)
			return true
		}
		var allowed bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tickets t LEFT JOIN ticket_departments d ON d.id=t.department_id WHERE t.id=$1 AND t.merged_ticket_id IS NULL AND t.user_id=$2 AND coalesce(d.customer_visible,true))`, result.ID, identityFrom(r).ID).Scan(&allowed)
		if err != nil {
			s.fail(w, r, err)
			return true
		}
		if !allowed {
			writeError(w, 404, "not_found", "工单不存在或无权查看")
			return true
		}
	}
	code := 200
	if operation == "create" {
		code = 201
	}
	write(w, code, json.RawMessage(raw))
	return true
}
func saveTicketReceipt(r *http.Request, tx pgx.Tx, key, operation, hash string, id int64, result any) error {
	if key == "" {
		return nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO ticket_mutation_receipts(actor_id,operation,idempotency_key,ticket_id,request_hash,result) VALUES($1,$2,$3,$4,$5,$6)`, identityFrom(r).ID, operation, key, id, hash, raw)
	return err
}

func (s *Server) ticketNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `INSERT INTO user_ticket_notification_preferences(user_id) VALUES($1) ON CONFLICT DO NOTHING`, identityFrom(r).ID); err != nil {
		s.fail(w, r, err)
		return
	}
	var email, sms bool
	var version int64
	if err = tx.QueryRow(r.Context(), `SELECT email_enabled,sms_enabled,version FROM user_ticket_notification_preferences WHERE user_id=$1 FOR UPDATE`, identityFrom(r).ID).Scan(&email, &sms, &version); err != nil {
		s.fail(w, r, err)
		return
	}
	if r.Method == http.MethodPatch {
		var in struct {
			Email   *bool `json:"email_enabled"`
			SMS     *bool `json:"sms_enabled"`
			Version int64 `json:"expected_version"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Email == nil || in.SMS == nil || in.Version < 1 {
			writeError(w, 400, "invalid_preferences", "请提供通知偏好和版本")
			return
		}
		if in.Version != version {
			writeError(w, 409, "stale_preferences", "通知偏好已更新，请刷新后重试")
			return
		}
		email, sms = *in.Email, *in.SMS
		version++
		if _, err = tx.Exec(r.Context(), `UPDATE user_ticket_notification_preferences SET email_enabled=$2,sms_enabled=$3,version=$4,updated_at=now() WHERE user_id=$1`, identityFrom(r).ID, email, sms, version); err != nil {
			s.fail(w, r, err)
			return
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.notification_preferences.updated", "user", identityFrom(r).ID, map[string]any{"email_enabled": email, "sms_enabled": sms, "version": version}); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"email_enabled": email, "sms_enabled": sms, "version": version})
}
func (s *Server) ticketNotifications(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	tx, err := s.db.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var staff bool
	err = tx.QueryRow(r.Context(), `SELECT ($2::boolean AND admin_ticket_department_allowed($3,t.department_id)) FROM tickets t LEFT JOIN ticket_departments d ON d.id=t.department_id WHERE t.id=$1 AND (($2::boolean AND admin_ticket_department_allowed($3,t.department_id)) OR (t.user_id=$3 AND coalesce(d.customer_visible,true)))`, id, isAdmin(r), identityFrom(r).ID).Scan(&staff)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	where := ` FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.ticket_id=$1 AND ($2::boolean OR (d.recipient_kind='user' AND d.user_id=$3))`
	var total int64
	if err = tx.QueryRow(r.Context(), `SELECT count(*)`+where, id, staff, identityFrom(r).ID).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT d.id,e.event_key,e.kind AS event_kind,e.reply_id,d.channel,d.outcome,d.reason,d.created_at,CASE WHEN $2 THEN d.recipient_kind ELSE 'user' END AS recipient_kind,CASE WHEN $2 THEN d.address ELSE NULL END AS address,d.email_outbox_id,d.sms_outbox_id`+where+` ORDER BY d.id DESC LIMIT $4 OFFSET $5`, id, staff, identityFrom(r).ID, limit, offset)
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
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}
