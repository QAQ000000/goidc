package httpapi

import "net/http"

func (s *Server) adminCustomerSummary(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	result, err := s.db.AdminCustomerSummary(r.Context(), identityFrom(r).ID, id)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, result)
}
