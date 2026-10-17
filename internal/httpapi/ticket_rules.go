package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func ticketMutationHash(action string, id int64, input any) (string, error) {
	raw, err := json.Marshal(struct {
		Action string `json:"action"`
		ID     int64  `json:"id"`
		Input  any    `json:"input"`
	}{action, id, input})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func loadTicketMutation(ctx context.Context, tx pgx.Tx, actor int64, key, hash string) (map[string]any, bool, error) {
	var out map[string]any
	replayed, err := store.ReplayOperation(ctx, tx, actor, "ticket", key, hash, &out)
	if err == nil && replayed {
		out["replayed"] = true
	}
	return out, replayed, err
}

func saveTicketMutation(ctx context.Context, tx pgx.Tx, actor int64, key, hash string, result map[string]any) error {
	return store.SaveOperation(ctx, tx, actor, "ticket", key, hash, result)
}

func ticketMutationKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "幂等键长度必须在 8 到 128 之间")
		return "", false
	}
	return key, true
}

func (s *Server) ticketStatuses(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := serviceActivityPagination(r)
	if !ok {
		writeError(w, 400, "invalid_pagination", "分页参数无效")
		return
	}
	s.writeListPage(w, r, `SELECT count(*) FROM ticket_statuses`,
		`SELECT id,slug,title,sort_order,builtin,active,version FROM ticket_statuses ORDER BY sort_order,id`, nil, limit, offset)
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
	key, ok := ticketMutationKey(w, r)
	if !ok {
		return
	}
	in.Slug = strings.TrimSpace(in.Slug)
	in.Title = strings.TrimSpace(in.Title)
	if in.Slug == "" || in.Title == "" {
		writeError(w, 400, "invalid_ticket_status", "状态资料无效")
		return
	}
	hash, e := ticketMutationHash("status.save", 0, in)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {

		if result, replay, err := loadTicketMutation(r.Context(), tx, identityFrom(r).ID, key, hash); err != nil {
			return txResponse{}, err
		} else if replay {

			return txResponse{http.StatusOK, result}, nil
		}
		var id int64
		var version int64
		var builtin bool
		var currentTitle string
		created := false
		e = tx.QueryRow(r.Context(), `SELECT id,version,builtin,title FROM ticket_statuses WHERE slug=$1 FOR UPDATE`, in.Slug).Scan(&id, &version, &builtin, &currentTitle)
		if e == pgx.ErrNoRows {
			e = tx.QueryRow(r.Context(), `INSERT INTO ticket_statuses(slug,title,sort_order,active) VALUES($1,$2,$3,coalesce($4,true)) RETURNING id,version,builtin`, in.Slug, in.Title, in.SortOrder, in.Active).Scan(&id, &version, &builtin)
			created = e == nil
		} else if e == nil {
			if in.ExpectedVersion < 1 || version != in.ExpectedVersion {
				return txResponse{}, &txHTTPError{409, "version_conflict", "状态已变更，请重新加载"}
			}
			if builtin && in.Title != currentTitle {
				return txResponse{}, &txHTTPError{409, "builtin_status", "默认状态不可重命名"}
			}
			_, e = tx.Exec(r.Context(), `UPDATE ticket_statuses SET title=$2,sort_order=$3,active=coalesce($4,active),version=version+1,updated_at=now() WHERE id=$1`, id, in.Title, in.SortOrder, in.Active)
			version++
		}
		if e != nil {
			return txResponse{}, e
		}
		action := "ticket.status.updated"
		if created {
			action = "ticket.status.created"
		}
		if e = store.Audit(r.Context(), tx, identityFrom(r).ID, action, "ticket_status", id, in); e != nil {
			return txResponse{}, e
		}
		result := map[string]any{"id": id, "version": version, "builtin": builtin}
		if e = saveTicketMutation(r.Context(), tx, identityFrom(r).ID, key, hash, result); e != nil {
			return txResponse{}, e
		}

		return txResponse{http.StatusOK, result}, nil
	})
}
func (s *Server) ticketRules(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := serviceActivityPagination(r)
	if !ok {
		writeError(w, 400, "invalid_pagination", "分页参数无效")
		return
	}
	s.writeListPage(w, r, `SELECT count(*) FROM ticket_delivery_rules`,
		`SELECT id,department_id,product_id,blocked_keywords,auto_reply,active,version FROM ticket_delivery_rules ORDER BY id DESC`, nil, limit, offset)
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
	key, ok := ticketMutationKey(w, r)
	if !ok {
		return
	}
	if len(in.AutoReply) > 10000 || len(in.BlockedKeywords) > 100 {
		writeError(w, 400, "invalid_ticket_rule", "规则内容无效")
		return
	}
	hash, e := ticketMutationHash("rule.create", 0, in)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {

		if result, replay, err := loadTicketMutation(r.Context(), tx, identityFrom(r).ID, key, hash); err != nil {
			return txResponse{}, err
		} else if replay {

			return txResponse{http.StatusOK, result}, nil
		}
		var id, version int64
		if in.DepartmentID != nil {
			var ok bool
			if e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM ticket_departments WHERE id=$1)`, *in.DepartmentID).Scan(&ok); e != nil || !ok {
				return txResponse{}, &txHTTPError{400, "invalid_ticket_department", "部门不存在"}
			}
		}
		if in.ProductID != nil {
			var ok bool
			if e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM products WHERE id=$1)`, *in.ProductID).Scan(&ok); e != nil || !ok {
				return txResponse{}, &txHTTPError{400, "invalid_product", "产品不存在"}
			}
		}
		e = tx.QueryRow(r.Context(), `INSERT INTO ticket_delivery_rules(department_id,product_id,blocked_keywords,auto_reply,active) VALUES($1,$2,$3,$4,coalesce($5,true)) RETURNING id,version`, in.DepartmentID, in.ProductID, in.BlockedKeywords, in.AutoReply, in.Active).Scan(&id, &version)
		if e != nil {
			return txResponse{}, e
		}
		if e = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.rule.created", "ticket_delivery_rule", id, in); e != nil {
			return txResponse{}, e
		}
		result := map[string]any{"id": id, "version": version}
		if e = saveTicketMutation(r.Context(), tx, identityFrom(r).ID, key, hash, result); e != nil {
			return txResponse{}, e
		}

		return txResponse{http.StatusCreated, result}, nil
	})
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
	key, ok := ticketMutationKey(w, r)
	if !ok {
		return
	}
	hash, e := ticketMutationHash("status.update", id, in)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {

		if result, replay, err := loadTicketMutation(r.Context(), tx, identityFrom(r).ID, key, hash); err != nil {
			return txResponse{}, err
		} else if replay {

			return txResponse{http.StatusOK, result}, nil
		}
		var v int64
		var builtin bool
		var currentTitle string
		if e = tx.QueryRow(r.Context(), `SELECT version,builtin,title FROM ticket_statuses WHERE id=$1 FOR UPDATE`, id).Scan(&v, &builtin, &currentTitle); e != nil {
			return txResponse{}, e
		}
		if v != in.ExpectedVersion {
			return txResponse{}, &txHTTPError{409, "version_conflict", "状态已变更，请重新加载"}
		}
		if builtin && in.Title != currentTitle {
			return txResponse{}, &txHTTPError{http.StatusConflict, "builtin_status", "默认状态不可重命名"}
		}
		_, e = tx.Exec(r.Context(), `UPDATE ticket_statuses SET title=$2,sort_order=$3,active=coalesce($4,active),version=version+1,updated_at=now() WHERE id=$1`, id, in.Title, in.SortOrder, in.Active)
		if e != nil {
			return txResponse{}, e
		}
		if e = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.status.updated", "ticket_status", id, in); e != nil {
			return txResponse{}, e
		}
		result := map[string]any{"id": id, "version": v + 1, "builtin": builtin}
		if e = saveTicketMutation(r.Context(), tx, identityFrom(r).ID, key, hash, result); e != nil {
			return txResponse{}, e
		}

		return txResponse{http.StatusOK, result}, nil
	})
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
	key, ok := ticketMutationKey(w, r)
	if !ok {
		return
	}
	hash, e := ticketMutationHash("status.delete", id, in)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {

		if result, replay, err := loadTicketMutation(r.Context(), tx, identityFrom(r).ID, key, hash); err != nil {
			return txResponse{}, err
		} else if replay {

			return txResponse{http.StatusOK, result}, nil
		}
		var builtin bool
		var v int64
		if e = tx.QueryRow(r.Context(), `SELECT builtin,version FROM ticket_statuses WHERE id=$1 FOR UPDATE`, id).Scan(&builtin, &v); e != nil {
			return txResponse{}, e
		}
		if builtin {
			return txResponse{}, &txHTTPError{409, "builtin_status", "默认状态不可删除"}
		}
		if v != in.ExpectedVersion {
			return txResponse{}, &txHTTPError{409, "version_conflict", "状态已变更，请重新加载"}
		}
		if _, e = tx.Exec(r.Context(), `DELETE FROM ticket_statuses WHERE id=$1`, id); e != nil {
			return txResponse{}, e
		}
		if e = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.status.deleted", "ticket_status", id, in); e != nil {
			return txResponse{}, e
		}
		result := map[string]any{"id": id, "deleted": true}
		if e = saveTicketMutation(r.Context(), tx, identityFrom(r).ID, key, hash, result); e != nil {
			return txResponse{}, e
		}

		return txResponse{http.StatusOK, result}, nil
	})
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
	key, ok := ticketMutationKey(w, r)
	if !ok {
		return
	}
	hash, e := ticketMutationHash("rule.update", id, in)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	tx, e := s.db.Pool.Begin(r.Context())
	if e != nil {
		s.fail(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	if result, replay, err := loadTicketMutation(r.Context(), tx, identityFrom(r).ID, key, hash); err != nil {
		s.fail(w, r, err)
		return
	} else if replay {
		if err = tx.Commit(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, http.StatusOK, result)
		return
	}
	if in.DepartmentID != nil {
		var exists bool
		if e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM ticket_departments WHERE id=$1)`, *in.DepartmentID).Scan(&exists); e != nil || !exists {
			if e != nil {
				s.fail(w, r, e)
			} else {
				writeError(w, http.StatusBadRequest, "invalid_ticket_department", "部门不存在")
			}
			return
		}
	}
	if in.ProductID != nil {
		var exists bool
		if e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM products WHERE id=$1)`, *in.ProductID).Scan(&exists); e != nil || !exists {
			if e != nil {
				s.fail(w, r, e)
			} else {
				writeError(w, http.StatusBadRequest, "invalid_product", "产品不存在")
			}
			return
		}
	}
	var v int64
	if e = tx.QueryRow(r.Context(), `SELECT version FROM ticket_delivery_rules WHERE id=$1 FOR UPDATE`, id).Scan(&v); e != nil {
		s.fail(w, r, e)
		return
	}
	if v != in.ExpectedVersion {
		writeError(w, 409, "version_conflict", "规则已变更，请重新加载")
		return
	}
	_, e = tx.Exec(r.Context(), `UPDATE ticket_delivery_rules SET department_id=$2,product_id=$3,blocked_keywords=$4,auto_reply=$5,active=coalesce($6,active),version=version+1,updated_at=now() WHERE id=$1`, id, in.DepartmentID, in.ProductID, in.BlockedKeywords, in.AutoReply, in.Active)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	if e = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.rule.updated", "ticket_delivery_rule", id, in); e != nil {
		s.fail(w, r, e)
		return
	}
	result := map[string]any{"id": id, "version": v + 1}
	if e = saveTicketMutation(r.Context(), tx, identityFrom(r).ID, key, hash, result); e != nil {
		s.fail(w, r, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, http.StatusOK, result)
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
	key, ok := ticketMutationKey(w, r)
	if !ok {
		return
	}
	hash, e := ticketMutationHash("rule.delete", id, in)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {

		if result, replay, err := loadTicketMutation(r.Context(), tx, identityFrom(r).ID, key, hash); err != nil {
			return txResponse{}, err
		} else if replay {

			return txResponse{http.StatusOK, result}, nil
		}
		var v int64
		if e = tx.QueryRow(r.Context(), `SELECT version FROM ticket_delivery_rules WHERE id=$1 FOR UPDATE`, id).Scan(&v); e != nil {
			return txResponse{}, e
		}
		if v != in.ExpectedVersion {
			return txResponse{}, &txHTTPError{409, "version_conflict", "规则已变更，请重新加载"}
		}
		if _, e = tx.Exec(r.Context(), `DELETE FROM ticket_delivery_rules WHERE id=$1`, id); e != nil {
			return txResponse{}, e
		}
		if e = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.rule.deleted", "ticket_delivery_rule", id, in); e != nil {
			return txResponse{}, e
		}
		result := map[string]any{"id": id, "deleted": true}
		if e = saveTicketMutation(r.Context(), tx, identityFrom(r).ID, key, hash, result); e != nil {
			return txResponse{}, e
		}

		return txResponse{http.StatusOK, result}, nil
	})
}
