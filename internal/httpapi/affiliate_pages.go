package httpapi

import (
	"net/http"
	"net/url"
)

func (s *Server) adminAffiliatePage(w http.ResponseWriter, r *http.Request, kind string) {
	limit, offset := pagination(r)
	filters, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_affiliate_filter", "推广查询参数无效")
		return
	}
	result, err := s.db.AdminAffiliatePageWithFilters(r.Context(), identityFrom(r).ID, kind, filters, limit, offset)
	if accountBusinessFailure(s, w, r, err) {
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
