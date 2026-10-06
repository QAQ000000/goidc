package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
)

func (s *Server) savePurchaseFields(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.PurchaseFieldsInput
	if !decodeLimit(w, r, &in, 64<<10) {
		return
	}
	if err := s.db.SavePurchaseFields(r.Context(), identityFrom(r).ID, id, in); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"product_id": id, "revision": in.ExpectedRevision + 1})
}
