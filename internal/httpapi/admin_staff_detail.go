package httpapi

import (
	"net/http"
)

func (s *Server) adminStaffDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, err := s.db.AdminStaffDetail(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, item)
}
