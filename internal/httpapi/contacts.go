package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
)

type contactInput = store.ContactInput
type contactPatch = store.ContactPatch

func (s *Server) contacts(w http.ResponseWriter, r *http.Request) {
	customer, ok := contactCustomerID(w, r)
	if !ok {
		return
	}
	items, err := s.db.ListCustomerContacts(requestFileContext(r), identityFrom(r).ID, customer, adminRoute(r))
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, items)
}
func contactCustomerID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if adminRoute(r) {
		return pathID(w, r)
	}
	return identityFrom(r).ID, true
}

func contactPathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	value := r.PathValue("id")
	if r.PathValue("contact_id") != "" {
		value = r.PathValue("contact_id")
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid_id", "记录编号无效")
		return 0, false
	}
	return id, true
}

func contactMutationKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	if len(r.Header.Values("Idempotency-Key")) > 1 {
		writeError(w, 400, "invalid_idempotency_key", "请求键不能重复")
		return "", false
	}
	return mutationKey(w, r, "", false)
}
func (s *Server) createContact(w http.ResponseWriter, r *http.Request) {
	customer, ok := contactCustomerID(w, r)
	if !ok {
		return
	}
	var in store.ContactInput
	if !decode(w, r, &in) {
		return
	}
	key, ok := contactMutationKey(w, r)
	if !ok {
		return
	}
	out, replay, err := s.db.CreateCustomerContact(requestFileContext(r), identityFrom(r).ID, customer, adminRoute(r), key, in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	write(w, status, out)
}
func (s *Server) updateContact(w http.ResponseWriter, r *http.Request) {
	id, ok := contactPathID(w, r)
	if !ok {
		return
	}
	customer, ok := contactCustomerID(w, r)
	if !ok {
		return
	}
	var in store.ContactPatch
	if !decode(w, r, &in) {
		return
	}
	key, ok := contactMutationKey(w, r)
	if !ok {
		return
	}
	out, err := s.db.UpdateCustomerContact(requestFileContext(r), identityFrom(r).ID, customer, id, adminRoute(r), key, in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, out)
}
func (s *Server) deleteContact(w http.ResponseWriter, r *http.Request) {
	id, ok := contactPathID(w, r)
	if !ok {
		return
	}
	customer, ok := contactCustomerID(w, r)
	if !ok {
		return
	}
	var in store.ContactDeleteInput
	// Older clients omitted DELETE bodies altogether.
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	key, ok := contactMutationKey(w, r)
	if !ok {
		return
	}
	out, err := s.db.DeleteCustomerContact(requestFileContext(r), identityFrom(r).ID, customer, id, adminRoute(r), key, in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, out)
}

func (s *Server) customerGroups(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	where := `($1='' OR g.name ILIKE '%'||$1||'%' OR g.id::text=$1)`
	if r.URL.Query().Get("options") == "true" {
		s.list(w, r, `SELECT g.id,g.name,g.color,g.discount_percent,g.suspend_delete_exempt,g.separate_invoices,g.version,g.created_at,g.updated_at,count(m.customer_id)::int AS member_count FROM customer_groups g LEFT JOIN customer_group_members m ON m.group_id=g.id GROUP BY g.id ORDER BY g.id DESC`)
		return
	}
	limit, offset := pagination(r)
	s.writeListPage(w, r, `SELECT count(*) FROM customer_groups g WHERE `+where,
		`SELECT g.id,g.name,g.color,g.discount_percent,g.suspend_delete_exempt,g.separate_invoices,g.version,g.created_at,g.updated_at,count(m.customer_id)::int AS member_count FROM customer_groups g LEFT JOIN customer_group_members m ON m.group_id=g.id WHERE `+where+` GROUP BY g.id ORDER BY g.id DESC`, []any{q}, limit, offset)
}

func (s *Server) createCustomerGroup(w http.ResponseWriter, r *http.Request) {
	var input store.CustomerGroupCreateInput
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Color = strings.TrimSpace(input.Color)
	if input.Color == "" {
		input.Color = "#64748b"
	}
	if input.Name == "" || len(input.Name) > 120 || len(input.Color) != 7 || input.Color[0] != '#' || input.DiscountPercent < 0 || input.DiscountPercent > 100 {
		writeError(w, 400, "invalid_customer_group", "客户分组字段无效")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	out, replay, err := s.db.CreateAdminCustomerGroup(requestFileContext(r), identityFrom(r).ID, key, input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	write(w, status, out)
}

func (s *Server) updateCustomerGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input store.CustomerGroupPatchInput
	if !decode(w, r, &input) {
		return
	}
	if input.Name != nil {
		*input.Name = strings.TrimSpace(*input.Name)
		if *input.Name == "" || len(*input.Name) > 120 {
			writeError(w, 400, "invalid_customer_group", "分组名称无效")
			return
		}
	}
	if input.Color != nil {
		*input.Color = strings.TrimSpace(*input.Color)
		if len(*input.Color) != 7 || (*input.Color)[0] != '#' {
			writeError(w, 400, "invalid_customer_group", "分组颜色无效")
			return
		}
	}
	if input.DiscountPercent != nil && (*input.DiscountPercent < 0 || *input.DiscountPercent > 100) {
		writeError(w, 400, "invalid_customer_group", "折扣比例无效")
		return
	}
	if input.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "expected_version_required", "请提供有效版本")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	out, err := s.db.UpdateAdminCustomerGroup(requestFileContext(r), identityFrom(r).ID, id, key, input)
	if err != nil {
		s.failCustomerPricing(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) deleteCustomerGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input store.CustomerGroupDeleteInput
	if !decode(w, r, &input) {
		return
	}
	if input.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "expected_version_required", "请提供有效版本")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	out, err := s.db.DeleteAdminCustomerGroup(requestFileContext(r), identityFrom(r).ID, id, key, input)
	if err != nil {
		s.failCustomerPricing(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) assignCustomerGroup(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		GroupID         json.RawMessage `json:"group_id"`
		ExpectedVersion int64           `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	in := store.AdminCustomerGroupAssignmentInput{ExpectedVersion: input.ExpectedVersion, GroupIDPresent: input.GroupID != nil}
	if input.GroupID != nil && json.Unmarshal(input.GroupID, &in.GroupID) != nil {
		writeError(w, http.StatusBadRequest, "invalid_group", "客户分组必须为正整数或 null")
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) > 1 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "请提供单个幂等键")
		return
	}
	out, err := s.db.AssignAdminCustomerGroup(requestFileContext(r), identityFrom(r).ID, customerID, r.Header.Get("Idempotency-Key"), in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}
