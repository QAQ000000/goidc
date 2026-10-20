package store

import (
	"context"
)

type TicketAssignmentInput struct {
	DepartmentID *int64 `json:"department_id"`
	AssigneeID   *int64 `json:"assignee_id"`
}
type TicketTransferInput struct {
	DepartmentID *int64 `json:"department_id"`
	AssigneeID   *int64 `json:"assignee_id"`
	Reason       string `json:"reason"`
}

func (s *Store) AssignTicketAccount(ctx context.Context, id, actor int64, input TicketAssignmentInput) (map[string]any, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var ownerID int64
	if err = tx.QueryRow(ctx, "SELECT user_id FROM tickets WHERE id=$1 AND merged_ticket_id IS NULL AND admin_ticket_department_allowed($2,department_id) FOR UPDATE", id, actor).Scan(&ownerID); err != nil {
		return nil, err
	}
	if err = requireTicketDepartment(ctx, tx, actor, input.DepartmentID); err != nil {
		return nil, err
	}
	if input.DepartmentID != nil {
		var active bool
		if err = tx.QueryRow(ctx, "SELECT COALESCE((SELECT active FROM ticket_departments WHERE id=$1), false)", *input.DepartmentID).Scan(&active); err != nil {
			return nil, err
		}
		if !active {
			return nil, ticketAccountError(409, "invalid_ticket_department", "工单部门不可用")
		}
	}
	if input.AssigneeID != nil {
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM administrators u LEFT JOIN admin_roles ar ON ar.id=u.admin_role_id WHERE u.id=$1 AND u.status='active' AND admin_ticket_department_allowed(u.id,$2) AND (u.role='super_admin' OR (u.role='admin' AND ar.active AND 'tickets.manage'=ANY(ar.permissions))))`, *input.AssigneeID, input.DepartmentID).Scan(&valid); err != nil {
			return nil, err
		}
		if !valid {
			return nil, ticketAccountError(409, "invalid_ticket_assignee", "处理人不可用")
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE tickets SET department_id=$2,assignee_id=$3,version=version+1,updated_at=now(),status=CASE WHEN status='open' THEN 'pending' ELSE status END WHERE id=$1", id, input.DepartmentID, input.AssigneeID); err != nil {
		return nil, err
	}
	if err = Audit(ctx, tx, actor, "ticket.assigned", "ticket", id, map[string]any{"department_id": input.DepartmentID, "assignee_id": input.AssigneeID, "customer_id": ownerID}); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"ticket_id": id, "department_id": input.DepartmentID, "assignee_id": input.AssigneeID}, nil
}
func (s *Store) TransferTicketAccount(ctx context.Context, tid, actor int64, input TicketTransferInput) (map[string]any, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var ownerID int64
	var previousDepartment, previousAssignee *int64
	var status string
	if err = tx.QueryRow(ctx, "SELECT user_id,department_id,assignee_id,status FROM tickets WHERE id=$1 AND merged_ticket_id IS NULL AND admin_ticket_department_allowed($2,department_id) FOR UPDATE", tid, actor).Scan(&ownerID, &previousDepartment, &previousAssignee, &status); err != nil {
		return nil, err
	}
	departmentID, assigneeID := previousDepartment, previousAssignee
	if input.DepartmentID != nil {
		if err = requireTicketDepartment(ctx, tx, actor, input.DepartmentID); err != nil {
			return nil, err
		}
	}
	if input.DepartmentID != nil {
		var active bool
		if err = tx.QueryRow(ctx, "SELECT COALESCE((SELECT active FROM ticket_departments WHERE id=$1),false)", *input.DepartmentID).Scan(&active); err != nil {
			return nil, err
		}
		if !active {
			return nil, ticketAccountError(409, "invalid_ticket_department", "工单部门不可用")
		}
		departmentID = input.DepartmentID
		if input.AssigneeID == nil {
			assigneeID = nil
		}
	}
	if input.AssigneeID != nil {
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM administrators u LEFT JOIN admin_roles ar ON ar.id=u.admin_role_id WHERE u.id=$1 AND u.status='active' AND admin_ticket_department_allowed(u.id,$2) AND (u.role='super_admin' OR (u.role='admin' AND ar.active AND 'tickets.manage'=ANY(ar.permissions))))`, *input.AssigneeID, departmentID).Scan(&valid); err != nil {
			return nil, err
		}
		if !valid {
			return nil, ticketAccountError(409, "invalid_ticket_assignee", "处理人不可用")
		}
		assigneeID = input.AssigneeID
	}
	if _, err = tx.Exec(ctx, `UPDATE tickets SET department_id=$2,assignee_id=$3,status=CASE WHEN $3::bigint IS NULL AND status='pending' THEN 'open' ELSE status END,version=version+1,updated_at=now() WHERE id=$1`, tid, departmentID, assigneeID); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ticket_transfer_logs(ticket_id,actor_id,previous_department_id,department_id,previous_assignee_id,assignee_id,reason)
VALUES($1,$2,$3,$4,$5,$6,$7)`, tid, actor, previousDepartment, departmentID, previousAssignee, assigneeID, input.Reason); err != nil {
		return nil, err
	}
	if err = Audit(ctx, tx, actor, "ticket.transferred", "ticket", tid, map[string]any{"department_id": departmentID, "assignee_id": assigneeID, "previous_department_id": previousDepartment, "previous_assignee_id": previousAssignee, "reason": input.Reason, "status": status, "customer_id": ownerID}); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"ticket_id": tid, "department_id": departmentID, "assignee_id": assigneeID}, nil
}
