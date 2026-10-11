package httpapi

import "net/http"

func (s *Server) cancelOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion *int64 `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	var versions []int64
	if input.ExpectedVersion != nil {
		versions = append(versions, *input.ExpectedVersion)
	}
	result, err := s.db.CancelOrder(r.Context(), identityFrom(r).ID, id, adminRoute(r), r.Header.Get("Idempotency-Key"), versions...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if !result.Replayed {
		status = http.StatusCreated
	}
	write(w, status, result)
}
