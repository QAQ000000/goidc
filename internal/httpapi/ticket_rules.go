package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
)

func (s *Server) ticketStatuses(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := serviceActivityPagination(r)
	if !ok {
		writeError(w, 400, "invalid_pagination", "分页参数无效")
		return
	}
	rows, e := s.db.Pool.Query(r.Context(), `SELECT id,slug,title,sort_order,builtin,active,version FROM ticket_statuses ORDER BY sort_order,id LIMIT $1 OFFSET $2`, limit, offset)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	out, e := pgx.CollectRows(rows, pgx.RowToMap)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	if out == nil {
		out = []map[string]any{}
	}
	var total int64
	if e = s.db.Pool.QueryRow(r.Context(), `SELECT count(*) FROM ticket_statuses`).Scan(&total); e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, map[string]any{"items": out, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) saveTicketStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Slug, Title     string
		SortOrder       int `json:"sort_order"`
		Active          *bool
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, 400, "idempotency_key_required", "请提供幂等键")
		return
	}
	in.Slug = strings.TrimSpace(in.Slug)
	in.Title = strings.TrimSpace(in.Title)
	if in.Slug == "" || in.Title == "" {
		writeError(w, 400, "invalid_ticket_status", "状态资料无效")
		return
	}
	tx, e := s.db.Pool.Begin(r.Context())
	if e != nil {
		s.fail(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	var id int64
	var version int64
	var builtin bool
	e = tx.QueryRow(r.Context(), `SELECT id,version,builtin FROM ticket_statuses WHERE slug=$1 FOR UPDATE`, in.Slug).Scan(&id, &version, &builtin)
	if e == pgx.ErrNoRows {
		e = tx.QueryRow(r.Context(), `INSERT INTO ticket_statuses(slug,title,sort_order,active,idempotency_key) VALUES($1,$2,$3,coalesce($4,true),$5) ON CONFLICT(idempotency_key) DO UPDATE SET idempotency_key=EXCLUDED.idempotency_key RETURNING id,version,builtin`, in.Slug, in.Title, in.SortOrder, in.Active, key).Scan(&id, &version, &builtin)
	} else if e == nil {
		if in.ExpectedVersion < 1 || version != in.ExpectedVersion {
			writeError(w, 409, "version_conflict", "状态已变更，请重新加载")
			return
		}
		if builtin && in.Title == "" {
			writeError(w, 409, "builtin_status", "默认状态不可重命名")
			return
		}
		_, e = tx.Exec(r.Context(), `UPDATE ticket_statuses SET title=$2,sort_order=$3,active=coalesce($4,active),version=version+1,updated_at=now() WHERE id=$1`, id, in.Title, in.SortOrder, in.Active)
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	if e = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.status.updated", "ticket_status", id, in); e != nil {
		s.fail(w, r, e)
		return
	}
	if e = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.status.updated", "ticket_status", id, in); e != nil {
		s.fail(w, r, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, map[string]any{"id": id, "version": version + 1})
}
func (s *Server) ticketRules(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := serviceActivityPagination(r)
	if !ok {
		writeError(w, 400, "invalid_pagination", "分页参数无效")
		return
	}
	rows, e := s.db.Pool.Query(r.Context(), `SELECT id,department_id,product_id,blocked_keywords,auto_reply,active,version FROM ticket_delivery_rules ORDER BY id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	out, e := pgx.CollectRows(rows, pgx.RowToMap)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	if out == nil {
		out = []map[string]any{}
	}
	var total int64
	if e = s.db.Pool.QueryRow(r.Context(), `SELECT count(*) FROM ticket_delivery_rules`).Scan(&total); e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, map[string]any{"items": out, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) saveTicketRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DepartmentID    *int64   `json:"department_id"`
		ProductID       *int64   `json:"product_id"`
		BlockedKeywords []string `json:"blocked_keywords"`
		AutoReply       string   `json:"auto_reply"`
		Active          *bool    `json:"active"`
	}
	if !decode(w, r, &in) {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, 400, "idempotency_key_required", "请提供幂等键")
		return
	}
	if len(in.AutoReply) > 10000 || len(in.BlockedKeywords) > 100 {
		writeError(w, 400, "invalid_ticket_rule", "规则内容无效")
		return
	}
	tx, e := s.db.Pool.Begin(r.Context())
	if e != nil {
		s.fail(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	var id, version int64
	if in.DepartmentID != nil {
		var ok bool
		if e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM ticket_departments WHERE id=$1)`, *in.DepartmentID).Scan(&ok); e != nil || !ok {
			writeError(w, 400, "invalid_ticket_department", "部门不存在")
			return
		}
	}
	if in.ProductID != nil {
		var ok bool
		if e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM products WHERE id=$1)`, *in.ProductID).Scan(&ok); e != nil || !ok {
			writeError(w, 400, "invalid_product", "产品不存在")
			return
		}
	}
	e = tx.QueryRow(r.Context(), `INSERT INTO ticket_delivery_rules(department_id,product_id,blocked_keywords,auto_reply,active,idempotency_key) VALUES($1,$2,$3,$4,coalesce($5,true),$6) ON CONFLICT(idempotency_key) DO UPDATE SET idempotency_key=EXCLUDED.idempotency_key RETURNING id,version`, in.DepartmentID, in.ProductID, in.BlockedKeywords, in.AutoReply, in.Active, key).Scan(&id, &version)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	if e = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.rule.created", "ticket_delivery_rule", id, in); e != nil {
		s.fail(w, r, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 201, map[string]any{"id": id, "version": version})
}

func (s *Server) updateTicketStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Title           string `json:"title"`
		SortOrder       int    `json:"sort_order"`
		Active          *bool  `json:"active"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if !decode(w, r, &in) || in.ExpectedVersion < 1 || strings.TrimSpace(in.Title) == "" {
		writeError(w, 400, "invalid_ticket_status", "状态资料或版本无效")
		return
	}
	tx, e := s.db.Pool.Begin(r.Context())
	if e != nil {
		s.fail(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	var v int64
	var builtin bool
	if e = tx.QueryRow(r.Context(), `SELECT version,builtin FROM ticket_statuses WHERE id=$1 FOR UPDATE`, id).Scan(&v, &builtin); e != nil {
		s.fail(w, r, e)
		return
	}
	if v != in.ExpectedVersion {
		writeError(w, 409, "version_conflict", "状态已变更，请重新加载")
		return
	}
	_, e = tx.Exec(r.Context(), `UPDATE ticket_statuses SET title=$2,sort_order=$3,active=coalesce($4,active),version=version+1,updated_at=now() WHERE id=$1`, id, in.Title, in.SortOrder, in.Active)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	if e = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.status.updated", "ticket_status", id, in); e != nil {
		s.fail(w, r, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, map[string]any{"id": id, "version": v + 1, "builtin": builtin})
}
func (s *Server) deleteTicketStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) || in.ExpectedVersion < 1 {
		writeError(w, 400, "invalid_ticket_status", "版本无效")
		return
	}
	var builtin bool
	var v int64
	if e := s.db.Pool.QueryRow(r.Context(), `SELECT builtin,version FROM ticket_statuses WHERE id=$1`, id).Scan(&builtin, &v); e != nil {
		s.fail(w, r, e)
		return
	}
	if builtin {
		writeError(w, 409, "builtin_status", "默认状态不可删除")
		return
	}
	if v != in.ExpectedVersion {
		writeError(w, 409, "version_conflict", "状态已变更，请重新加载")
		return
	}
	if _, e := s.db.Pool.Exec(r.Context(), `DELETE FROM ticket_statuses WHERE id=$1`, id); e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
func (s *Server) updateTicketRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		DepartmentID    *int64   `json:"department_id"`
		ProductID       *int64   `json:"product_id"`
		BlockedKeywords []string `json:"blocked_keywords"`
		AutoReply       string   `json:"auto_reply"`
		Active          *bool    `json:"active"`
		ExpectedVersion int64    `json:"expected_version"`
	}
	if !decode(w, r, &in) || in.ExpectedVersion < 1 || len(in.AutoReply) > 10000 || len(in.BlockedKeywords) > 100 {
		writeError(w, 400, "invalid_ticket_rule", "规则资料或版本无效")
		return
	}
	var v int64
	if e := s.db.Pool.QueryRow(r.Context(), `SELECT version FROM ticket_delivery_rules WHERE id=$1`, id).Scan(&v); e != nil {
		s.fail(w, r, e)
		return
	}
	if v != in.ExpectedVersion {
		writeError(w, 409, "version_conflict", "规则已变更，请重新加载")
		return
	}
	_, e := s.db.Pool.Exec(r.Context(), `UPDATE ticket_delivery_rules SET department_id=$2,product_id=$3,blocked_keywords=$4,auto_reply=$5,active=coalesce($6,active),version=version+1,updated_at=now() WHERE id=$1`, id, in.DepartmentID, in.ProductID, in.BlockedKeywords, in.AutoReply, in.Active)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, map[string]any{"id": id, "version": v + 1})
}
func (s *Server) deleteTicketRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) || in.ExpectedVersion < 1 {
		writeError(w, 400, "invalid_ticket_rule", "版本无效")
		return
	}
	var v int64
	if e := s.db.Pool.QueryRow(r.Context(), `SELECT version FROM ticket_delivery_rules WHERE id=$1`, id).Scan(&v); e != nil {
		s.fail(w, r, e)
		return
	}
	if v != in.ExpectedVersion {
		writeError(w, 409, "version_conflict", "规则已变更，请重新加载")
		return
	}
	if _, e := s.db.Pool.Exec(r.Context(), `DELETE FROM ticket_delivery_rules WHERE id=$1`, id); e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
