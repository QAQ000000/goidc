package httpapi

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/correlation"
	"github.com/codex/zjmfgo/internal/store"
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
	page, err := s.db.AccountSMSLog(r.Context(), r.URL.Query(), identityFrom(r).ID, limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, page)
}

func (s *Server) adminEmailLog(w http.ResponseWriter, r *http.Request) {
	limit, offset := logPageArgs(r)
	page, err := s.db.AccountEmailLog(r.Context(), r.URL.Query(), identityFrom(r).ID, limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, page)
}

func (s *Server) adminInboxLog(w http.ResponseWriter, r *http.Request) {
	limit, offset := logPageArgs(r)
	page, err := s.db.AccountInboxLog(r.Context(), r.URL.Query(), identityFrom(r).ID, limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, page)
}

func (s *Server) adminAPILog(w http.ResponseWriter, r *http.Request) {
	limit, offset := logPageArgs(r)
	filter := store.APIAccessLogFilter{}
	if method := strings.TrimSpace(r.URL.Query().Get("method")); method != "" {
		method = strings.ToUpper(method)
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		default:
			writeError(w, http.StatusBadRequest, "invalid_api_method", "API方法无效")
			return
		}
		filter.Method = method
	}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		code, err := strconv.Atoi(status)
		if err != nil || code < 100 || code > 599 {
			writeError(w, http.StatusBadRequest, "invalid_api_status", "API状态码无效")
			return
		}
		filter.StatusCode = code
	}
	if ip := strings.TrimSpace(r.URL.Query().Get("ip")); ip != "" {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			writeError(w, http.StatusBadRequest, "invalid_api_ip", "IP地址无效")
			return
		}
		filter.RemoteIP = parsed
	}
	filter.Username = strings.TrimSpace(r.URL.Query().Get("username"))
	filter.Search = strings.TrimSpace(r.URL.Query().Get("q"))
	if value := strings.TrimSpace(r.URL.Query().Get("correlation_id")); value != "" {
		if !correlation.Valid(value) {
			writeError(w, http.StatusBadRequest, "invalid_correlation_id", "请求编号无效")
			return
		}
		filter.CorrelationID = value
	}
	for _, key := range []string{"from", "to"} {
		value := strings.TrimSpace(r.URL.Query().Get(key))
		if value == "" {
			continue
		}
		d, err := parseLogDate(value, key, key == "to")
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_log_date", err.Error())
			return
		}
		if key == "from" {
			filter.From = &d
		} else {
			filter.Until = &d
		}
	}
	page, err := s.db.APIAccessLogs(r.Context(), filter, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, page)
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
