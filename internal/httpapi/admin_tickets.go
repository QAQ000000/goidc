package httpapi

import (
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/store"
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
	customerID, err := s.db.AdminCustomerFilter(r.Context(), identityFrom(r).ID, r.URL.Query())
	if accountBusinessFailure(s, w, r, err) {
		return
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
	result, err := s.db.AdminTicketAccountPage(r.Context(), identityFrom(r).ID, status, priority, q, customerID, departmentFilter, departmentID, limit, offset)
	if err != nil {
		s.failTicketAccount(w, r, err)
		return
	}
	write(w, 200, result)
}

func (s *Server) adminTicketCreateOptions(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit, offset := pagination(r)
	if kind != "" && kind != "customers" && kind != "departments" && kind != "services" && kind != "assignees" {
		writeError(w, 400, "invalid_ticket_options", "工单选项类型无效")
		return
	}
	if len(q) > 120 {
		writeError(w, 400, "invalid_query", "搜索条件过长")
		return
	}
	if kind == "customers" {
		result, err := s.db.AdminCustomerCreateOptions(r.Context(), identityFrom(r).ID, r.URL.Query(), q, "ticket", limit, offset)
		if accountBusinessFailure(s, w, r, err) {
			return
		}
		write(w, 200, result)
		return
	}
	var relation int64
	if kind == "services" || kind == "assignees" {
		key, message := "customer_id", "客户编号无效"
		if kind == "assignees" {
			key, message = "department_id", "部门编号无效"
		}
		var ok bool
		relation, ok = requiredPositiveQueryID(w, r, key, message)
		if !ok {
			return
		}
	}
	result, err := s.db.AdminTicketAccountOptions(r.Context(), identityFrom(r).ID, kind, q, relation, limit, offset)
	if err != nil {
		s.failTicketAccount(w, r, err)
		return
	}
	if kind == "" {
		result["priorities"] = ticketPriorities
	}
	write(w, 200, result)
}

type adminTicketCreateInput = store.AdminTicketCreateInput
type adminTicketCreateResult = store.AdminTicketCreateResult

func (s *Server) createAdminTicket(w http.ResponseWriter, r *http.Request) {
	var in adminTicketCreateInput
	if !decodeLimit(w, r, &in, 64<<10) {
		return
	}
	if err := normalizeAdminTicketCreateInput(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_ticket", err.Error())
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	result, status, err := s.db.CreateAdminTicketAccount(r.Context(), identityFrom(r).ID, key, settingMutationHash(in), in)
	if err != nil {
		s.failTicketAccount(w, r, err)
		return
	}
	write(w, status, result)
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
