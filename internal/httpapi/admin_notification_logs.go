package httpapi

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// adminNotificationLogQuery contains the common filters used by the legacy
// SMS and email log screens while returning the stable items/total contract.
type adminLogFilters struct {
	where []string
	args  []any
}

func (f *adminLogFilters) add(value any, clause string) {
	f.args = append(f.args, value)
	f.where = append(f.where, fmt.Sprintf(clause, len(f.args)))
}

func parseLogDate(value, label string, end bool) (time.Time, error) {
	d, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s日期无效", label)
	}
	if end {
		d = d.AddDate(0, 0, 1)
	}
	return d, nil
}

func addLogDateFilters(r *http.Request, f *adminLogFilters, column string) error {
	for _, key := range []string{"from", "to"} {
		value := strings.TrimSpace(r.URL.Query().Get(key))
		if value == "" {
			continue
		}
		d, err := parseLogDate(value, key, key == "to")
		if err != nil {
			return err
		}
		op := ">="
		if key == "to" {
			op = "<"
		}
		f.add(d, column+" "+op+" $%d")
	}
	return nil
}

func logPageArgs(r *http.Request) (int, int) { return pagination(r) }

func (s *Server) adminSMSLog(w http.ResponseWriter, r *http.Request) {
	limit, offset := logPageArgs(r)
	f := adminLogFilters{where: []string{"TRUE"}}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		if !validOutboxStatus(status) {
			writeError(w, http.StatusBadRequest, "invalid_sms_status", "短信状态无效")
			return
		}
		f.add(status, "j.status=$%d")
	}
	if kind := strings.TrimSpace(r.URL.Query().Get("kind")); kind != "" {
		f.add(kind, "j.kind=$%d")
	}
	if recipient := strings.TrimSpace(r.URL.Query().Get("phone")); recipient != "" {
		f.add("%"+recipient+"%", "j.recipient ILIKE $%d")
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		f.add("%"+q+"%", "(j.recipient ILIKE $%d OR j.kind ILIKE $%d OR coalesce(e.display_name,'') ILIKE $%d OR coalesce(e.username,'') ILIKE $%d OR coalesce(e.email,'') ILIKE $%d)")
		// Repeat the same parameter number so one search value covers all fields.
		idx := len(f.args)
		f.where[len(f.where)-1] = fmt.Sprintf("(j.recipient ILIKE $%d OR j.kind ILIKE $%d OR coalesce(e.display_name,'') ILIKE $%d OR coalesce(e.username,'') ILIKE $%d OR coalesce(e.email,'') ILIKE $%d)", idx, idx, idx, idx, idx)
	}
	if err := addLogDateFilters(r, &f, "j.created_at"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_log_date", err.Error())
		return
	}
	if username := strings.TrimSpace(r.URL.Query().Get("username")); username != "" {
		f.add("%"+username+"%", "(e.display_name ILIKE $%d OR e.username ILIKE $%d OR e.email ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = fmt.Sprintf("(e.display_name ILIKE $%d OR e.username ILIKE $%d OR e.email ILIKE $%d)", idx, idx, idx)
	}
	if !addTicketNotificationLogFilters(w, r, &f, "sms") {
		return
	}
	where := strings.Join(f.where, " AND ")
	base := ` FROM sms_outbox j LEFT JOIN LATERAL (
 SELECT n.user_id FROM notification_delivery_events n WHERE n.sms_outbox_id=j.id ORDER BY n.id LIMIT 1
) ev ON true LEFT JOIN LATERAL (
 SELECT te.ticket_id,te.event_key,te.kind AS event_kind,td.recipient_kind,td.customer_id,t.department_id
 FROM ticket_notification_deliveries td JOIN ticket_notification_events te ON te.id=td.event_id JOIN tickets t ON t.id=te.ticket_id
 WHERE td.sms_outbox_id=j.id ORDER BY td.id LIMIT 1
 ) tv ON true LEFT JOIN users e ON e.id=coalesce(ev.user_id,tv.customer_id) WHERE ` + where
	s.writeListPage(w, r, "SELECT count(*)"+base,
		`SELECT j.id,j.created_at,j.recipient,j.recipient AS phone,j.kind,j.status,j.attempts,j.max_attempts,j.last_error,j.last_error AS failure_reason,
 j.sent_at,j.dead_letter_at,e.display_name AS username,NULL::inet AS remote_ip,NULL::inet AS ip,
 tv.ticket_id,tv.event_key,tv.event_kind,tv.recipient_kind,(j.status='sent') AS success,
 CASE WHEN j.body_encrypted<>''::bytea THEN '[已加密]' ELSE '' END AS content`+base+
			" ORDER BY j.id DESC", f.args, limit, offset)
}

func (s *Server) adminEmailLog(w http.ResponseWriter, r *http.Request) {
	limit, offset := logPageArgs(r)
	f := adminLogFilters{where: []string{"TRUE"}}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		if !validOutboxStatus(status) {
			writeError(w, http.StatusBadRequest, "invalid_email_status", "邮件状态无效")
			return
		}
		f.add(status, "j.status=$%d")
	}
	if recipient := strings.TrimSpace(r.URL.Query().Get("recipient")); recipient != "" {
		f.add("%"+recipient+"%", "j.recipient ILIKE $%d")
	}
	if subject := strings.TrimSpace(r.URL.Query().Get("subject")); subject != "" {
		f.add("%"+subject+"%", "j.subject ILIKE $%d")
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		f.add("%"+q+"%", "(j.recipient ILIKE $%d OR j.subject ILIKE $%d OR j.kind ILIKE $%d OR coalesce(e.display_name,'') ILIKE $%d OR coalesce(e.username,'') ILIKE $%d OR coalesce(e.email,'') ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = fmt.Sprintf("(j.recipient ILIKE $%d OR j.subject ILIKE $%d OR j.kind ILIKE $%d OR coalesce(e.display_name,'') ILIKE $%d OR coalesce(e.username,'') ILIKE $%d OR coalesce(e.email,'') ILIKE $%d)", idx, idx, idx, idx, idx, idx)
	}
	if username := strings.TrimSpace(r.URL.Query().Get("username")); username != "" {
		f.add("%"+username+"%", "(e.display_name ILIKE $%d OR e.username ILIKE $%d OR e.email ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = fmt.Sprintf("(e.display_name ILIKE $%d OR e.username ILIKE $%d OR e.email ILIKE $%d)", idx, idx, idx)
	}
	if err := addLogDateFilters(r, &f, "j.created_at"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_log_date", err.Error())
		return
	}
	if !addTicketNotificationLogFilters(w, r, &f, "email") {
		return
	}
	where := strings.Join(f.where, " AND ")
	base := ` FROM email_outbox j LEFT JOIN LATERAL (
 SELECT n.user_id FROM notification_delivery_events n WHERE n.email_outbox_id=j.id ORDER BY n.id LIMIT 1
) ev ON true LEFT JOIN LATERAL (
 SELECT te.ticket_id,te.event_key,te.kind AS event_kind,td.recipient_kind,td.customer_id,t.department_id
 FROM ticket_notification_deliveries td JOIN ticket_notification_events te ON te.id=td.event_id JOIN tickets t ON t.id=te.ticket_id
 WHERE td.email_outbox_id=j.id
 UNION ALL SELECT t.id,NULL::text,NULL::text,NULL::text,t.user_id,t.department_id FROM tickets t WHERE t.id=j.ticket_id LIMIT 1
 ) tv ON true LEFT JOIN users e ON e.id=coalesce(ev.user_id,tv.customer_id) WHERE ` + where
	s.writeListPage(w, r, "SELECT count(*)"+base,
		`SELECT j.id,j.created_at,j.subject,j.recipient,j.status,j.attempts,j.max_attempts,j.last_error,j.last_error AS failure_reason,
 j.sent_at,j.dead_letter_at,e.display_name AS username,NULL::inet AS remote_ip,NULL::inet AS ip,
 tv.ticket_id,tv.event_key,tv.event_kind,tv.recipient_kind,(j.status='sent') AS success`+base+
			" ORDER BY j.id DESC", f.args, limit, offset)
}

func (s *Server) adminInboxLog(w http.ResponseWriter, r *http.Request) {
	limit, offset := logPageArgs(r)
	f := adminLogFilters{where: []string{"admin_customer_allowed($1,m.user_id)"}, args: []any{identityFrom(r).ID}}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		if status != "sent" && status != "read" && status != "failed" {
			writeError(w, http.StatusBadRequest, "invalid_inbox_status", "站内信状态无效")
			return
		}
		f.add(status, "m.status=$%d")
	}
	if subject := strings.TrimSpace(r.URL.Query().Get("subject")); subject != "" {
		f.add("%"+subject+"%", "m.subject ILIKE $%d")
	}
	if username := strings.TrimSpace(r.URL.Query().Get("username")); username != "" {
		f.add("%"+username+"%", "(u.display_name ILIKE $%d OR u.username ILIKE $%d OR u.email ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = fmt.Sprintf("(u.display_name ILIKE $%d OR u.username ILIKE $%d OR u.email ILIKE $%d)", idx, idx, idx)
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		f.add("%"+q+"%", "(m.subject ILIKE $%d OR m.body ILIKE $%d OR u.display_name ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = fmt.Sprintf("(m.subject ILIKE $%d OR m.body ILIKE $%d OR u.display_name ILIKE $%d)", idx, idx, idx)
	}
	if err := addLogDateFilters(r, &f, "m.created_at"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_log_date", err.Error())
		return
	}
	where := strings.Join(f.where, " AND ")
	base := " FROM admin_inbox_messages m JOIN users u ON u.id=m.user_id WHERE " + where
	s.writeListPage(w, r, "SELECT count(*)"+base,
		`SELECT m.id,m.created_at,m.subject,m.status,m.user_id,u.display_name AS username,m.read_at`+base+
			" ORDER BY m.id DESC", f.args, limit, offset)
}

func (s *Server) adminAPILog(w http.ResponseWriter, r *http.Request) {
	limit, offset := logPageArgs(r)
	f := adminLogFilters{where: []string{"u.role IN ('admin','super_admin')"}}
	if method := strings.TrimSpace(r.URL.Query().Get("method")); method != "" {
		method = strings.ToUpper(method)
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		default:
			writeError(w, http.StatusBadRequest, "invalid_api_method", "API方法无效")
			return
		}
		f.add(method, "a.method=$%d")
	}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		code, err := strconv.Atoi(status)
		if err != nil || code < 100 || code > 599 {
			writeError(w, http.StatusBadRequest, "invalid_api_status", "API状态码无效")
			return
		}
		f.add(code, "a.status_code=$%d")
	}
	if ip := strings.TrimSpace(r.URL.Query().Get("ip")); ip != "" {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			writeError(w, http.StatusBadRequest, "invalid_api_ip", "IP地址无效")
			return
		}
		f.add(parsed, "a.remote_ip=$%d")
	}
	if username := strings.TrimSpace(r.URL.Query().Get("username")); username != "" {
		f.add("%"+username+"%", "(u.display_name ILIKE $%d OR u.username ILIKE $%d OR u.email ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = fmt.Sprintf("(u.display_name ILIKE $%d OR u.username ILIKE $%d OR u.email ILIKE $%d)", idx, idx, idx)
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		f.add("%"+q+"%", "(a.path ILIKE $%d OR a.method ILIKE $%d OR coalesce(u.display_name,'') ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = fmt.Sprintf("(a.path ILIKE $%d OR a.method ILIKE $%d OR coalesce(u.display_name,'') ILIKE $%d)", idx, idx, idx)
	}
	if err := addLogDateFilters(r, &f, "a.created_at"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_log_date", err.Error())
		return
	}
	where := strings.Join(f.where, " AND ")
	base := " FROM admin_api_logs a LEFT JOIN users u ON u.id=a.actor_id WHERE " + where
	s.writeListPage(w, r, "SELECT count(*)"+base,
		`SELECT a.id,a.created_at,a.method,a.path,a.path AS description,a.status_code,a.duration_ms,a.remote_ip,a.remote_ip AS ip,
 a.actor_id,u.display_name AS username`+base+
			" ORDER BY a.id DESC", f.args, limit, offset)
}

func validOutboxStatus(value string) bool {
	switch value {
	case "pending", "sending", "sent", "expired", "dead_letter":
		return true
	default:
		return false
	}
}

// Ticket messages retain the live ticket department boundary even for log viewers.
func addTicketNotificationLogFilters(w http.ResponseWriter, r *http.Request, f *adminLogFilters, channel string) bool {
	f.args = append(f.args, identityFrom(r).ID)
	f.where = append(f.where, ticketNotificationOutboxSQL(channel, "j.id", "$"+strconv.Itoa(len(f.args)), "tickets.view"))
	if value := strings.TrimSpace(r.URL.Query().Get("ticket_id")); value != "" {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id < 1 {
			writeError(w, 400, "invalid_ticket", "工单编号无效")
			return false
		}
		f.add(id, "tv.ticket_id=$%d")
	}
	return true
}
