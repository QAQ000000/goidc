package httpapi

import "net/http"

func (s *Server) serviceMonitoring(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !s.authLimit(w, r, "service-monitoring", 30) {
		return
	}
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "1h"
	}
	result, err := s.db.ServiceMonitoring(r.Context(), identityFrom(r).ID, id, adminRoute(r), period)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Also revalidate the session/API key after the network call, including logout.
	s.auth(func(w http.ResponseWriter, r *http.Request) { write(w, 200, result) }, adminRoute(r))(w, freshBrowserSessionRequest(r))
}
