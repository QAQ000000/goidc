package store

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type accountLogFilters struct {
	where []string
	args  []any
}

func (f *accountLogFilters) add(value any, clause string) {
	f.args = append(f.args, value)
	f.where = append(f.where, fmt.Sprintf(clause, len(f.args)))
}
func accountLogDateFilters(filters url.Values, f *accountLogFilters, column string) error {
	dates := make(map[string]time.Time, 2)
	for _, key := range []string{"from", "to"} {
		values, present := filters[key]
		if !present {
			continue
		}
		if len(values) != 1 {
			return fmt.Errorf("%s日期不能重复", key)
		}
		raw := strings.TrimSpace(values[0])
		if raw == "" {
			continue
		}
		d, err := time.ParseInLocation("2006-01-02", raw, notificationShanghai)
		if err != nil {
			return fmt.Errorf("%s日期无效", key)
		}
		dates[key] = d
	}
	if from, ok := dates["from"]; ok {
		if to, present := dates["to"]; present && from.After(to) {
			return fmt.Errorf("开始日期不能晚于结束日期")
		}
	}
	for _, key := range []string{"from", "to"} {
		d, present := dates[key]
		if !present {
			continue
		}
		op := ">="
		if key == "to" {
			op = "<"
			d = d.AddDate(0, 0, 1)
		}
		f.add(d, column+" "+op+" $%d")
	}
	return nil
}
func accountOutboxStatus(value string) bool {
	switch value {
	case "pending", "sending", "sent", "expired", "dead_letter":
		return true
	}
	return false
}
func accountTicketLogFilters(filters url.Values, f *accountLogFilters, channel string, actor int64) error {
	f.args = append(f.args, actor)
	f.where = append(f.where, TicketNotificationOutboxPredicate(channel, "j.id", "$"+strconv.Itoa(len(f.args)), "tickets.view"))
	if value := strings.TrimSpace(filters.Get("ticket_id")); value != "" {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id < 1 {
			return &AccountBusinessError{400, "invalid_ticket", "工单编号无效"}
		}
		f.add(id, "tv.ticket_id=$%d")
	}
	return nil
}
func accountLogPage(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}, countSQL, selectSQL string, args []any, limit, offset int) (map[string]any, error) {
	var total int64
	if err := q.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, err
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	items, err := collectMaps(q.Query(ctx, selectSQL+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), pageArgs...))
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "total": total, "limit": limit, "offset": offset}, nil
}
func (s *Store) AccountSMSLog(ctx context.Context, filters url.Values, actor int64, limit, offset int) (map[string]any, error) {
	customer, err := s.accountLogCustomer(ctx, filters, actor)
	if err != nil {
		return nil, err
	}
	f := accountLogFilters{where: []string{"TRUE"}}
	if customer != nil {
		f.args = append(f.args, *customer, actor)
		f.where = append(f.where, "admin_customer_allowed($2,$1)", accountOutboxCustomerSQL("sms", "$1"))
	}
	if status := strings.TrimSpace(filters.Get("status")); status != "" {
		if !accountOutboxStatus(status) {
			return nil, &AccountBusinessError{400, "invalid_sms_status", "短信状态无效"}
		}
		f.add(status, "j.status=$%d")
	}
	if kind := strings.TrimSpace(filters.Get("kind")); kind != "" {
		f.add(kind, "j.kind=$%d")
	}
	if recipient := strings.TrimSpace(filters.Get("phone")); recipient != "" {
		f.add("%"+recipient+"%", "j.recipient ILIKE $%d")
	}
	if q := strings.TrimSpace(filters.Get("q")); q != "" {
		f.add("%"+q+"%", "(j.recipient ILIKE $%d OR j.kind ILIKE $%d OR coalesce(e.display_name,'') ILIKE $%d OR coalesce(e.username,'') ILIKE $%d OR coalesce(e.email,'') ILIKE $%d)")
		// Repeat the same parameter number so one search value covers all fields.
		idx := len(f.args)
		f.where[len(f.where)-1] = fmt.Sprintf("(j.recipient ILIKE $%d OR j.kind ILIKE $%d OR coalesce(e.display_name,'') ILIKE $%d OR coalesce(e.username,'') ILIKE $%d OR coalesce(e.email,'') ILIKE $%d)", idx, idx, idx, idx, idx)
	}
	if err := accountLogDateFilters(filters, &f, "j.created_at"); err != nil {
		return nil, &AccountBusinessError{400, "invalid_log_date", err.Error()}
	}
	if username := strings.TrimSpace(filters.Get("username")); username != "" {
		f.add("%"+username+"%", "(e.display_name ILIKE $%d OR e.username ILIKE $%d OR e.email ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = fmt.Sprintf("(e.display_name ILIKE $%d OR e.username ILIKE $%d OR e.email ILIKE $%d)", idx, idx, idx)
	}
	if err := accountTicketLogFilters(filters, &f, "sms", actor); err != nil {
		return nil, err
	}
	where := strings.Join(f.where, " AND ")
	base := ` FROM sms_outbox j LEFT JOIN LATERAL (
 SELECT n.user_id FROM notification_delivery_events n WHERE n.sms_outbox_id=j.id ORDER BY n.id LIMIT 1
) ev ON true LEFT JOIN LATERAL (
 SELECT te.ticket_id,te.event_key,te.kind AS event_kind,td.recipient_kind,td.customer_id,t.department_id
 FROM ticket_notification_deliveries td JOIN ticket_notification_events te ON te.id=td.event_id JOIN tickets t ON t.id=te.ticket_id
 WHERE td.sms_outbox_id=j.id ORDER BY td.id LIMIT 1
 ) tv ON true LEFT JOIN account_identities e ON e.id=coalesce(ev.user_id,tv.customer_id) WHERE ` + where
	return accountLogPage(ctx, s.Pool, "SELECT count(*)"+base,
		`SELECT j.id,j.created_at,j.recipient,j.recipient AS phone,j.kind,j.status,j.attempts,j.max_attempts,j.last_error,j.last_error AS failure_reason,
 j.sent_at,j.dead_letter_at,e.display_name AS username,NULL::inet AS remote_ip,NULL::inet AS ip,
 tv.ticket_id,tv.event_key,tv.event_kind,tv.recipient_kind,(j.status='sent') AS success,
 CASE WHEN j.body_encrypted<>''::bytea THEN '[已加密]' ELSE '' END AS content`+base+
			" ORDER BY j.id DESC", f.args, limit, offset)
}
func (s *Store) AccountEmailLog(ctx context.Context, filters url.Values, actor int64, limit, offset int) (map[string]any, error) {
	customer, err := s.accountLogCustomer(ctx, filters, actor)
	if err != nil {
		return nil, err
	}
	f := accountLogFilters{where: []string{"TRUE"}}
	if customer != nil {
		f.args = append(f.args, *customer, actor)
		f.where = append(f.where, "admin_customer_allowed($2,$1)", accountOutboxCustomerSQL("email", "$1"))
	}
	if status := strings.TrimSpace(filters.Get("status")); status != "" {
		if !accountOutboxStatus(status) {
			return nil, &AccountBusinessError{400, "invalid_email_status", "邮件状态无效"}
		}
		f.add(status, "j.status=$%d")
	}
	if recipient := strings.TrimSpace(filters.Get("recipient")); recipient != "" {
		f.add("%"+recipient+"%", "j.recipient ILIKE $%d")
	}
	if subject := strings.TrimSpace(filters.Get("subject")); subject != "" {
		f.add("%"+subject+"%", "j.subject ILIKE $%d")
	}
	if q := strings.TrimSpace(filters.Get("q")); q != "" {
		f.add("%"+q+"%", "(j.recipient ILIKE $%d OR j.subject ILIKE $%d OR j.kind ILIKE $%d OR coalesce(e.display_name,'') ILIKE $%d OR coalesce(e.username,'') ILIKE $%d OR coalesce(e.email,'') ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = fmt.Sprintf("(j.recipient ILIKE $%d OR j.subject ILIKE $%d OR j.kind ILIKE $%d OR coalesce(e.display_name,'') ILIKE $%d OR coalesce(e.username,'') ILIKE $%d OR coalesce(e.email,'') ILIKE $%d)", idx, idx, idx, idx, idx, idx)
	}
	if username := strings.TrimSpace(filters.Get("username")); username != "" {
		f.add("%"+username+"%", "(e.display_name ILIKE $%d OR e.username ILIKE $%d OR e.email ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = fmt.Sprintf("(e.display_name ILIKE $%d OR e.username ILIKE $%d OR e.email ILIKE $%d)", idx, idx, idx)
	}
	if err := accountLogDateFilters(filters, &f, "j.created_at"); err != nil {
		return nil, &AccountBusinessError{400, "invalid_log_date", err.Error()}
	}
	if err := accountTicketLogFilters(filters, &f, "email", actor); err != nil {
		return nil, err
	}
	where := strings.Join(f.where, " AND ")
	base := ` FROM email_outbox j LEFT JOIN LATERAL (
 SELECT n.user_id FROM notification_delivery_events n WHERE n.email_outbox_id=j.id ORDER BY n.id LIMIT 1
) ev ON true LEFT JOIN LATERAL (
 SELECT te.ticket_id,te.event_key,te.kind AS event_kind,td.recipient_kind,td.customer_id,t.department_id
 FROM ticket_notification_deliveries td JOIN ticket_notification_events te ON te.id=td.event_id JOIN tickets t ON t.id=te.ticket_id
 WHERE td.email_outbox_id=j.id
 UNION ALL SELECT t.id,NULL::text,NULL::text,NULL::text,t.user_id,t.department_id FROM tickets t WHERE t.id=j.ticket_id LIMIT 1
 ) tv ON true LEFT JOIN account_identities e ON e.id=coalesce(ev.user_id,tv.customer_id) WHERE ` + where
	return accountLogPage(ctx, s.Pool, "SELECT count(*)"+base,
		`SELECT j.id,j.created_at,j.subject,j.recipient,j.status,j.attempts,j.max_attempts,j.last_error,j.last_error AS failure_reason,
 j.sent_at,j.dead_letter_at,e.display_name AS username,NULL::inet AS remote_ip,NULL::inet AS ip,
 tv.ticket_id,tv.event_key,tv.event_kind,tv.recipient_kind,(j.status='sent') AS success`+base+
			" ORDER BY j.id DESC", f.args, limit, offset)
}
func (s *Store) AccountInboxLog(ctx context.Context, filters url.Values, actor int64, limit, offset int) (map[string]any, error) {
	customer, err := s.accountLogCustomer(ctx, filters, actor)
	if err != nil {
		return nil, err
	}
	f := accountLogFilters{where: []string{"admin_customer_allowed($1,m.user_id)"}, args: []any{actor}}
	if customer != nil {
		f.add(*customer, "m.user_id=$%d")
	}
	if status := strings.TrimSpace(filters.Get("status")); status != "" {
		if status != "sent" && status != "read" && status != "failed" {
			return nil, &AccountBusinessError{400, "invalid_inbox_status", "站内信状态无效"}
		}
		f.add(status, "m.status=$%d")
	}
	if subject := strings.TrimSpace(filters.Get("subject")); subject != "" {
		f.add("%"+subject+"%", "m.subject ILIKE $%d")
	}
	if username := strings.TrimSpace(filters.Get("username")); username != "" {
		f.add("%"+username+"%", "(u.display_name ILIKE $%d OR u.username ILIKE $%d OR u.email ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = fmt.Sprintf("(u.display_name ILIKE $%d OR u.username ILIKE $%d OR u.email ILIKE $%d)", idx, idx, idx)
	}
	if q := strings.TrimSpace(filters.Get("q")); q != "" {
		f.add("%"+q+"%", "(m.subject ILIKE $%d OR m.body ILIKE $%d OR u.display_name ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = fmt.Sprintf("(m.subject ILIKE $%d OR m.body ILIKE $%d OR u.display_name ILIKE $%d)", idx, idx, idx)
	}
	if err := accountLogDateFilters(filters, &f, "m.created_at"); err != nil {
		return nil, &AccountBusinessError{400, "invalid_log_date", err.Error()}
	}
	where := strings.Join(f.where, " AND ")
	base := " FROM admin_inbox_messages m JOIN account_identities u ON u.id=m.user_id WHERE " + where
	return accountLogPage(ctx, s.Pool, "SELECT count(*)"+base,
		`SELECT m.id,m.created_at,m.subject,m.status,m.user_id,u.display_name AS username,m.read_at`+base+
			" ORDER BY m.id DESC", f.args, limit, offset)
}
func (s *Store) AccountAdministratorLoginLog(ctx context.Context, filters url.Values, limit, offset int) (map[string]any, error) {
	f := accountLogFilters{where: []string{"a.action='auth.login_succeeded'", "u.role IN ('admin','super_admin')"}}
	if q := strings.TrimSpace(filters.Get("q")); q != "" {
		f.add("%"+q+"%", "(u.username ILIKE $%d OR u.email ILIKE $%d OR u.display_name ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = "(u.username ILIKE $" + strconv.Itoa(idx) + " OR u.email ILIKE $" + strconv.Itoa(idx) + " OR u.display_name ILIKE $" + strconv.Itoa(idx) + ")"
	}
	if username := strings.TrimSpace(filters.Get("username")); username != "" {
		f.add("%"+username+"%", "(u.username ILIKE $%d OR u.display_name ILIKE $%d)")
		idx := len(f.args)
		f.where[len(f.where)-1] = "(u.username ILIKE $" + strconv.Itoa(idx) + " OR u.display_name ILIKE $" + strconv.Itoa(idx) + ")"
	}
	if ip := strings.TrimSpace(filters.Get("ip")); ip != "" {
		f.add("%"+ip+"%", "coalesce(a.payload->>'remote_addr','') ILIKE $%d")
	}
	if err := accountLogDateFilters(filters, &f, "a.created_at"); err != nil {
		return nil, &AccountBusinessError{400, "invalid_log_date", err.Error()}
	}
	where := strings.Join(f.where, " AND ")
	base := ` FROM audit_events a JOIN administrators u ON u.id=a.actor_id
LEFT JOIN LATERAL (SELECT created_at FROM audit_events l WHERE l.action='auth.logout' AND l.actor_id=a.actor_id AND l.created_at>=a.created_at ORDER BY l.created_at LIMIT 1) logout ON true WHERE ` + where
	return accountLogPage(ctx, s.Pool, "SELECT count(*)"+base,
		`SELECT a.id,a.created_at AS login_time,a.created_at AS last_access,logout.created_at AS logout_time,
u.username,u.display_name,coalesce(a.payload->>'remote_addr','') AS ip`+base+
			" ORDER BY a.id DESC", f.args, limit, offset)
}
