package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type ticketScopeQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	var scope string
	var unassigned bool
	var customerScope string
	var ownerless, salesEnabled bool
	if err := s.db.Pool.QueryRow(r.Context(), "SELECT ticket_scope,admin_ticket_department_allowed(id,NULL),customer_scope,customer_ownerless,sales_enabled FROM users WHERE id=$1", identityFrom(r).ID).Scan(&scope, &unassigned, &customerScope, &ownerless, &salesEnabled); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, struct {
		identity
		TicketScope       string `json:"ticket_scope"`
		TicketUnassigned  bool   `json:"ticket_unassigned"`
		CustomerScope     string `json:"customer_scope"`
		CustomerOwnerless bool   `json:"customer_ownerless"`
		SalesEnabled      bool   `json:"sales_enabled"`
	}{identityFrom(r), scope, unassigned, customerScope, ownerless, salesEnabled})
}

func (s *Server) allowTicketDepartment(w http.ResponseWriter, r *http.Request, q ticketScopeQuerier, department *int64) bool {
	var allowed bool
	if err := q.QueryRow(r.Context(), "SELECT admin_ticket_department_allowed($1,$2)", identityFrom(r).ID, department).Scan(&allowed); err != nil {
		s.fail(w, r, err)
		return false
	}
	if !allowed {
		writeError(w, 403, "ticket_department_forbidden", "没有此工单部门的访问权限")
		return false
	}
	return true
}

// Missing or zero department identifies the unassigned queue in assignee lookups.
func ticketDepartmentFilter(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	value := r.URL.Query().Get("department_id")
	if value == "" || value == "0" {
		return nil, true
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "invalid_ticket_department", "工单部门编号无效")
		return nil, false
	}
	return &id, true
}

func (s *Server) adminTicketDepartments(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, `SELECT d.id,d.name,d.slug,d.sort_order,d.active,d.customer_visible,d.version,
 (SELECT count(*) FROM tickets t WHERE t.department_id=d.id) AS ticket_count,
 (SELECT count(*) FROM admin_ticket_departments a WHERE a.department_id=d.id) AS member_count
 FROM ticket_departments d WHERE admin_ticket_department_allowed($1,d.id) ORDER BY d.sort_order,d.id`, identityFrom(r).ID)
}

func (s *Server) updateTicketDepartment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name            string `json:"name"`
		Active          *bool  `json:"active"`
		SortOrder       int    `json:"sort_order"`
		ExpectedVersion int64  `json:"expected_version"`
		CustomerVisible *bool  `json:"customer_visible"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 255 || in.Active == nil || in.ExpectedVersion < 1 || in.SortOrder < -100000 || in.SortOrder > 100000 {
		writeError(w, 400, "invalid_ticket_department", "部门资料或版本无效")
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var version int64
		if version, err = store.LockVersion(r.Context(), tx, "ticket_departments", id, in.ExpectedVersion, "部门已变更，请重新加载"); err != nil {
			return txResponse{}, err
		}
		if _, err = store.UpdateVersioned(r.Context(), tx, "ticket_departments", `name=$1,active=$2,sort_order=$3,customer_visible=coalesce($4,customer_visible)`, id, in.ExpectedVersion, in.Name, *in.Active, in.SortOrder, in.CustomerVisible); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.department.updated", "ticket_department", id, in); err != nil {
			return txResponse{}, err
		}

		return txResponse{200, map[string]any{"id": id, "version": version + 1}}, nil
	})
}

func (s *Server) deleteTicketDepartment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if _, err = store.LockVersion(r.Context(), tx, "ticket_departments", id, in.ExpectedVersion, "部门已变更，请重新加载"); err != nil {
			return txResponse{}, err
		}
		var used bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tickets WHERE department_id=$1) OR EXISTS(SELECT 1 FROM admin_ticket_departments WHERE department_id=$1)
 OR EXISTS(SELECT 1 FROM ticket_transfer_logs WHERE department_id=$1 OR previous_department_id=$1)`, id).Scan(&used); err != nil {
			return txResponse{}, err
		}
		if used {
			return txResponse{}, &txHTTPError{409, "department_in_use", "部门仍有关联工单、管理员或转移记录，请停用保留历史"}
		}
		if _, err = tx.Exec(r.Context(), "DELETE FROM ticket_departments WHERE id=$1", id); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.department.deleted", "ticket_department", id, map[string]any{}); err != nil {
			return txResponse{}, err
		}

		return txResponse{200, map[string]bool{"deleted": true}}, nil
	})
}
