package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) services(w http.ResponseWriter, r *http.Request)     { s.servicesList(w, r, false) }
func (s *Server) servicesPage(w http.ResponseWriter, r *http.Request) { s.servicesList(w, r, true) }
func (s *Server) servicesList(w http.ResponseWriter, r *http.Request, paged bool) {
	var group *int64
	var customerID *int64
	if adminRoute(r) {
		var err error
		customerID, err = s.db.AdminCustomerFilter(r.Context(), identityFrom(r).ID, r.URL.Query())
		if accountBusinessFailure(s, w, r, err) {
			return
		}
	}
	if raw := r.URL.Query().Get("group_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 0 {
			writeError(w, 400, "invalid_service_group", "服务分组筛选无效")
			return
		}
		group = &id
	}
	state := r.URL.Query().Get("status")
	if state != "" && state != "pending_payment" && state != "queued" && state != "provisioning" && state != "active" && state != "suspended" && state != "terminated" && state != "failed" && state != "cancelled" && state != "import_review_required" {
		writeError(w, 400, "invalid_status", "服务状态无效")
		return
	}
	limit, offset := pagination(r)
	result, err := s.db.ServiceAccountList(r.Context(), store.ServiceAccountListFilter{
		Actor: identityFrom(r).ID, Admin: adminRoute(r), Page: paged,
		CustomerID: customerID, GroupID: group, Status: state,
		Query: strings.TrimSpace(r.URL.Query().Get("q")), Limit: limit, Offset: offset,
	})
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, result)
}

func (s *Server) serviceDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	result, err := s.db.ServiceAccountDetail(requestFileContext(r), identityFrom(r).ID, id, adminRoute(r))
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, result)
}

func (s *Server) serviceAction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Action       string `json:"action"`
		System       int    `json:"system"`
		TempPassword string `json:"temp_password"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	if input.Action == "" {
		writeError(w, 400, "invalid_service_action", "服务动作不能为空")
		return
	}
	var result store.ServiceActionResult
	var err error
	if input.Action == "rescue_system" {
		result, err = s.db.RescueService(r.Context(), identityFrom(r).ID, id, input.System, input.TempPassword, r.Header.Get("Idempotency-Key"), adminRoute(r))
	} else {
		result, err = s.db.EnqueueServiceAction(r.Context(), identityFrom(r).ID, id, input.Action, r.Header.Get("Idempotency-Key"), adminRoute(r))
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	write(w, status, result)
}

func (s *Server) resetServicePassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := s.db.ResetServicePassword(r.Context(), identityFrom(r).ID, id, input.Password, r.Header.Get("Idempotency-Key"), adminRoute(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	write(w, status, result)
}

func (s *Server) serviceDelivery(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input store.ManualCompletion
	if !decode(w, r, &input) {
		return
	}
	result, err := s.db.CompleteManualServiceJob(requestFileContext(r), identityFrom(r).ID, id, input)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, result)
}
