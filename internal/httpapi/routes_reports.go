package httpapi

import "net/http"

func (s *Server) registerAdminReportRoutes(mux *http.ServeMux) {
	s.registerProductReportRoutes(mux)
	s.registerAnnualReportRoutes(mux)
	s.registerTicketReportRoutes(mux)
	s.registerSalesReportRoutes(mux)
	mux.HandleFunc("GET /api/v1/admin/reports/income", s.auth(s.incomeReport, true))
	mux.HandleFunc("GET /api/v1/admin/reports/sales", s.auth(s.salesReport, true))
	mux.HandleFunc("GET /api/v1/admin/reports/tickets", s.auth(s.ticketReport, true))
	mux.HandleFunc("GET /api/v1/admin/reports/new-clients", s.auth(s.newClientsReport, true))
	mux.HandleFunc("GET /api/v1/admin/reports/customer-ranking", s.auth(s.customerRankingReport, true))
	mux.HandleFunc("GET /api/v1/admin/reports/product-ranking", s.auth(s.productRankingReport, true))
	mux.HandleFunc("GET /api/v1/admin/reports/staff-ranking", s.auth(s.staffRankingReport, true))
	mux.HandleFunc("GET /api/v1/admin/reports/product-income", s.auth(s.productIncomeReport, true))
}
