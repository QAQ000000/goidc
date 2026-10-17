package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/fileformat"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

const maxTicketAttachmentBytes = 10 << 20

func isAdmin(r *http.Request) bool {
	id := identityFrom(r)
	if strings.HasPrefix(r.URL.Path, "/api/v1/tickets/") {
		permission := "tickets.manage"
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			permission = "tickets.view"
		}
		return id.can(permission)
	}
	return id.Role == "admin" || id.Role == "super_admin"
}
func (s *Server) tickets(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	filter := r.URL.Query().Get("department_id") != "" && r.URL.Query().Get("department_id") != "all"
	var customerID *int64
	if adminRoute(r) && r.URL.Query().Get("customer_id") != "" {
		id, err := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)
		if err != nil || id < 1 {
			writeError(w, 400, "invalid_customer", "客户筛选无效")
			return
		}
		customerID = &id
	}
	var department *int64
	if filter {
		var ok bool
		department, ok = ticketDepartmentFilter(w, r)
		if !ok {
			return
		}
		if adminRoute(r) && !s.allowTicketDepartment(w, r, s.db.Pool, department) {
			return
		}
	}
	s.list(w, r, `SELECT t.id,t.ticket_no,t.legacy_ticket_no,t.user_id,u.display_name AS customer_name,t.department_id,d.name AS department_name,t.assignee_id,a.display_name AS assignee_name,t.subject,t.status,t.priority,t.version,t.created_at,t.updated_at
 FROM tickets t JOIN users u ON u.id=t.user_id LEFT JOIN ticket_departments d ON d.id=t.department_id LEFT JOIN users a ON a.id=t.assignee_id
 WHERE t.merged_ticket_id IS NULL AND (($1::boolean AND admin_ticket_department_allowed($2,t.department_id)) OR (NOT $1::boolean AND t.user_id=$2 AND coalesce(d.customer_visible,true)))
 AND (NOT $5::boolean OR t.department_id IS NOT DISTINCT FROM $6::bigint)
 AND ($7='' OR t.subject ILIKE '%'||$7||'%' OR t.ticket_no ILIKE '%'||$7||'%' OR t.legacy_ticket_no ILIKE '%'||$7||'%')
 AND ($8::bigint IS NULL OR t.user_id=$8) ORDER BY t.updated_at DESC,t.id DESC LIMIT $3 OFFSET $4`, adminRoute(r), identityFrom(r).ID, limit, offset, filter, department, strings.TrimSpace(r.URL.Query().Get("q")), customerID)
}
func (s *Server) ticketDepartments(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, "SELECT id,name,slug,sort_order FROM ticket_departments WHERE active AND customer_visible ORDER BY sort_order,id")
}

func (s *Server) ticketAssignees(w http.ResponseWriter, r *http.Request) {
	department, ok := ticketDepartmentFilter(w, r)
	if !ok {
		return
	}
	if !s.allowTicketDepartment(w, r, s.db.Pool, department) {
		return
	}
	s.list(w, r, `SELECT u.id,u.username,u.display_name,u.role FROM users u LEFT JOIN admin_roles ar ON ar.id=u.admin_role_id
 WHERE u.status='active' AND admin_ticket_department_allowed(u.id,$1) AND (u.role='super_admin' OR (u.role='admin' AND ar.active AND 'tickets.manage'=ANY(ar.permissions))) ORDER BY u.display_name,u.id`, department)
}
func (s *Server) createTicket(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IdempotencyKey string `json:"idempotency_key,omitempty"`
		Subject        string `json:"subject"`
		Body           string `json:"body"`
		Priority       string `json:"priority"`
		DepartmentID   *int64 `json:"department_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	key, ok := mutationKey(w, r, input.IdempotencyKey, false)
	if !ok {
		return
	}
	input.IdempotencyKey = ""
	input.Subject = strings.TrimSpace(input.Subject)
	input.Body = strings.TrimSpace(input.Body)
	if input.Subject == "" || len(input.Subject) > 240 || input.Body == "" || len(input.Body) > 10000 || (input.Priority != "low" && input.Priority != "normal" && input.Priority != "high" && input.Priority != "urgent") {
		writeError(w, 400, "invalid_ticket", "请检查主题、正文和优先级")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	hash := settingMutationHash(input)
	if s.ticketReplay(w, r, tx, key, "create", hash, 0) {
		return
	}
	if input.DepartmentID != nil {
		var active bool
		if err = tx.QueryRow(r.Context(), "SELECT COALESCE((SELECT active FROM ticket_departments WHERE id=$1), false)", *input.DepartmentID).Scan(&active); err != nil {
			s.fail(w, r, err)
			return
		}
		if !active {
			writeError(w, 409, "invalid_ticket_department", "工单部门不可用")
			return
		}
		var visible bool
		if err = tx.QueryRow(r.Context(), "SELECT customer_visible FROM ticket_departments WHERE id=$1", *input.DepartmentID).Scan(&visible); err != nil {
			s.fail(w, r, err)
			return
		}
		if !visible {
			writeError(w, 409, "invalid_ticket_department", "工单部门不可用")
			return
		}
	}
	var id int64
	err = tx.QueryRow(r.Context(), "INSERT INTO tickets(ticket_no,user_id,department_id,subject,body,priority) VALUES('T-'||replace(gen_random_uuid()::text,'-',''),$1,$2,$3,$4,$5) RETURNING id", identityFrom(r).ID, input.DepartmentID, input.Subject, input.Body, input.Priority).Scan(&id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.created", "ticket", id, map[string]string{}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = s.db.QueueTicketNotification(r.Context(), tx, id, 0, identityFrom(r).ID, "customer_created", s.cfg.PublicOrigin); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveTicketReceipt(r, tx, key, "create", hash, id, map[string]int64{"id": id}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 201, map[string]int64{"id": id})
}
func (s *Server) ticketDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, err := s.db.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	rows, err := tx.Query(r.Context(), "SELECT t.id,t.ticket_no,t.legacy_ticket_no,t.user_id,t.merged_ticket_id,t.service_id,t.legacy_status_label,t.department_id,d.name AS department_name,t.assignee_id,a.display_name AS assignee_name,t.subject,t.body,t.status,t.priority,t.version,t.created_at,t.updated_at,($2::boolean AND admin_ticket_department_allowed($3,t.department_id)) AS admin_access FROM tickets t LEFT JOIN ticket_departments d ON d.id=t.department_id LEFT JOIN users a ON a.id=t.assignee_id WHERE t.id=$1 AND (($2::boolean AND admin_ticket_department_allowed($3,t.department_id)) OR (t.user_id=$3 AND coalesce(d.customer_visible,true)))", id, isAdmin(r), identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ticket, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if ticket["merged_ticket_id"] != nil {
		writeError(w, http.StatusConflict, "ticket_merged", "该工单已合并到其他工单")
		return
	}
	rows, err = tx.Query(r.Context(), "SELECT r.id,r.body,r.created_at,u.display_name AS author_name,u.role AS author_role FROM ticket_replies r JOIN users u ON u.id=r.author_id WHERE ticket_id=$1 ORDER BY r.created_at,r.id", id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	replies, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if replies == nil {
		replies = []map[string]any{}
	}
	rows, err = tx.Query(r.Context(), `SELECT a.id,a.ticket_id,a.reply_id,a.note_id,a.uploader_id,u.display_name AS uploader_name,a.filename,a.mime_type,a.size_bytes,a.sha256,a.created_at
FROM ticket_attachments a JOIN users u ON u.id=a.uploader_id WHERE a.ticket_id=$1 AND (a.note_id IS NULL OR $2::boolean) ORDER BY a.id`, id, ticket["admin_access"] == true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	attachments, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if attachments == nil {
		attachments = []map[string]any{}
	}
	ticket["replies"] = replies
	ticket["attachments"] = attachments
	if ticket["admin_access"] == true {
		rows, err = tx.Query(r.Context(), `SELECT n.id,n.ticket_id,n.author_id,u.display_name AS author_name,n.body,n.created_at
FROM ticket_notes n JOIN users u ON u.id=n.author_id WHERE n.ticket_id=$1 ORDER BY n.id`, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		notes, noteErr := pgx.CollectRows(rows, pgx.RowToMap)
		if noteErr != nil {
			s.fail(w, r, noteErr)
			return
		}
		if notes == nil {
			notes = []map[string]any{}
		}
		ticket["notes"] = notes
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, ticket)
}

func attachmentID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("attachment_id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "invalid_id", "附件编号无效")
		return 0, false
	}
	return id, true
}

func cleanAttachmentFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\x00", ""))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	runes := []rune(name)
	if len(runes) > 255 {
		name = string(runes[:255])
	}
	return name
}

func unsafeAttachmentExtension(name string) bool {
	return fileformat.UnsafeTicketExtension(name)
}

func attachmentMIME(name, declaredHeader string, content []byte) (string, bool) {
	return fileformat.TicketMIME(name, declaredHeader, content)
}

func (s *Server) uploadTicketAttachment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTicketAttachmentBytes+1<<20)
	if err := r.ParseMultipartForm(32 << 10); err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "attachment_too_large", "附件不能超过 10 MiB")
		} else {
			writeError(w, 400, "invalid_multipart", "附件上传格式无效")
		}
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		file, header, err = r.FormFile("attachment")
	}
	if err != nil {
		writeError(w, 400, "attachment_required", "请选择要上传的附件")
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxTicketAttachmentBytes+1))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(content) == 0 {
		writeError(w, 400, "empty_attachment", "附件不能为空")
		return
	}
	if len(content) > maxTicketAttachmentBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "attachment_too_large", "附件不能超过 10 MiB")
		return
	}
	name := cleanAttachmentFilename(header.Filename)
	mimeType, valid := attachmentMIME(name, header.Header.Get("Content-Type"), content)
	if !valid {
		writeError(w, 415, "unsupported_attachment", "附件类型不受支持")
		return
	}
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var ownerID int64
		var status string
		if err = tx.QueryRow(r.Context(), "SELECT user_id,status FROM tickets WHERE id=$1 AND merged_ticket_id IS NULL AND (($2::boolean AND admin_ticket_department_allowed($3,department_id)) OR (user_id=$3 AND coalesce((SELECT customer_visible FROM ticket_departments WHERE id=department_id),true))) FOR UPDATE", id, isAdmin(r), identityFrom(r).ID).Scan(&ownerID, &status); err != nil {
			return txResponse{}, err
		}
		if status == "closed" {
			return txResponse{}, &txHTTPError{409, "ticket_closed", "工单已关闭"}
		}
		var attachmentID int64
		var createdAt any
		if err = tx.QueryRow(r.Context(), `INSERT INTO ticket_attachments(ticket_id,uploader_id,filename,mime_type,size_bytes,sha256,content)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id,created_at`, id, identityFrom(r).ID, name, mimeType, len(content), hash, content).Scan(&attachmentID, &createdAt); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.attachment.uploaded", "ticket_attachment", attachmentID, map[string]any{"ticket_id": id, "filename": name, "mime_type": mimeType, "size_bytes": len(content), "sha256": hash, "customer_id": ownerID}); err != nil {
			return txResponse{}, err
		}

		return txResponse{201, map[string]any{"id": attachmentID, "ticket_id": id, "uploader_id": identityFrom(r).ID, "filename": name, "mime_type": mimeType, "size_bytes": len(content), "sha256": hash, "created_at": createdAt}}, nil
	})
}

func (s *Server) downloadTicketAttachment(w http.ResponseWriter, r *http.Request) {
	ticketID, ok := pathID(w, r)
	if !ok {
		return
	}
	attachment, ok := attachmentID(w, r)
	if !ok {
		return
	}
	var name, mimeType string
	var size int64
	var content []byte
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	err = tx.QueryRow(r.Context(), `SELECT a.filename,a.mime_type,a.size_bytes,a.content FROM ticket_attachments a JOIN tickets t ON t.id=a.ticket_id
WHERE a.id=$1 AND a.ticket_id=$2 AND t.merged_ticket_id IS NULL AND (($3::boolean AND admin_ticket_department_allowed($4,t.department_id)) OR (t.user_id=$4 AND a.note_id IS NULL AND coalesce((SELECT customer_visible FROM ticket_departments WHERE id=t.department_id),true)))`, attachment, ticketID, isAdmin(r), identityFrom(r).ID).Scan(&name, &mimeType, &size, &content)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.attachment.downloaded", "ticket_attachment", attachment, map[string]any{"ticket_id": ticketID, "filename": name, "size_bytes": size}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Length", fmt.Sprint(size))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"attachment\"; filename*=UTF-8''%s", url.PathEscape(name)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (s *Server) deleteTicketAttachment(w http.ResponseWriter, r *http.Request) {
	ticketID, ok := pathID(w, r)
	if !ok {
		return
	}
	attachment, ok := attachmentID(w, r)
	if !ok {
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var ownerID, uploaderID int64
		var name string
		if err = tx.QueryRow(r.Context(), `SELECT t.user_id,a.uploader_id,a.filename FROM ticket_attachments a JOIN tickets t ON t.id=a.ticket_id
WHERE a.id=$1 AND a.ticket_id=$2 AND t.merged_ticket_id IS NULL AND (($3::boolean AND admin_ticket_department_allowed($4,t.department_id)) OR (t.user_id=$4 AND a.note_id IS NULL AND coalesce((SELECT customer_visible FROM ticket_departments WHERE id=t.department_id),true))) FOR UPDATE`, attachment, ticketID, isAdmin(r), identityFrom(r).ID).Scan(&ownerID, &uploaderID, &name); err != nil {
			return txResponse{}, err
		}
		if _, err = tx.Exec(r.Context(), "DELETE FROM ticket_attachments WHERE id=$1", attachment); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.attachment.deleted", "ticket_attachment", attachment, map[string]any{"ticket_id": ticketID, "filename": name, "uploader_id": uploaderID, "customer_id": ownerID}); err != nil {
			return txResponse{}, err
		}

		return txResponse{200, map[string]any{"id": attachment, "deleted": true}}, nil
	})
}

func (s *Server) createTicketDepartment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name            string `json:"name"`
		Slug            string `json:"slug"`
		SortOrder       int    `json:"sort_order"`
		CustomerVisible *bool  `json:"customer_visible"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Name, input.Slug = strings.TrimSpace(input.Name), strings.ToLower(strings.TrimSpace(input.Slug))
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 255 || !validSlug(input.Slug) || input.Slug[0] < 'a' || input.Slug[0] > 'z' || input.SortOrder < -100000 || input.SortOrder > 100000 {
		writeError(w, 400, "invalid_ticket_department", "部门名称或标识无效")
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var id int64
		if err = tx.QueryRow(r.Context(), "INSERT INTO ticket_departments(name,slug,sort_order,customer_visible) VALUES($1,$2,$3,coalesce($4,true)) RETURNING id", input.Name, input.Slug, input.SortOrder, input.CustomerVisible).Scan(&id); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.department.created", "ticket_department", id, input); err != nil {
			return txResponse{}, err
		}

		return txResponse{201, map[string]any{"id": id, "name": input.Name, "slug": input.Slug}}, nil
	})
}

func (s *Server) assignTicket(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		DepartmentID *int64 `json:"department_id"`
		AssigneeID   *int64 `json:"assignee_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var ownerID int64
	if err = tx.QueryRow(r.Context(), "SELECT user_id FROM tickets WHERE id=$1 AND merged_ticket_id IS NULL AND admin_ticket_department_allowed($2,department_id) FOR UPDATE", id, identityFrom(r).ID).Scan(&ownerID); err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.allowTicketDepartment(w, r, tx, input.DepartmentID) {
		return
	}
	if input.DepartmentID != nil {
		var active bool
		if err = tx.QueryRow(r.Context(), "SELECT COALESCE((SELECT active FROM ticket_departments WHERE id=$1), false)", *input.DepartmentID).Scan(&active); err != nil {
			s.fail(w, r, err)
			return
		}
		if !active {
			writeError(w, 409, "invalid_ticket_department", "工单部门不可用")
			return
		}
	}
	if input.AssigneeID != nil {
		var valid bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users u LEFT JOIN admin_roles ar ON ar.id=u.admin_role_id WHERE u.id=$1 AND u.status='active' AND admin_ticket_department_allowed(u.id,$2) AND (u.role='super_admin' OR (u.role='admin' AND ar.active AND 'tickets.manage'=ANY(ar.permissions))))`, *input.AssigneeID, input.DepartmentID).Scan(&valid); err != nil {
			s.fail(w, r, err)
			return
		}
		if !valid {
			writeError(w, 409, "invalid_ticket_assignee", "处理人不可用")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), "UPDATE tickets SET department_id=$2,assignee_id=$3,version=version+1,updated_at=now(),status=CASE WHEN status='open' THEN 'pending' ELSE status END WHERE id=$1", id, input.DepartmentID, input.AssigneeID); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.assigned", "ticket", id, map[string]any{"department_id": input.DepartmentID, "assignee_id": input.AssigneeID, "customer_id": ownerID}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"ticket_id": id, "department_id": input.DepartmentID, "assignee_id": input.AssigneeID})
}

func (s *Server) ticketNotes(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, err := s.db.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var ticketID int64
	if err = tx.QueryRow(r.Context(), "SELECT id FROM tickets WHERE id=$1 AND merged_ticket_id IS NULL AND admin_ticket_department_allowed($2,department_id)", id, identityFrom(r).ID).Scan(&ticketID); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT n.id,n.ticket_id,n.author_id,u.display_name AS author_name,n.body,n.created_at FROM ticket_notes n JOIN users u ON u.id=n.author_id WHERE n.ticket_id=$1 ORDER BY n.id`, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	notes, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	if notes == nil {
		notes = []map[string]any{}
	}
	write(w, 200, notes)
}

func (s *Server) createTicketNote(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Body string `json:"body"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Body = strings.TrimSpace(input.Body)
	if input.Body == "" || len(input.Body) > 10000 {
		writeError(w, http.StatusBadRequest, "invalid_ticket_note", "备注内容无效")
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var ownerID int64
		if err = tx.QueryRow(r.Context(), "SELECT user_id FROM tickets WHERE id=$1 AND merged_ticket_id IS NULL AND admin_ticket_department_allowed($2,department_id) FOR UPDATE", id, identityFrom(r).ID).Scan(&ownerID); err != nil {
			return txResponse{}, err
		}
		var noteID int64
		var createdAt any
		if err = tx.QueryRow(r.Context(), "INSERT INTO ticket_notes(ticket_id,author_id,body) VALUES($1,$2,$3) RETURNING id,created_at", id, identityFrom(r).ID, input.Body).Scan(&noteID, &createdAt); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.note.created", "ticket_note", noteID, map[string]any{"ticket_id": id, "customer_id": ownerID}); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusCreated, map[string]any{"id": noteID, "ticket_id": id, "author_id": identityFrom(r).ID, "body": input.Body, "created_at": createdAt}}, nil
	})
}

func (s *Server) deleteTicketNote(w http.ResponseWriter, r *http.Request) {
	ticketID, ok := pathID(w, r)
	if !ok {
		return
	}
	noteID, ok := noteID(w, r)
	if !ok {
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var ownerID int64
		if err = tx.QueryRow(r.Context(), `SELECT t.user_id FROM ticket_notes n JOIN tickets t ON t.id=n.ticket_id
WHERE n.id=$1 AND n.ticket_id=$2 AND t.merged_ticket_id IS NULL AND admin_ticket_department_allowed($3,t.department_id) FOR UPDATE`, noteID, ticketID, identityFrom(r).ID).Scan(&ownerID); err != nil {
			return txResponse{}, err
		}
		if _, err = tx.Exec(r.Context(), "DELETE FROM ticket_notes WHERE id=$1", noteID); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.note.deleted", "ticket_note", noteID, map[string]any{"ticket_id": ticketID, "customer_id": ownerID}); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, map[string]any{"id": noteID, "deleted": true}}, nil
	})
}

func noteID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("note_id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid_id", "备注编号无效")
		return 0, false
	}
	return id, true
}

func (s *Server) receiveTicket(w http.ResponseWriter, r *http.Request) {
	tid, ok := pathID(w, r)
	if !ok {
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var ownerID int64
		var assigneeID *int64
		var version int64
		if err = tx.QueryRow(r.Context(), "SELECT user_id,assignee_id,version FROM tickets WHERE id=$1 AND merged_ticket_id IS NULL AND admin_ticket_department_allowed($2,department_id) FOR UPDATE", tid, identityFrom(r).ID).Scan(&ownerID, &assigneeID, &version); err != nil {
			return txResponse{}, err
		}
		if assigneeID != nil {
			return txResponse{}, &txHTTPError{http.StatusConflict, "ticket_already_claimed", "工单已被其他管理员接单"}
		}
		if _, err = tx.Exec(r.Context(), "UPDATE tickets SET assignee_id=$2,status=CASE WHEN status='open' THEN 'pending' ELSE status END,version=version+1,updated_at=now() WHERE id=$1", tid, identityFrom(r).ID); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.received", "ticket", tid, map[string]any{"customer_id": ownerID}); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, map[string]any{"ticket_id": tid, "assignee_id": identityFrom(r).ID, "version": version + 1}}, nil
	})
}

func (s *Server) transferTicket(w http.ResponseWriter, r *http.Request) {
	tid, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		DepartmentID *int64 `json:"department_id"`
		AssigneeID   *int64 `json:"assignee_id"`
		Reason       string `json:"reason"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.DepartmentID == nil && input.AssigneeID == nil {
		writeError(w, http.StatusBadRequest, "invalid_transfer", "至少指定部门或处理人")
		return
	}
	if len(input.Reason) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_transfer", "转移备注过长")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var ownerID int64
	var previousDepartment, previousAssignee *int64
	var status string
	if err = tx.QueryRow(r.Context(), "SELECT user_id,department_id,assignee_id,status FROM tickets WHERE id=$1 AND merged_ticket_id IS NULL AND admin_ticket_department_allowed($2,department_id) FOR UPDATE", tid, identityFrom(r).ID).Scan(&ownerID, &previousDepartment, &previousAssignee, &status); err != nil {
		s.fail(w, r, err)
		return
	}
	departmentID, assigneeID := previousDepartment, previousAssignee
	if input.DepartmentID != nil && !s.allowTicketDepartment(w, r, tx, input.DepartmentID) {
		return
	}
	if input.DepartmentID != nil {
		var active bool
		if err = tx.QueryRow(r.Context(), "SELECT COALESCE((SELECT active FROM ticket_departments WHERE id=$1),false)", *input.DepartmentID).Scan(&active); err != nil {
			s.fail(w, r, err)
			return
		}
		if !active {
			writeError(w, http.StatusConflict, "invalid_ticket_department", "工单部门不可用")
			return
		}
		departmentID = input.DepartmentID
		if input.AssigneeID == nil {
			assigneeID = nil
		}
	}
	if input.AssigneeID != nil {
		var valid bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users u LEFT JOIN admin_roles ar ON ar.id=u.admin_role_id WHERE u.id=$1 AND u.status='active' AND admin_ticket_department_allowed(u.id,$2) AND (u.role='super_admin' OR (u.role='admin' AND ar.active AND 'tickets.manage'=ANY(ar.permissions))))`, *input.AssigneeID, departmentID).Scan(&valid); err != nil {
			s.fail(w, r, err)
			return
		}
		if !valid {
			writeError(w, http.StatusConflict, "invalid_ticket_assignee", "处理人不可用")
			return
		}
		assigneeID = input.AssigneeID
	}
	if _, err = tx.Exec(r.Context(), `UPDATE tickets SET department_id=$2,assignee_id=$3,status=CASE WHEN $3::bigint IS NULL AND status='pending' THEN 'open' ELSE status END,version=version+1,updated_at=now() WHERE id=$1`, tid, departmentID, assigneeID); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO ticket_transfer_logs(ticket_id,actor_id,previous_department_id,department_id,previous_assignee_id,assignee_id,reason)
VALUES($1,$2,$3,$4,$5,$6,$7)`, tid, identityFrom(r).ID, previousDepartment, departmentID, previousAssignee, assigneeID, input.Reason); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.transferred", "ticket", tid, map[string]any{"department_id": departmentID, "assignee_id": assigneeID, "previous_department_id": previousDepartment, "previous_assignee_id": previousAssignee, "reason": input.Reason, "status": status, "customer_id": ownerID}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"ticket_id": tid, "department_id": departmentID, "assignee_id": assigneeID})
}

func (s *Server) mergeTickets(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TicketIDs []int64 `json:"ticket_ids"`
	}
	if !decode(w, r, &input) {
		return
	}
	seen := make(map[int64]bool, len(input.TicketIDs))
	ids := make([]int64, 0, len(input.TicketIDs))
	for _, id := range input.TicketIDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) < 2 || len(ids) > 20 {
		writeError(w, http.StatusBadRequest, "invalid_merge", "请选择 2 到 20 个工单")
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		rows, err := tx.Query(r.Context(), "SELECT id,user_id,merged_ticket_id,department_id FROM tickets WHERE id=ANY($1::bigint[]) AND admin_ticket_department_allowed($2,department_id) ORDER BY id FOR UPDATE", ids, identityFrom(r).ID)
		if err != nil {
			return txResponse{}, err
		}
		tickets, err := pgx.CollectRows(rows, pgx.RowToMap)
		if err != nil {
			return txResponse{}, err
		}
		if len(tickets) != len(ids) {
			return txResponse{}, &txHTTPError{http.StatusNotFound, "ticket_not_found", "部分工单不存在"}
		}
		canonical := ids[0]
		var ownerID int64
		departments := map[string]any{}
		for index, ticket := range tickets {
			departments[fmt.Sprint(ticket["id"])] = ticket["department_id"]
			userID, ok := ticket["user_id"].(int64)
			if !ok {
				return txResponse{}, &txHTTPError{http.StatusInternalServerError, "internal_error", "工单数据无效"}
			}
			if index == 0 {
				ownerID = userID
			} else if userID != ownerID {
				return txResponse{}, &txHTTPError{http.StatusConflict, "merge_owner_mismatch", "只能合并同一客户的工单"}
			}
			if ticket["merged_ticket_id"] != nil {
				return txResponse{}, &txHTTPError{http.StatusConflict, "ticket_already_merged", "所选工单已经合并"}
			}
		}
		if _, err = tx.Exec(r.Context(), "UPDATE ticket_replies SET ticket_id=$1 WHERE ticket_id=ANY($2::bigint[]) AND ticket_id<>$1", canonical, ids); err != nil {
			return txResponse{}, err
		}
		if _, err = tx.Exec(r.Context(), "UPDATE ticket_notes SET ticket_id=$1 WHERE ticket_id=ANY($2::bigint[]) AND ticket_id<>$1", canonical, ids); err != nil {
			return txResponse{}, err
		}
		if _, err = tx.Exec(r.Context(), "UPDATE ticket_attachments SET ticket_id=$1 WHERE ticket_id=ANY($2::bigint[]) AND ticket_id<>$1", canonical, ids); err != nil {
			return txResponse{}, err
		}
		if _, err = tx.Exec(r.Context(), "UPDATE tickets SET merged_ticket_id=$1,status='closed',version=version+1,updated_at=now() WHERE id=ANY($2::bigint[]) AND id<>$1", canonical, ids); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "ticket.merged", "ticket", canonical, map[string]any{"ticket_ids": ids, "customer_id": ownerID, "source_departments": departments}); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, map[string]any{"canonical_ticket_id": canonical, "merged_ticket_ids": ids[1:]}}, nil
	})
}

func (s *Server) replyTicket(w http.ResponseWriter, r *http.Request) { s.changeTicket(w, r, false) }
func (s *Server) closeTicket(w http.ResponseWriter, r *http.Request) { s.changeTicket(w, r, true) }
func (s *Server) changeTicket(w http.ResponseWriter, r *http.Request, close bool) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		IdempotencyKey string `json:"idempotency_key,omitempty"`
		Version        int64  `json:"version"`
		Body           string `json:"body,omitempty"`
	}
	if !decode(w, r, &input) {
		return
	}
	key, ok := mutationKey(w, r, input.IdempotencyKey, false)
	if !ok {
		return
	}
	input.IdempotencyKey = ""
	input.Body = strings.TrimSpace(input.Body)
	if input.Version < 1 || (!close && (input.Body == "" || len(input.Body) > 10000)) {
		writeError(w, 400, "invalid_reply", "回复内容或工单版本无效")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var version int64
	var status string
	var staffAccess bool
	err = tx.QueryRow(r.Context(), "SELECT version,status,($2::boolean AND admin_ticket_department_allowed($3,department_id)) FROM tickets WHERE id=$1 AND merged_ticket_id IS NULL AND (($2::boolean AND admin_ticket_department_allowed($3,department_id)) OR (user_id=$3 AND coalesce((SELECT customer_visible FROM ticket_departments WHERE id=department_id),true))) FOR UPDATE", id, isAdmin(r), identityFrom(r).ID).Scan(&version, &status, &staffAccess)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	operation := fmt.Sprintf("reply:%d", id)
	hash := settingMutationHash(input)
	if !close && s.ticketReplay(w, r, tx, key, operation, hash, id) {
		return
	}
	var replyID int64
	if version != input.Version {
		writeError(w, 409, "stale_ticket", "工单已更新，请刷新后操作")
		return
	}
	if status == "closed" {
		writeError(w, 409, "ticket_closed", "工单已关闭")
		return
	}
	action := "ticket.replied"
	newStatus := "open"
	if close {
		action = "ticket.closed"
		newStatus = "closed"
	} else {
		if staffAccess {
			newStatus = "pending"
		}
		if err = tx.QueryRow(r.Context(), "INSERT INTO ticket_replies(ticket_id,author_id,body) VALUES($1,$2,$3) RETURNING id", id, identityFrom(r).ID, input.Body).Scan(&replyID); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if _, err = tx.Exec(r.Context(), "UPDATE tickets SET status=$1,version=version+1,updated_at=now() WHERE id=$2", newStatus, id); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, action, "ticket", id, map[string]string{"status": newStatus}); err != nil {
		s.fail(w, r, err)
		return
	}
	if !close {
		kind := "customer_replied"
		if staffAccess {
			kind = "staff_replied"
		}
		if err = s.db.QueueTicketNotification(r.Context(), tx, id, replyID, identityFrom(r).ID, kind, s.cfg.PublicOrigin); err != nil {
			s.fail(w, r, err)
			return
		}
		if err = saveTicketReceipt(r, tx, key, operation, hash, id, map[string]any{"version": version + 1, "status": newStatus, "reply_id": replyID}); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"version": version + 1, "status": newStatus}
	if !close {
		result["reply_id"] = replyID
	}
	write(w, 200, result)
}
