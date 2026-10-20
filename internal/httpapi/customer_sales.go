package httpapi

import (
	"encoding/json"
	"net/http"
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
	out, err := s.db.AssignCustomerSalesOwner(r.Context(), identityFrom(r).ID, id, input.ExpectedVersion, owner)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
