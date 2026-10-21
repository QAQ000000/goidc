package httpapi

import "net/http"

func (s *Server) registerPaymentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/invoices/{id}/pay", s.auth(s.payInvoice, false))
	mux.HandleFunc("POST /api/v1/balance/topups/{id}/cancel", s.sessionAuth(s.cancelOnlineTopup, false))
	mux.HandleFunc("GET /api/v1/balance/topup-options", s.sessionAuth(s.topupOptions, false))
	mux.HandleFunc("POST /api/v1/balance/topups", s.sessionAuth(s.createOnlineTopup, false))
	mux.HandleFunc("POST /api/v1/invoices/{id}/payments", s.auth(s.createPayment, false))
	mux.HandleFunc("POST /api/v1/invoices/{id}/payments/{payment_id}/reconcile", s.auth(s.reconcilePayment, false))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/payments/{payment_id}/reconcile", s.auth(s.reconcilePayment, true))
	mux.HandleFunc("GET /api/v1/admin/invoices/{id}/payments/{payment_id}/close-verifications", s.paymentCloseHTTP(s.closeVerificationList))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/payments/{payment_id}/close-verifications", s.paymentCloseHTTP(s.closeVerificationWrite))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/payments/{payment_id}/close-verifications/{verification_id}/{action}", s.paymentCloseHTTP(s.closeVerificationReview))
	mux.HandleFunc("GET /api/v1/admin/invoices/{id}/payments/{payment_id}/close", s.paymentCloseHTTP(s.paymentCloseStatus))
	mux.HandleFunc("GET /api/v1/admin/invoices/{id}/payment-anomaly-groups", s.paymentCloseHTTP(s.paymentAnomalyGroups))
	mux.HandleFunc("GET /api/v1/admin/invoices/{id}/payment-anomaly-summary", s.paymentCloseHTTP(s.paymentAnomalySummary))
	mux.HandleFunc("GET /api/v1/admin/invoices/{id}/payment-anomaly-entries", s.paymentCloseHTTP(s.paymentAnomalyEntries))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/payment-anomaly-groups/{anchor_id}/refunds", s.paymentCloseHTTP(s.createPaymentAnomalyRefund))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/payment-anomaly-refunds/{refund_id}/confirm", s.paymentCloseHTTP(s.reviewPaymentAnomalyRefund))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/payment-anomaly-refunds/{refund_id}/reject", s.paymentCloseHTTP(s.reviewPaymentAnomalyRefund))
	mux.HandleFunc("POST /api/v1/admin/invoices/{id}/payments/{payment_id}/close", s.paymentCloseHTTP(s.closePayment))
	mux.HandleFunc("GET /api/v1/payment-providers", s.auth(s.paymentProviders, false))
	mux.HandleFunc("POST /api/v1/payments/{provider}/webhook", s.paymentWebhook)
	mux.HandleFunc("GET /api/v1/payments/{provider}/webhook", s.paymentWebhook)
}
