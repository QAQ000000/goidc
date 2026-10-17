package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

var ticketPriorities = []map[string]string{
	{"value": "low", "label": "低"},
	{"value": "normal", "label": "普通"},
	{"value": "high", "label": "高"},
	{"value": "urgent", "label": "紧急"},
}

func (s *Server) adminTicketsPage(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "all" {
		status = ""
	}
	if status != "" && status != "open" && status != "pending" && status != "resolved" && status != "closed" {
		writeError(w, http.StatusBadRequest, "invalid_ticket_status", "工单状态筛选无效")
		return
	}
	priority := strings.TrimSpace(r.URL.Query().Get("priority"))
	if priority != "" && priority != "low" && priority != "normal" && priority != "high" && priority != "urgent" {
		writeError(w, http.StatusBadRequest, "invalid_ticket_priority", "工单优先级筛选无效")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 120 {
		writeError(w, http.StatusBadRequest, "invalid_query", "搜索条件过长")
		return
	}
	var customerID *int64
	if raw := strings.TrimSpace(r.URL.Query().Get("customer_id")); raw != "" {
		id, err := parsePositiveID(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_customer", "客户筛选无效")
			return
		}
		customerID = &id
	}
	departmentFilter := false
	var departmentID int64
	if raw := strings.TrimSpace(r.URL.Query().Get("department_id")); raw != "" && raw != "all" {
		departmentFilter = true
		if raw != "0" {
			id, err := parsePositiveID(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_ticket_department", "工单部门筛选无效")
				return
			}
			departmentID = id
		}
	}
	limit, offset := pagination(r)
	query := `SELECT t.id,t.ticket_no,t.legacy_ticket_no,t.user_id,u.display_name AS customer_name,
t.department_id,d.name AS department_name,t.assignee_id,a.display_name AS assignee_name,
t.subject,t.status,t.priority,t.version,t.created_at,t.updated_at
FROM tickets t JOIN users u ON u.id=t.user_id
LEFT JOIN ticket_departments d ON d.id=t.department_id LEFT JOIN users a ON a.id=t.assignee_id
WHERE t.merged_ticket_id IS NULL
AND admin_ticket_department_allowed($1,t.department_id)
AND admin_ticket_customer_allowed($1,t.user_id)
AND ($2='' OR t.status=$2) AND ($3='' OR t.priority=$3)
AND ($4='' OR t.subject ILIKE '%'||$4||'%' OR t.ticket_no ILIKE '%'||$4||'%' OR t.legacy_ticket_no ILIKE '%'||$4||'%' OR u.display_name ILIKE '%'||$4||'%' OR u.email ILIKE '%'||$4||'%')
AND ($5::bigint IS NULL OR t.user_id=$5)
AND (NOT $6::boolean OR (($7::bigint=0 AND t.department_id IS NULL) OR t.department_id=$7))`
	s.adminListPage(w, r, query, "updated_at DESC,id DESC", limit, offset,
		identityFrom(r).ID, status, priority, q, customerID, departmentFilter, departmentID)
}

func (s *Server) adminTicketCreateOptions(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		var mailEnabled bool
		if err := s.db.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM channel_instances WHERE category='mail' AND enabled)`).Scan(&mailEnabled); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, http.StatusOK, map[string]any{"priorities": ticketPriorities, "mail_enabled": mailEnabled})
		return
	}
	if kind != "customers" && kind != "departments" && kind != "services" && kind != "assignees" {
		writeError(w, http.StatusBadRequest, "invalid_ticket_options", "工单选项类型无效")
		return
	}
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 120 {
		writeError(w, http.StatusBadRequest, "invalid_query", "搜索条件过长")
		return
	}
	actor := identityFrom(r).ID
	var query string
	var args []any
	switch kind {
	case "customers":
		query = `SELECT id,display_name AS name,email FROM users
WHERE role='customer' AND status='active' AND admin_ticket_customer_allowed($1,id)
AND ($2='' OR username ILIKE '%'||$2||'%' OR display_name ILIKE '%'||$2||'%' OR email ILIKE '%'||$2||'%')`
		args = []any{actor, q}
	case "departments":
		query = `SELECT id,name FROM ticket_departments WHERE active AND admin_ticket_department_allowed($1,id)`
		args = []any{actor}
	case "services":
		customerID, ok := requiredPositiveQueryID(w, r, "customer_id", "客户编号无效")
		if !ok {
			return
		}
		var allowed bool
		if err := s.db.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='customer' AND status='active' AND admin_ticket_customer_allowed($2,id))`, customerID, actor).Scan(&allowed); err != nil {
			s.fail(w, r, err)
			return
		}
		if !allowed {
			writeError(w, http.StatusNotFound, "not_found", "客户不存在或无权查看")
			return
		}
		query = `SELECT s.id,p.name,s.domain,s.status FROM services s JOIN products p ON p.id=s.product_id
WHERE s.user_id=$1 AND ($2='' OR p.name ILIKE '%'||$2||'%' OR s.domain ILIKE '%'||$2||'%' OR s.id::text ILIKE '%'||$2||'%')`
		args = []any{customerID, q}
	case "assignees":
		departmentID, ok := requiredPositiveQueryID(w, r, "department_id", "部门编号无效")
		if !ok {
			return
		}
		var departmentActive bool
		var departmentAllowed bool
		if err := s.db.Pool.QueryRow(r.Context(), `SELECT COALESCE((SELECT active FROM ticket_departments WHERE id=$2),false),admin_ticket_department_allowed($1,$2)`, actor, departmentID).Scan(&departmentActive, &departmentAllowed); err != nil {
			s.fail(w, r, err)
			return
		}
		if !departmentAllowed {
			writeError(w, http.StatusForbidden, "ticket_department_forbidden", "沒有此工單部門的訪問權限")
			return
		}
		if !departmentActive {
			writeError(w, http.StatusConflict, "invalid_ticket_department", "工單部門不可用")
			return
		}
		query = `SELECT u.id,u.display_name AS name FROM users u LEFT JOIN admin_roles ar ON ar.id=u.admin_role_id
WHERE u.status='active' AND (u.role='super_admin' OR (u.role='admin' AND ar.active AND 'tickets.manage'=ANY(ar.permissions)))
AND admin_ticket_department_allowed(u.id,$1)
AND ($2='' OR u.username ILIKE '%'||$2||'%' OR u.display_name ILIKE '%'||$2||'%')`
		args = []any{departmentID, q}
	}
	order := "id DESC"
	if kind == "departments" || kind == "assignees" {
		order = "name,id"
	}
	s.adminListPage(w, r, query, order, limit, offset, args...)
}

type adminTicketCreateInput struct {
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

type adminTicketCreateResult struct {
	ID          int64  `json:"id"`
	TicketNo    string `json:"ticket_no"`
	Version     int64  `json:"version"`
	EmailStatus string `json:"email_status"`
	Replayed    bool   `json:"replayed"`
}

func (s *Server) createAdminTicket(w http.ResponseWriter, r *http.Request) {
	var in adminTicketCreateInput
	if !decodeLimit(w, r, &in, 64<<10) {
		return
	}
	if err := normalizeAdminTicketCreateInput(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_ticket", err.Error())
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(in)
	ccJSON, err := json.Marshal(in.CC)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if err = lockAdminTicketManager(r, tx, actor); err != nil {
			return txResponse{}, err
		}
		if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("admin-ticket-create:%d:%s", actor, key)); err != nil {
			return txResponse{}, err
		}
		var savedHash string
		var savedJSON []byte
		err = tx.QueryRow(r.Context(), `SELECT request_hash,result FROM admin_ticket_creation_receipts WHERE actor_id=$1 AND idempotency_key=$2`, actor, key).Scan(&savedHash, &savedJSON)
		if err == nil {
			if savedHash != hash {
				return txResponse{}, &txHTTPError{http.StatusConflict, "idempotency_conflict", "相同请求键已用于不同内容"}
			}
			var result adminTicketCreateResult
			if err = json.Unmarshal(savedJSON, &result); err != nil {
				return txResponse{}, err
			}
			var accessible bool
			if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM tickets t WHERE t.id=$1 AND admin_ticket_access_allowed($2,t.user_id,t.department_id))`, result.ID, actor).Scan(&accessible); err != nil {
				return txResponse{}, err
			}
			if !accessible {
				return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "工单不存在或无权查看"}
			}
			result.Replayed = true

			return txResponse{http.StatusOK, result}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return txResponse{}, err
		}
		if err = store.LockAdminCustomer(r.Context(), tx, actor, in.CustomerID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "客户不存在或无权选择"}
			}
			return txResponse{}, err
		}
		var customerEmail string
		var customerEmailVerified bool
		err = tx.QueryRow(r.Context(), `SELECT email,email_verified FROM users
WHERE id=$1 AND role='customer' AND status='active' AND admin_ticket_customer_allowed($2,id) FOR SHARE`, in.CustomerID, actor).Scan(&customerEmail, &customerEmailVerified)
		if errors.Is(err, pgx.ErrNoRows) {
			return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "客户不存在或无权选择"}
		}
		if err != nil {
			return txResponse{}, err
		}
		var departmentActive, departmentAllowed bool
		err = tx.QueryRow(r.Context(), `SELECT active,admin_ticket_department_allowed($1,id) FROM ticket_departments WHERE id=$2 FOR SHARE`, actor, in.DepartmentID).Scan(&departmentActive, &departmentAllowed)
		if errors.Is(err, pgx.ErrNoRows) {
			return txResponse{}, &txHTTPError{http.StatusConflict, "invalid_ticket_department", "工单部门不可用"}
		}
		if err != nil {
			return txResponse{}, err
		}
		if !departmentAllowed {
			return txResponse{}, &txHTTPError{http.StatusForbidden, "ticket_department_forbidden", "没有此工单部门的访问权限"}
		}
		if !departmentActive {
			return txResponse{}, &txHTTPError{http.StatusConflict, "invalid_ticket_department", "工单部门不可用"}
		}
		if in.ServiceID != nil {
			var serviceID int64
			if err = tx.QueryRow(r.Context(), `SELECT id FROM services WHERE id=$1 AND user_id=$2 FOR SHARE`, *in.ServiceID, in.CustomerID).Scan(&serviceID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return txResponse{}, err
			}
			if errors.Is(err, pgx.ErrNoRows) {
				return txResponse{}, &txHTTPError{http.StatusConflict, "invalid_ticket_service", "关联服务不属于所选客户"}
			}
		}
		if in.AssigneeID != nil {
			if err = lockAdminTicketAssignee(r, tx, *in.AssigneeID, in.DepartmentID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return txResponse{}, &txHTTPError{http.StatusConflict, "invalid_ticket_assignee", "处理人不可用或无权处理此部门工单"}
				}
				return txResponse{}, err
			}
		}
		var ticket adminTicketCreateResult
		err = tx.QueryRow(r.Context(), `INSERT INTO tickets(ticket_no,user_id,department_id,assignee_id,service_id,subject,body,priority,created_by,cc,send_initial_email)
VALUES('T-'||replace(gen_random_uuid()::text,'-',''),$1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10)
RETURNING id,ticket_no,version`, in.CustomerID, in.DepartmentID, in.AssigneeID, in.ServiceID, in.Subject, in.Body, in.Priority, actor, string(ccJSON), in.SendEmail).Scan(&ticket.ID, &ticket.TicketNo, &ticket.Version)
		if err != nil {
			return txResponse{}, err
		}
		ticket.EmailStatus = "not_requested"
		if in.SendEmail {
			ticket.EmailStatus, err = queueAdminTicketEmail(r, tx, ticket.ID, customerEmail, customerEmailVerified, in.CC, ticket.TicketNo, in.Subject, in.Body)
			if err != nil {
				return txResponse{}, err
			}
			if _, err = tx.Exec(r.Context(), `UPDATE tickets SET initial_email_status=$2 WHERE id=$1`, ticket.ID, ticket.EmailStatus); err != nil {
				return txResponse{}, err
			}
		}
		resultJSON, err := json.Marshal(ticket)
		if err != nil {
			return txResponse{}, err
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO admin_ticket_creation_receipts(actor_id,idempotency_key,customer_id,ticket_id,request_hash,result)
VALUES($1,$2,$3,$4,$5,$6::jsonb)`, actor, key, in.CustomerID, ticket.ID, hash, string(resultJSON)); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, actor, "ticket.admin_created", "ticket", ticket.ID, map[string]any{"customer_id": in.CustomerID, "department_id": in.DepartmentID, "service_id": in.ServiceID, "assignee_id": in.AssigneeID, "priority": in.Priority, "cc_count": len(in.CC), "send_email": in.SendEmail, "email_status": ticket.EmailStatus}); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusCreated, ticket}, nil
	})
}

func lockAdminTicketManager(r *http.Request, tx pgx.Tx, actor int64) error {
	var role string
	var roleID *int64
	if err := tx.QueryRow(r.Context(), `SELECT role,admin_role_id FROM users WHERE id=$1 AND status='active' FOR SHARE`, actor).Scan(&role, &roleID); err != nil {
		return err
	}
	if role == "super_admin" {
		return nil
	}
	if role != "admin" || roleID == nil {
		return &store.Conflict{Code: "permission_denied", Message: "没有管理工单的权限"}
	}
	var allowed bool
	if err := tx.QueryRow(r.Context(), `SELECT active AND 'tickets.manage'=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE`, *roleID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return &store.Conflict{Code: "permission_denied", Message: "没有管理工单的权限"}
	}
	return nil
}

func lockAdminTicketAssignee(r *http.Request, tx pgx.Tx, assigneeID, departmentID int64) error {
	var role string
	var roleID *int64
	if err := tx.QueryRow(r.Context(), `SELECT role,admin_role_id FROM users WHERE id=$1 AND status='active' FOR SHARE`, assigneeID).Scan(&role, &roleID); err != nil {
		return err
	}
	if role == "super_admin" {
		return nil
	}
	if role != "admin" || roleID == nil {
		return pgx.ErrNoRows
	}
	var allowed bool
	if err := tx.QueryRow(r.Context(), `SELECT active AND 'tickets.manage'=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE`, *roleID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return pgx.ErrNoRows
	}
	var inDepartment bool
	if err := tx.QueryRow(r.Context(), `SELECT admin_ticket_department_allowed($1,$2)`, assigneeID, departmentID).Scan(&inDepartment); err != nil {
		return err
	}
	if !inDepartment {
		return pgx.ErrNoRows
	}
	return nil
}

func normalizeAdminTicketCreateInput(in *adminTicketCreateInput) error {
	in.Subject = strings.TrimSpace(in.Subject)
	in.Body = strings.TrimSpace(in.Body)
	if in.CC == nil {
		in.CC = []string{}
	}
	if in.CustomerID < 1 || in.DepartmentID < 1 || in.ServiceID != nil && *in.ServiceID < 1 || in.AssigneeID != nil && *in.AssigneeID < 1 {
		return fmt.Errorf("请选择有效客户和部门")
	}
	if in.Subject == "" || utf8.RuneCountInString(in.Subject) > 240 || in.Body == "" || utf8.RuneCountInString(in.Body) > 10000 {
		return fmt.Errorf("标题长度需为1至240个字符，正文需为1至10000个字符")
	}
	if in.Priority != "low" && in.Priority != "normal" && in.Priority != "high" && in.Priority != "urgent" {
		return fmt.Errorf("工单优先级无效")
	}
	if len(in.CC) > 10 {
		return fmt.Errorf("抄送最多10个有效邮箱")
	}
	seen := make(map[string]struct{}, len(in.CC))
	for i, address := range in.CC {
		address = strings.TrimSpace(address)
		parsed, err := mail.ParseAddress(address)
		if err != nil || parsed.Name != "" || parsed.Address != address || strings.ContainsAny(address, "\r\n") {
			return fmt.Errorf("抄送邮箱格式无效")
		}
		key := strings.ToLower(address)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("抄送邮箱不能重复")
		}
		seen[key] = struct{}{}
		in.CC[i] = address
	}
	return nil
}

func queueAdminTicketEmail(r *http.Request, tx pgx.Tx, ticketID int64, customerEmail string, customerVerified bool, cc []string, ticketNo, subject, body string) (string, error) {
	if !customerVerified || strings.TrimSpace(customerEmail) == "" {
		return "skipped_unverified", nil
	}
	var instance string
	err := tx.QueryRow(r.Context(), `SELECT id FROM channel_instances WHERE category='mail' AND enabled ORDER BY sort_order,id LIMIT 1`).Scan(&instance)
	if errors.Is(err, pgx.ErrNoRows) {
		return "skipped_channel", nil
	}
	if err != nil {
		return "", err
	}
	var templateSubject, templateBody string
	var active bool
	err = tx.QueryRow(r.Context(), `SELECT subject,body,active FROM notification_templates WHERE channel='email' AND kind='ticket_created'`).Scan(&templateSubject, &templateBody, &active)
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
		if _, err = tx.Exec(r.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind,channel_instance_id,ticket_id) VALUES($1,$2,$3,'ticket_created',$4,$5)`, recipient, messageSubject, messageBody, instance, ticketID); err != nil {
			return "", err
		}
	}
	return "queued", nil
}

func parsePositiveID(value string) (int64, error) {
	var id int64
	if _, err := fmt.Sscan(value, &id); err != nil || id < 1 || fmt.Sprint(id) != value {
		return 0, fmt.Errorf("invalid id")
	}
	return id, nil
}

func requiredPositiveQueryID(w http.ResponseWriter, r *http.Request, key, message string) (int64, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	id, err := parsePositiveID(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_ticket_option", message)
		return 0, false
	}
	return id, true
}
