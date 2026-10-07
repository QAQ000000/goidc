package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func validNotificationKind(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func (s *Server) notificationTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Pool.Query(r.Context(), `SELECT id,kind,name,subject,body,active,updated_by,created_at,updated_at
FROM notification_templates ORDER BY id`)
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

func (s *Server) createNotificationTemplate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
		Active  *bool  `json:"active"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Kind = strings.TrimSpace(input.Kind)
	input.Name, input.Subject, input.Body = strings.TrimSpace(input.Name), strings.TrimSpace(input.Subject), strings.TrimSpace(input.Body)
	if !validNotificationKind(input.Kind) || input.Name == "" || len(input.Name) > 120 || input.Subject == "" || len(input.Subject) > 240 || input.Body == "" || len(input.Body) > 100000 {
		writeError(w, http.StatusBadRequest, "invalid_notification_template", "通知模板字段无效")
		return
	}
	active := true
	if input.Active != nil {
		active = *input.Active
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var id int64
	err = tx.QueryRow(r.Context(), `INSERT INTO notification_templates(kind,name,subject,body,active,updated_by)
VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, input.Kind, input.Name, input.Subject, input.Body, active, identityFrom(r).ID).Scan(&id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "notification_template.created", "notification_template", id, map[string]any{"kind": input.Kind}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusCreated, map[string]any{"id": id, "kind": input.Kind})
}

func (s *Server) updateNotificationTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Name    *string `json:"name"`
		Subject *string `json:"subject"`
		Body    *string `json:"body"`
		Active  *bool   `json:"active"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Name == nil && input.Subject == nil && input.Body == nil && input.Active == nil {
		writeError(w, http.StatusBadRequest, "invalid_notification_template", "至少提供一个要修改的字段")
		return
	}
	if input.Name != nil {
		*input.Name = strings.TrimSpace(*input.Name)
		if *input.Name == "" || len(*input.Name) > 120 {
			writeError(w, http.StatusBadRequest, "invalid_notification_template", "模板名称无效")
			return
		}
	}
	if input.Subject != nil {
		*input.Subject = strings.TrimSpace(*input.Subject)
		if *input.Subject == "" || len(*input.Subject) > 240 {
			writeError(w, http.StatusBadRequest, "invalid_notification_template", "模板主题无效")
			return
		}
	}
	if input.Body != nil {
		*input.Body = strings.TrimSpace(*input.Body)
		if *input.Body == "" || len(*input.Body) > 100000 {
			writeError(w, http.StatusBadRequest, "invalid_notification_template", "模板正文无效")
			return
		}
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var kind string
	if err = tx.QueryRow(r.Context(), "SELECT kind FROM notification_templates WHERE id=$1 FOR UPDATE", id).Scan(&kind); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE notification_templates SET name=COALESCE($2,name),subject=COALESCE($3,subject),body=COALESCE($4,body),active=COALESCE($5,active),updated_by=$6,updated_at=now() WHERE id=$1`, id, input.Name, input.Subject, input.Body, input.Active, identityFrom(r).ID); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "notification_template.updated", "notification_template", id, map[string]any{"kind": kind, "active": input.Active}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"id": id, "kind": kind})
}

func (s *Server) emailOutbox(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	args := []any{}
	where := "TRUE"
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		if status != "pending" && status != "sending" && status != "sent" && status != "expired" {
			writeError(w, http.StatusBadRequest, "invalid_email_status", "邮件状态无效")
			return
		}
		args = append(args, status)
		where += " AND status=$1"
	}
	args = append(args, limit, offset)
	query := "SELECT id,recipient,subject,kind,status,attempts,last_error,available_at,sent_at,created_at FROM email_outbox WHERE " + where + " ORDER BY id DESC LIMIT $" + strconv.Itoa(len(args)-1) + " OFFSET $" + strconv.Itoa(len(args))
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
	write(w, http.StatusOK, items)
}

func (s *Server) retryEmail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var status string
	if err = tx.QueryRow(r.Context(), "SELECT status FROM email_outbox WHERE id=$1 FOR UPDATE", id).Scan(&status); err != nil {
		s.fail(w, r, err)
		return
	}
	if status == "sent" {
		writeError(w, http.StatusConflict, "email_already_sent", "邮件已经发送")
		return
	}
	var active bool
	if err = tx.QueryRow(r.Context(), `SELECT (expires_at IS NULL OR expires_at>now()) AND (verification_id IS NULL OR EXISTS(SELECT 1 FROM verification_challenges v WHERE v.id=email_outbox.verification_id AND v.consumed_at IS NULL AND v.expires_at>now())) FROM email_outbox WHERE id=$1`, id).Scan(&active); err != nil {
		s.fail(w, r, err)
		return
	}
	if !active || status == "expired" {
		writeError(w, 409, "email_expired", "验证码邮件已过期，请重新申请验证码")
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE email_outbox SET status='pending',locked_at=NULL,available_at=now(),last_error='' WHERE id=$1", id); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "email_outbox.retried", "email_outbox", id, map[string]any{}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"id": id, "status": "pending"})
}
