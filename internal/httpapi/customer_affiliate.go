package httpapi

import (
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) adminCustomerAffiliate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	items, err := s.db.AdminCustomerAffiliate(r.Context(), identityFrom(r).ID, id, limit, offset)
	if err != nil {
		if c, ok := err.(*store.Conflict); ok && c.Code == "not_found" {
			writeError(w, http.StatusNotFound, c.Code, c.Message)
			return
		}
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, items)
}
