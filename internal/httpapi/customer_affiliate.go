package httpapi

import "net/http"

func (s *Server) adminCustomerAffiliate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	items, err := s.db.AdminCustomerAffiliate(r.Context(), identityFrom(r).ID, id, limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, items)
}

func (s *Server) adminCustomerAffiliatePage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	page, err := s.db.AdminCustomerAffiliatePage(r.Context(), identityFrom(r).ID, id, limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, page)
}
