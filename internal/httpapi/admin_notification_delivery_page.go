package httpapi

import "net/http"

func (s *Server) adminNotificationDeliveriesPage(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	result, err := s.db.AdminNotificationDeliveriesPage(r.Context(), identityFrom(r).ID, r.URL.Query(), limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, result)
}
