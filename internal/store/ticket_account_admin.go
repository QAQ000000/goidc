package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
)

func ticketAccountPage(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, query, order string, args []any, limit, offset int) (map[string]any, error) {
	var total int64
	if err := q.QueryRow(ctx, "SELECT count(*) FROM ("+query+") ticket_page", args...).Scan(&total); err != nil {
		return nil, err
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := q.Query(ctx, "SELECT * FROM ("+query+") ticket_page ORDER BY "+order+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), pageArgs...)
	items, err := collectMaps(rows, err)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "total": total, "limit": limit, "offset": offset}, nil
}

type AdminTicketCreateInput struct {
	CustomerID   int64    `json:"customer_id"`
	DepartmentID int64    `json:"department_id"`
	ServiceID    *int64   `json:"service_id"`
	AssigneeID   *int64   `json:"assignee_id"`
	Subject      string   `json:"subject"`
	Body         string   `json:"body"`
	Priority     string   `json:"priority"`
	CC           []string `json:"cc"`
	SendEmail    bool     `json:"send_email"`
}

type AdminTicketCreateResult struct {
	ID          int64  `json:"id"`
	TicketNo    string `json:"ticket_no"`
	Version     int64  `json:"version"`
	EmailStatus string `json:"email_status"`
	Replayed    bool   `json:"replayed"`
}

func (s *Store) CreateAdminTicketAccount(ctx context.Context, actor int64, key, hash string, in AdminTicketCreateInput) (AdminTicketCreateResult, int, error) {
	ccJSON, err := json.Marshal(in.CC)
	if err != nil {
		return AdminTicketCreateResult{}, 0, err
	}
	var savedResult AdminTicketCreateResult
	status := 201
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		if err = lockAdminTicketManager(ctx, tx, actor); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("admin-ticket-create:%d:%s", actor, key)); err != nil {
			return err
		}
		var savedHash string
		var savedJSON []byte
		err = tx.QueryRow(ctx, `SELECT request_hash,result FROM admin_ticket_creation_receipts WHERE actor_id=$1 AND idempotency_key=$2`, actor, key).Scan(&savedHash, &savedJSON)
		if err == nil {
			if savedHash != hash {
				return ticketAccountError(409, "idempotency_conflict", "相同请求键已用于不同内容")
			}
			var result AdminTicketCreateResult
			if err = json.Unmarshal(savedJSON, &result); err != nil {
				return err
			}
			var accessible bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tickets t WHERE t.id=$1 AND admin_ticket_access_allowed($2,t.user_id,t.department_id))`, result.ID, actor).Scan(&accessible); err != nil {
				return err
			}
			if !accessible {
				return ticketAccountError(404, "not_found", "工单不存在或无权查看")
			}
			result.Replayed = true

			savedResult = result
			status = 200
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err = LockAdminCustomer(ctx, tx, actor, in.CustomerID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ticketAccountError(404, "not_found", "客户不存在或无权选择")
			}
			return err
		}
		var customerEmail string
		var customerEmailVerified bool
		err = tx.QueryRow(ctx, `SELECT email,email_verified FROM users
WHERE id=$1 AND role='customer' AND status='active' AND admin_ticket_customer_allowed($2,id) FOR SHARE`, in.CustomerID, actor).Scan(&customerEmail, &customerEmailVerified)
		if errors.Is(err, pgx.ErrNoRows) {
			return ticketAccountError(404, "not_found", "客户不存在或无权选择")
		}
		if err != nil {
			return err
		}
		var departmentActive, departmentAllowed bool
		err = tx.QueryRow(ctx, `SELECT active,admin_ticket_department_allowed($1,id) FROM ticket_departments WHERE id=$2 FOR SHARE`, actor, in.DepartmentID).Scan(&departmentActive, &departmentAllowed)
		if errors.Is(err, pgx.ErrNoRows) {
			return ticketAccountError(409, "invalid_ticket_department", "工单部门不可用")
		}
		if err != nil {
			return err
		}
		if !departmentAllowed {
			return ticketAccountError(403, "ticket_department_forbidden", "没有此工单部门的访问权限")
		}
		if !departmentActive {
			return ticketAccountError(409, "invalid_ticket_department", "工单部门不可用")
		}
		if in.ServiceID != nil {
			var serviceID int64
			if err = tx.QueryRow(ctx, `SELECT id FROM services WHERE id=$1 AND user_id=$2 FOR SHARE`, *in.ServiceID, in.CustomerID).Scan(&serviceID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if errors.Is(err, pgx.ErrNoRows) {
				return ticketAccountError(409, "invalid_ticket_service", "关联服务不属于所选客户")
			}
		}
		if in.AssigneeID != nil {
			if err = lockAdminTicketAssignee(ctx, tx, *in.AssigneeID, in.DepartmentID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ticketAccountError(409, "invalid_ticket_assignee", "处理人不可用或无权处理此部门工单")
				}
				return err
			}
		}
		var ticket AdminTicketCreateResult
		err = tx.QueryRow(ctx, `INSERT INTO tickets(ticket_no,user_id,department_id,assignee_id,service_id,subject,body,priority,created_by,cc,send_initial_email,initial_author_kind)
VALUES('T-'||replace(gen_random_uuid()::text,'-',''),$1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,'staff')
RETURNING id,ticket_no,version`, in.CustomerID, in.DepartmentID, in.AssigneeID, in.ServiceID, in.Subject, in.Body, in.Priority, actor, string(ccJSON), in.SendEmail).Scan(&ticket.ID, &ticket.TicketNo, &ticket.Version)
		if err != nil {
			return err
		}
		ticket.EmailStatus = "not_requested"
		if in.SendEmail {
			ticket.EmailStatus, err = queueAdminTicketEmail(ctx, tx, ticket.ID, customerEmail, customerEmailVerified, in.CC, ticket.TicketNo, in.Subject, in.Body)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE tickets SET initial_email_status=$2 WHERE id=$1`, ticket.ID, ticket.EmailStatus); err != nil {
				return err
			}
		}
		resultJSON, err := json.Marshal(ticket)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO admin_ticket_creation_receipts(actor_id,idempotency_key,customer_id,ticket_id,request_hash,result)
VALUES($1,$2,$3,$4,$5,$6::jsonb)`, actor, key, in.CustomerID, ticket.ID, hash, string(resultJSON)); err != nil {
			return err
		}
		if err = Audit(ctx, tx, actor, "ticket.admin_created", "ticket", ticket.ID, map[string]any{"customer_id": in.CustomerID, "department_id": in.DepartmentID, "service_id": in.ServiceID, "assignee_id": in.AssigneeID, "priority": in.Priority, "cc_count": len(in.CC), "send_email": in.SendEmail, "email_status": ticket.EmailStatus}); err != nil {
			return err
		}

		savedResult = ticket
		return nil
	})
	return savedResult, status, err
}
func lockAdminTicketManager(ctx context.Context, tx pgx.Tx, actor int64) error {
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, `SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE`, actor).Scan(&role, &roleID); err != nil {
		return err
	}
	if role == "super_admin" {
		return nil
	}
	if role != "admin" || roleID == nil {
		return &Conflict{Code: "permission_denied", Message: "没有管理工单的权限"}
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT active AND 'tickets.manage'=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE`, *roleID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return &Conflict{Code: "permission_denied", Message: "没有管理工单的权限"}
	}
	return nil
}

func lockAdminTicketAssignee(ctx context.Context, tx pgx.Tx, assigneeID, departmentID int64) error {
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, `SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE`, assigneeID).Scan(&role, &roleID); err != nil {
		return err
	}
	if role == "super_admin" {
		return nil
	}
	if role != "admin" || roleID == nil {
		return pgx.ErrNoRows
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT active AND 'tickets.manage'=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE`, *roleID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return pgx.ErrNoRows
	}
	var inDepartment bool
	if err := tx.QueryRow(ctx, `SELECT admin_ticket_department_allowed($1,$2)`, assigneeID, departmentID).Scan(&inDepartment); err != nil {
		return err
	}
	if !inDepartment {
		return pgx.ErrNoRows
	}
	return nil
}

func queueAdminTicketEmail(ctx context.Context, tx pgx.Tx, ticketID int64, customerEmail string, customerVerified bool, cc []string, ticketNo, subject, body string) (string, error) {
	if !customerVerified || strings.TrimSpace(customerEmail) == "" {
		return "skipped_unverified", nil
	}
	var instance string
	err := tx.QueryRow(ctx, `SELECT id FROM channel_instances WHERE category='mail' AND enabled ORDER BY sort_order,id LIMIT 1`).Scan(&instance)
	if errors.Is(err, pgx.ErrNoRows) {
		return "skipped_channel", nil
	}
	if err != nil {
		return "", err
	}
	var templateSubject, templateBody string
	var active bool
	err = tx.QueryRow(ctx, `SELECT subject,body,active FROM notification_templates WHERE channel='email' AND kind='ticket_created'`).Scan(&templateSubject, &templateBody, &active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		return "skipped_template", nil
	}
	if err != nil {
		return "", err
	}
	replacer := strings.NewReplacer("{{ticket_no}}", ticketNo, "{{subject}}", subject, "{{body}}", body)
	messageSubject, messageBody := replacer.Replace(templateSubject), replacer.Replace(templateBody)
	recipients := make([]string, 0, len(cc)+1)
	seen := map[string]struct{}{strings.ToLower(strings.TrimSpace(customerEmail)): {}}
	recipients = append(recipients, strings.TrimSpace(customerEmail))
	for _, address := range cc {
		key := strings.ToLower(address)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		recipients = append(recipients, address)
	}
	for _, recipient := range recipients {
		if _, err = tx.Exec(ctx, `INSERT INTO email_outbox(recipient,subject,body,kind,channel_instance_id,ticket_id) VALUES($1,$2,$3,'ticket_created',$4,$5)`, recipient, messageSubject, messageBody, instance, ticketID); err != nil {
			return "", err
		}
	}
	return "queued", nil
}

func (s *Store) AdminTicketAccountPage(ctx context.Context, actor int64, status, priority, q string, customerID *int64, departmentFilter bool, departmentID int64, limit, offset int) (map[string]any, error) {
	query := `SELECT t.id,t.ticket_no,t.legacy_ticket_no,t.user_id,u.display_name AS customer_name,
t.department_id,d.name AS department_name,t.assignee_id,a.display_name AS assignee_name,
t.subject,t.status,t.priority,t.version,t.created_at,t.updated_at
FROM tickets t JOIN users u ON u.id=t.user_id
LEFT JOIN ticket_departments d ON d.id=t.department_id LEFT JOIN administrators a ON a.id=t.assignee_id
WHERE t.merged_ticket_id IS NULL
AND admin_ticket_department_allowed($1,t.department_id)
AND admin_ticket_customer_allowed($1,t.user_id)
AND ($2='' OR t.status=$2) AND ($3='' OR t.priority=$3)
AND ($4='' OR t.subject ILIKE '%'||$4||'%' OR t.ticket_no ILIKE '%'||$4||'%' OR t.legacy_ticket_no ILIKE '%'||$4||'%' OR u.display_name ILIKE '%'||$4||'%' OR u.email ILIKE '%'||$4||'%')
AND ($5::bigint IS NULL OR t.user_id=$5)
AND (NOT $6::boolean OR (($7::bigint=0 AND t.department_id IS NULL) OR t.department_id=$7))`
	return customerAccountReadPage(ctx, s.Pool, query, "updated_at DESC,id DESC", []any{actor, status, priority, q, customerID, departmentFilter, departmentID}, limit, offset)
}
func (s *Store) AdminTicketAccountOptions(ctx context.Context, actor int64, kind, q string, relation int64, limit, offset int) (map[string]any, error) {
	if kind == "" {
		var enabled bool
		err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM channel_instances WHERE category='mail' AND enabled)`).Scan(&enabled)
		return map[string]any{"mail_enabled": enabled}, err
	}
	var query string
	var args []any
	order := "id DESC"
	switch kind {
	case "customers":
		query = `SELECT id,display_name AS name,email FROM users WHERE role='customer' AND status='active' AND admin_ticket_customer_allowed($1,id) AND ($2='' OR username ILIKE '%'||$2||'%' OR display_name ILIKE '%'||$2||'%' OR email ILIKE '%'||$2||'%')`
		args = []any{actor, q}
	case "departments":
		query = `SELECT id,name FROM ticket_departments WHERE active AND admin_ticket_department_allowed($1,id)`
		args = []any{actor}
		order = "name,id"
	case "services":
		var allowed bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='customer' AND status='active' AND admin_ticket_customer_allowed($2,id))`, relation, actor).Scan(&allowed); err != nil {
			return nil, err
		}
		if !allowed {
			return nil, ticketAccountError(404, "not_found", "客户不存在或无权查看")
		}
		query = `SELECT s.id,p.name,s.domain,s.status FROM services s JOIN products p ON p.id=s.product_id WHERE s.user_id=$1 AND ($2='' OR p.name ILIKE '%'||$2||'%' OR s.domain ILIKE '%'||$2||'%' OR s.id::text ILIKE '%'||$2||'%')`
		args = []any{relation, q}
	case "assignees":
		var active, allowed bool
		if err := s.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT active FROM ticket_departments WHERE id=$2),false),admin_ticket_department_allowed($1,$2)`, actor, relation).Scan(&active, &allowed); err != nil {
			return nil, err
		}
		if !allowed {
			return nil, ticketAccountError(403, "ticket_department_forbidden", "没有此工单部门的访问权限")
		}
		if !active {
			return nil, ticketAccountError(409, "invalid_ticket_department", "工单部门不可用")
		}
		query = `SELECT u.id,u.display_name AS name FROM administrators u LEFT JOIN admin_roles ar ON ar.id=u.admin_role_id WHERE u.status='active' AND (u.role='super_admin' OR (u.role='admin' AND ar.active AND 'tickets.manage'=ANY(ar.permissions))) AND admin_ticket_department_allowed(u.id,$1) AND ($2='' OR u.username ILIKE '%'||$2||'%' OR u.display_name ILIKE '%'||$2||'%')`
		args = []any{relation, q}
		order = "name,id"
	default:
		return nil, ticketAccountError(400, "invalid_ticket_options", "工单选项类型无效")
	}
	return customerAccountReadPage(ctx, s.Pool, query, order, args, limit, offset)
}
