package httpapi

import "net/http"

func (s *Server) resourcesPage(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	page, err := s.db.AdminResourcesPage(r.Context(), identityFrom(r).ID, r.URL.Query(), limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, page)
}
