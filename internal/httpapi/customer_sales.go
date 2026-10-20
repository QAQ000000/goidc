package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) salesOwners(w http.ResponseWriter, r *http.Request) {
	items, err := s.db.SalesOwnerDirectory(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, items)
}

func (s *Server) assignCustomerSalesOwner(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Owner           json.RawMessage `json:"sales_owner_id"`
		ExpectedVersion int64           `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	var owner *int64
	if len(input.Owner) == 0 || json.Unmarshal(input.Owner, &owner) != nil || input.ExpectedVersion < 1 || (owner != nil && *owner < 1) {
		writeError(w, 400, "invalid_sales_assignment", "销售归属或版本无效")
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	key := ""
	if len(keys) > 0 {
		if len(keys) != 1 || len(keys[0]) < 8 || len(keys[0]) > 128 || strings.TrimSpace(keys[0]) != keys[0] {
			writeError(w, 400, "invalid_idempotency_key", "请提供长度为 8 到 128 的幂等键")
			return
		}
		key = keys[0]
	}
	out, err := s.db.AssignCustomerSalesOwnerWithKey(requestFileContext(r), identityFrom(r).ID, id, input.ExpectedVersion, owner, key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
