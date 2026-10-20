package httpapi

import "net/http"

func (s *Server) setAutoRenew(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_auto_renew", "请明确选择是否启用自动续费")
		return
	}
	if err := s.db.SetAutoRenew(requestFileContext(r), identityFrom(r).ID, id, *input.Enabled, adminRoute(r)); err != nil {
		s.failTicketAccount(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"service_id": id, "auto_renew": *input.Enabled})
}
