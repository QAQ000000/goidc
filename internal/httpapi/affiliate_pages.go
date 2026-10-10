package httpapi

import "net/http"

func (s *Server) adminAffiliatePage(w http.ResponseWriter, r *http.Request, kind string) {
	limit, offset := pagination(r)
	result, err := s.db.AdminAffiliatePage(r.Context(), identityFrom(r).ID, kind, r.URL.Query().Get("search"), r.URL.Query().Get("status"), limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}
func (s *Server) adminAffiliateAccountsPage(w http.ResponseWriter, r *http.Request) {
	s.adminAffiliatePage(w, r, "accounts")
}
func (s *Server) adminAffiliateAttributionsPage(w http.ResponseWriter, r *http.Request) {
	s.adminAffiliatePage(w, r, "attributions")
}
func (s *Server) adminAffiliateWithdrawalsPage(w http.ResponseWriter, r *http.Request) {
	s.adminAffiliatePage(w, r, "withdrawals")
}
