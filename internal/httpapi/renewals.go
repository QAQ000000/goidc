package httpapi

import "net/http"

func (s *Server) renewService(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct{}
	if !decode(w, r, &input) {
		return
	}
	result, err := s.db.CreateRenewalInvoice(r.Context(), identityFrom(r).ID, id, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	write(w, status, result)
}
