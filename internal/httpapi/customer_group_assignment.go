package httpapi

import "net/http"

func (s *Server) adminCustomerGroupMembership(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := s.db.AdminCustomerGroupMembership(requestFileContext(r), identityFrom(r).ID, id)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}
