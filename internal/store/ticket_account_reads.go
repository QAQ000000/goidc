package store

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// TicketAccountError preserves the ticket contract's HTTP status and stable code
// when admission fails inside a store transaction.
type TicketAccountError struct {
	Status        int
	Code, Message string
}

func (e *TicketAccountError) Error() string { return e.Code + ": " + e.Message }
func ticketAccountError(status int, code, message string) error {
	return &TicketAccountError{status, code, message}
}

type TicketScopeQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func TicketDepartmentAllowed(ctx context.Context, q TicketScopeQuery, actor int64, department *int64) (bool, error) {
	var allowed bool
	err := q.QueryRow(ctx, "SELECT admin_ticket_department_allowed($1,$2)", actor, department).Scan(&allowed)
	return allowed, err
}
func requireTicketDepartment(ctx context.Context, q TicketScopeQuery, actor int64, department *int64) error {
	allowed, err := TicketDepartmentAllowed(ctx, q, actor, department)
	if err != nil {
		return err
	}
	if !allowed {
		return ticketAccountError(403, "ticket_department_forbidden", "没有此工单部门的访问权限")
	}
	return nil
}
func (s *Store) CurrentTicketAccountScopes(ctx context.Context, actor int64) (string, bool, string, bool, bool, error) {
	var scope, customer string
	var unassigned, ownerless, sales bool
	err := s.Pool.QueryRow(ctx, "SELECT ticket_scope,admin_ticket_department_allowed(id,NULL),customer_scope,customer_ownerless,sales_enabled FROM account_identities WHERE id=$1", actor).Scan(&scope, &unassigned, &customer, &ownerless, &sales)
	return scope, unassigned, customer, ownerless, sales, err
}
func (s *Store) TicketAssigneeDirectory(ctx context.Context, actor int64, department *int64) ([]map[string]any, error) {
	if err := requireTicketDepartment(ctx, s.Pool, actor, department); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT u.id,u.username,u.display_name,u.role FROM administrators u LEFT JOIN admin_roles ar ON ar.id=u.admin_role_id WHERE u.status='active' AND admin_ticket_department_allowed(u.id,$1) AND (u.role='super_admin' OR (u.role='admin' AND ar.active AND 'tickets.manage'=ANY(ar.permissions))) ORDER BY u.display_name,u.id`, department)
	return collectMaps(rows, err)
}
func (s *Store) TicketList(ctx context.Context, actor int64, admin bool, limit, offset int, filter bool, department *int64, query string, customer *int64) ([]map[string]any, error) {
	if admin && filter {
		if err := requireTicketDepartment(ctx, s.Pool, actor, department); err != nil {
			return nil, err
		}
	}
	rows, err := s.Pool.Query(ctx, `SELECT t.id,t.ticket_no,t.legacy_ticket_no,t.user_id,u.display_name AS customer_name,t.department_id,d.name AS department_name,t.assignee_id,a.display_name AS assignee_name,t.subject,t.status,t.priority,t.version,t.created_at,t.updated_at
 FROM tickets t JOIN account_identities u ON u.id=t.user_id LEFT JOIN ticket_departments d ON d.id=t.department_id LEFT JOIN administrators a ON a.id=t.assignee_id
 WHERE t.merged_ticket_id IS NULL AND (($1::boolean AND admin_ticket_department_allowed($2,t.department_id)) OR (NOT $1::boolean AND t.user_id=$2 AND coalesce(d.customer_visible,true)))
 AND (NOT $5::boolean OR t.department_id IS NOT DISTINCT FROM $6::bigint)
 AND ($7='' OR t.subject ILIKE '%'||$7||'%' OR t.ticket_no ILIKE '%'||$7||'%' OR t.legacy_ticket_no ILIKE '%'||$7||'%')
 AND ($8::bigint IS NULL OR t.user_id=$8) ORDER BY t.updated_at DESC,t.id DESC LIMIT $3 OFFSET $4`, admin, actor, limit, offset, filter, department, query, customer)
	return collectMaps(rows, err)
}
func (s *Store) TicketDetail(ctx context.Context, id, actor int64, admin, customer bool) (map[string]any, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT t.id,t.ticket_no,t.legacy_ticket_no,t.user_id,t.merged_ticket_id,t.service_id,t.legacy_status_label,t.department_id,d.name AS department_name,t.assignee_id,a.display_name AS assignee_name,t.subject,t.body,t.status,t.priority,t.version,t.created_at,t.updated_at,($2::boolean AND admin_ticket_department_allowed($3,t.department_id)) AS admin_access FROM tickets t LEFT JOIN ticket_departments d ON d.id=t.department_id LEFT JOIN administrators a ON a.id=t.assignee_id WHERE t.id=$1 AND (($2::boolean AND admin_ticket_department_allowed($3,t.department_id)) OR (t.user_id=$3 AND coalesce(d.customer_visible,true)))", id, admin, actor)
	if err != nil {
		return nil, err
	}
	ticket, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if ticket["merged_ticket_id"] != nil {
		return nil, ticketAccountError(409, "ticket_merged", "该工单已合并到其他工单")
	}
	var initialRating map[string]any
	canRate := customer && ticket["user_id"] == actor
	if err = tx.QueryRow(ctx, `SELECT jsonb_build_object('stars',tr.stars,'provenance',tr.provenance,'can_rate',($2::boolean AND t.initial_author_kind='staff' AND t.created_by IS NOT NULL AND tr.id IS NULL)) FROM tickets t LEFT JOIN ticket_ratings tr ON tr.initial_ticket_id=t.id WHERE t.id=$1`, id, canRate).Scan(&initialRating); err != nil {
		return nil, err
	}
	ticket["initial_rating"] = initialRating
	rows, err = tx.Query(ctx, `SELECT r.id,r.body,r.created_at,u.display_name AS author_name,
CASE r.author_kind WHEN 'staff' THEN 'admin' WHEN 'customer' THEN 'customer' ELSE 'unknown' END AS author_role,
jsonb_build_object('stars',tr.stars,'provenance',tr.provenance,'can_rate',($2::boolean AND r.author_kind='staff' AND tr.id IS NULL)) AS rating
FROM ticket_replies r JOIN account_identities u ON u.id=r.author_id LEFT JOIN ticket_ratings tr ON tr.reply_id=r.id WHERE ticket_id=$1 ORDER BY r.created_at,r.id`, id, canRate)
	if err != nil {
		return nil, err
	}
	replies, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if replies == nil {
		replies = []map[string]any{}
	}
	rows, err = tx.Query(ctx, `SELECT a.id,a.ticket_id,a.reply_id,a.note_id,a.uploader_id,u.display_name AS uploader_name,a.filename,a.mime_type,a.size_bytes,a.sha256,a.created_at
FROM ticket_attachments a JOIN account_identities u ON u.id=a.uploader_id WHERE a.ticket_id=$1 AND (a.note_id IS NULL OR $2::boolean) ORDER BY a.id`, id, ticket["admin_access"] == true)
	if err != nil {
		return nil, err
	}
	attachments, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if attachments == nil {
		attachments = []map[string]any{}
	}
	ticket["replies"] = replies
	ticket["attachments"] = attachments
	if ticket["admin_access"] == true {
		rows, err = tx.Query(ctx, `SELECT n.id,n.ticket_id,n.author_id,u.display_name AS author_name,n.body,n.created_at
FROM ticket_notes n JOIN administrators u ON u.id=n.author_id WHERE n.ticket_id=$1 ORDER BY n.id`, id)
		if err != nil {
			return nil, err
		}
		notes, noteErr := pgx.CollectRows(rows, pgx.RowToMap)
		if noteErr != nil {
			return nil, noteErr
		}
		if notes == nil {
			notes = []map[string]any{}
		}
		ticket["notes"] = notes
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ticket, nil
}
func (s *Store) TicketNotes(ctx context.Context, id, actor int64) ([]map[string]any, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var ticketID int64
	if err = tx.QueryRow(ctx, "SELECT id FROM tickets WHERE id=$1 AND merged_ticket_id IS NULL AND admin_ticket_department_allowed($2,department_id)", id, actor).Scan(&ticketID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT n.id,n.ticket_id,n.author_id,u.display_name AS author_name,n.body,n.created_at FROM ticket_notes n JOIN administrators u ON u.id=n.author_id WHERE n.ticket_id=$1 ORDER BY n.id`, id)
	if err != nil {
		return nil, err
	}
	notes, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	if notes == nil {
		notes = []map[string]any{}
	}
	return notes, nil
}
