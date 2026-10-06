package httpapi

import "net/http"

func (s *Server) setAutoRenew(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := s.db.SetAutoRenew(r.Context(), identityFrom(r).ID, id, input.Enabled, adminRoute(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"service_id": id, "auto_renew": input.Enabled})
}
