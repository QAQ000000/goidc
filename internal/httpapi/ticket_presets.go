package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) ticketReplyPresets(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, `SELECT p.id,p.category_id,c.name AS category_name,c.active AS category_active,p.title,p.body,p.active,p.created_at,p.updated_at
FROM ticket_reply_presets p JOIN ticket_reply_preset_categories c ON c.id=p.category_id ORDER BY c.sort_order,c.id,p.id`)
}

func (s *Server) ticketReplyPresetCategories(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, "SELECT id,name,sort_order,active,created_at,updated_at FROM ticket_reply_preset_categories ORDER BY sort_order,id")
}

func (s *Server) createTicketReplyPresetCategory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name      string `json:"name"`
		SortOrder int    `json:"sort_order"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 120 || input.SortOrder < -100000 || input.SortOrder > 100000 {
		writeError(w, http.StatusBadRequest, "invalid_preset_category", "预设回复分类无效")
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var id int64
		if err = tx.QueryRow(r.Context(), "INSERT INTO ticket_reply_preset_categories(name,sort_order) VALUES($1,$2) RETURNING id", input.Name, input.SortOrder).Scan(&id); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.preset_category.created", "ticket_reply_preset_category", id, input); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusCreated, map[string]any{"id": id, "name": input.Name, "sort_order": input.SortOrder, "active": true}}, nil
	})
}

func (s *Server) updateTicketReplyPresetCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Name      *string `json:"name"`
		SortOrder *int    `json:"sort_order"`
		Active    *bool   `json:"active"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Name == nil && input.SortOrder == nil && input.Active == nil {
		writeError(w, http.StatusBadRequest, "invalid_preset_category", "至少提供一个修改字段")
		return
	}
	if input.Name != nil {
		*input.Name = strings.TrimSpace(*input.Name)
		if *input.Name == "" || len(*input.Name) > 120 {
			writeError(w, http.StatusBadRequest, "invalid_preset_category", "预设回复分类无效")
			return
		}
	}
	if input.SortOrder != nil && (*input.SortOrder < -100000 || *input.SortOrder > 100000) {
		writeError(w, http.StatusBadRequest, "invalid_preset_category", "排序值无效")
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if _, err = tx.Exec(r.Context(), "UPDATE ticket_reply_preset_categories SET name=COALESCE($2,name),sort_order=COALESCE($3,sort_order),active=COALESCE($4,active),updated_at=now() WHERE id=$1", id, input.Name, input.SortOrder, input.Active); err != nil {
			return txResponse{}, err
		}
		var exists bool
		if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM ticket_reply_preset_categories WHERE id=$1)", id).Scan(&exists); err != nil {
			return txResponse{}, err
		}
		if !exists {
			return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "预设回复分类不存在"}
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.preset_category.updated", "ticket_reply_preset_category", id, input); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, map[string]any{"id": id, "updated": true}}, nil
	})
}

func (s *Server) deleteTicketReplyPresetCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var lockedID int64
		err = tx.QueryRow(r.Context(), "SELECT id FROM ticket_reply_preset_categories WHERE id=$1 FOR UPDATE", id).Scan(&lockedID)
		if errors.Is(err, pgx.ErrNoRows) {
			return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "预设回复分类不存在"}
		}
		if err != nil {
			return txResponse{}, err
		}
		var referenced bool
		if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM ticket_reply_presets WHERE category_id=$1)", id).Scan(&referenced); err != nil {
			return txResponse{}, err
		}
		if referenced {
			return txResponse{}, &txHTTPError{http.StatusConflict, "preset_category_in_use", "请先删除或移动分类中的预设回复"}
		}
		if _, err = tx.Exec(r.Context(), "DELETE FROM ticket_reply_preset_categories WHERE id=$1", id); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.preset_category.deleted", "ticket_reply_preset_category", id, map[string]any{}); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, map[string]any{"id": id, "deleted": true}}, nil
	})
}

func validatePreset(categoryID int64, title, body string) bool {
	return categoryID > 0 && strings.TrimSpace(title) != "" && len(title) <= 240 && strings.TrimSpace(body) != "" && len(body) <= 10000
}

func (s *Server) createTicketReplyPreset(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CategoryID int64  `json:"category_id"`
		Title      string `json:"title"`
		Body       string `json:"body"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Title, input.Body = strings.TrimSpace(input.Title), strings.TrimSpace(input.Body)
	if !validatePreset(input.CategoryID, input.Title, input.Body) {
		writeError(w, http.StatusBadRequest, "invalid_preset", "预设回复内容无效")
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var categoryActive bool
		if err = tx.QueryRow(r.Context(), "SELECT active FROM ticket_reply_preset_categories WHERE id=$1", input.CategoryID).Scan(&categoryActive); err != nil {
			return txResponse{}, err
		}
		if !categoryActive {
			return txResponse{}, &txHTTPError{http.StatusConflict, "invalid_preset_category", "预设回复分类不可用"}
		}
		var id int64
		if err = tx.QueryRow(r.Context(), "INSERT INTO ticket_reply_presets(category_id,title,body) VALUES($1,$2,$3) RETURNING id", input.CategoryID, input.Title, input.Body).Scan(&id); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.preset.created", "ticket_reply_preset", id, input); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusCreated, map[string]any{"id": id, "category_id": input.CategoryID, "title": input.Title, "body": input.Body, "active": true}}, nil
	})
}

func (s *Server) updateTicketReplyPreset(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		CategoryID *int64  `json:"category_id"`
		Title      *string `json:"title"`
		Body       *string `json:"body"`
		Active     *bool   `json:"active"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Title != nil {
		*input.Title = strings.TrimSpace(*input.Title)
	}
	if input.Body != nil {
		*input.Body = strings.TrimSpace(*input.Body)
	}
	if input.CategoryID == nil && input.Title == nil && input.Body == nil && input.Active == nil {
		writeError(w, http.StatusBadRequest, "invalid_preset", "至少提供一个修改字段")
		return
	}
	if input.Title != nil && (*input.Title == "" || len(*input.Title) > 240) || input.Body != nil && (*input.Body == "" || len(*input.Body) > 10000) {
		writeError(w, http.StatusBadRequest, "invalid_preset", "预设回复内容无效")
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if input.CategoryID != nil {
			var active bool
			if err = tx.QueryRow(r.Context(), "SELECT active FROM ticket_reply_preset_categories WHERE id=$1", *input.CategoryID).Scan(&active); err != nil {
				return txResponse{}, err
			}
			if !active {
				return txResponse{}, &txHTTPError{http.StatusConflict, "invalid_preset_category", "预设回复分类不可用"}
			}
		}
		if _, err = tx.Exec(r.Context(), "UPDATE ticket_reply_presets SET category_id=COALESCE($2,category_id),title=COALESCE($3,title),body=COALESCE($4,body),active=COALESCE($5,active),updated_at=now() WHERE id=$1", id, input.CategoryID, input.Title, input.Body, input.Active); err != nil {
			return txResponse{}, err
		}
		var exists bool
		if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM ticket_reply_presets WHERE id=$1)", id).Scan(&exists); err != nil {
			return txResponse{}, err
		}
		if !exists {
			return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "预设回复不存在"}
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.preset.updated", "ticket_reply_preset", id, input); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, map[string]any{"id": id, "updated": true}}, nil
	})
}

func (s *Server) deleteTicketReplyPreset(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var exists bool
		if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM ticket_reply_presets WHERE id=$1)", id).Scan(&exists); err != nil {
			return txResponse{}, err
		}
		if !exists {
			return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "预设回复不存在"}
		}
		if _, err = tx.Exec(r.Context(), "DELETE FROM ticket_reply_presets WHERE id=$1", id); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.preset.deleted", "ticket_reply_preset", id, map[string]any{}); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, map[string]any{"id": id, "deleted": true}}, nil
	})
}
