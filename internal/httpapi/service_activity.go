package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
)

var serviceActivityAuditActions = store.ServiceActivityAuditActions
var serviceActivityJobActions = store.ServiceActivityJobActions

var serviceActivityActions = func() map[string]bool {
	m := make(map[string]bool, len(serviceActivityAuditActions)+len(serviceActivityJobActions))
	for _, action := range serviceActivityAuditActions {
		m[action] = true
	}
	for _, action := range serviceActivityJobActions {
		m[action] = true
	}
	return m
}()

var serviceActivityCategories = map[string]bool{
	"lifecycle": true, "operation": true, "configuration": true, "billing": true, "cancellation": true,
}

var serviceActivityStatuses = map[string]bool{
	"pending": true, "running": true, "queued": true, "waiting_manual": true, "waiting_verification": true,
	"succeeded": true, "failed": true, "cancelled": true, "completed": true, "verified": true,
	"recorded": true, "requested": true, "withdrawn": true, "approved": true, "rejected": true,
}

func (s *Server) serviceActivity(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, offset, valid := serviceActivityPagination(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_pagination", "分页参数无效")
		return
	}
	action, valid := serviceActivityFilter(r, "action", serviceActivityActions)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_activity_action", "服务历史动作无效")
		return
	}
	category, valid := serviceActivityFilter(r, "category", serviceActivityCategories)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_activity_category", "服务历史分类无效")
		return
	}
	status, valid := serviceActivityFilter(r, "status", serviceActivityStatuses)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_activity_status", "服务历史状态无效")
		return
	}

	result, err := s.db.ServiceAccountActivity(requestFileContext(r), store.ServiceActivityAccountFilter{
		ServiceID: id, Actor: identityFrom(r).ID, Admin: adminRoute(r), Action: action, Category: category, Status: status, Limit: limit, Offset: offset,
	})
	if err != nil {
		s.failTicketAccount(w, r, err)
		return
	}

	w.Header().Set("Cache-Control", "private, no-store")
	write(w, http.StatusOK, result)
}

func serviceActivityFilter(r *http.Request, key string, allowed map[string]bool) (string, bool) {
	values, exists := r.URL.Query()[key]
	if !exists {
		return "", true
	}
	if len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	return value, allowed[value]
}

func serviceActivityPagination(r *http.Request) (int, int, bool) {
	limit, offset := 25, 0
	if raw, ok := r.URL.Query()["limit"]; ok {
		if len(raw) != 1 || raw[0] == "" {
			return 0, 0, false
		}
		parsed, err := strconv.Atoi(raw[0])
		if err != nil || parsed < 1 || parsed > 100 {
			return 0, 0, false
		}
		limit = parsed
	}
	if raw, ok := r.URL.Query()["offset"]; ok {
		if len(raw) != 1 || raw[0] == "" {
			return 0, 0, false
		}
		parsed, err := strconv.Atoi(raw[0])
		if err != nil || parsed < 0 || parsed > 1000000 {
			return 0, 0, false
		}
		offset = parsed
	}
	return limit, offset, true
}
