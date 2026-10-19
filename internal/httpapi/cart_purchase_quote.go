package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
)

func (s *Server) cartPurchaseQuote(w http.ResponseWriter, r *http.Request) {
	var input store.CheckoutInput
	if !decode(w, r, &input) {
		return
	}
	out, err := s.db.QuoteCartPurchase(r.Context(), identityFrom(r).ID, input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}
