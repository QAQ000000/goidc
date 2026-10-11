package httpapi

import (
	"net/http"
)

func (s *Server) bulkCancelOrders(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ExpectedVersions map[int64]int64 `json:"expected_versions"`
		OrderIDs         []int64         `json:"order_ids"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := s.db.BulkCancelOrders(r.Context(), identityFrom(r).ID, input.OrderIDs, r.Header.Get("Idempotency-Key"), input.ExpectedVersions)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}
