package httpapi

import "net/http"

func (s *Server) registerAccountBillingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/account/certification", s.auth(s.certification, false))
	mux.HandleFunc("POST /api/v1/account/certification", s.auth(s.submitCertification, false))
	mux.HandleFunc("POST /api/v1/account/certification/convert-company", s.auth(s.convertCertificationToCompany, false))
	mux.HandleFunc("GET /api/v1/invoice-profiles", s.auth(s.invoiceProfiles, false))
	mux.HandleFunc("POST /api/v1/invoice-profiles", s.auth(s.invoiceProfiles, false))
	mux.HandleFunc("PUT /api/v1/invoice-profiles/{id}", s.auth(s.invoiceProfiles, false))
	mux.HandleFunc("DELETE /api/v1/invoice-profiles/{id}", s.auth(s.invoiceProfiles, false))
	mux.HandleFunc("GET /api/v1/invoice-addresses", s.auth(s.invoiceAddresses, false))
	mux.HandleFunc("POST /api/v1/invoice-addresses", s.auth(s.invoiceAddresses, false))
	mux.HandleFunc("PUT /api/v1/invoice-addresses/{id}", s.auth(s.invoiceAddresses, false))
	mux.HandleFunc("DELETE /api/v1/invoice-addresses/{id}", s.auth(s.invoiceAddresses, false))
	mux.HandleFunc("GET /api/v1/invoiceable-invoices", s.auth(s.invoiceableInvoices, false))
	mux.HandleFunc("GET /api/v1/invoice-applications", s.auth(s.invoiceApplications, false))
	mux.HandleFunc("GET /api/v1/invoice-applications/{id}", s.auth(s.invoiceApplicationDetail, false))
	mux.HandleFunc("GET /api/v1/invoice-applications/{id}/documents/{document_id}/file", s.auth(s.invoiceApplicationDocumentFile, false))
	mux.HandleFunc("POST /api/v1/invoice-applications", s.auth(s.createInvoiceApplication, false))
	mux.HandleFunc("GET /api/v1/admin/invoice-applications", s.auth(s.invoiceApplications, true))
	mux.HandleFunc("GET /api/v1/admin/invoice-applications/{id}", s.auth(s.invoiceApplicationDetail, true))
	mux.HandleFunc("POST /api/v1/admin/invoice-applications/{id}/review", s.auth(s.reviewInvoiceApplication, true))
	mux.HandleFunc("POST /api/v1/admin/invoice-applications/{id}/adjust", s.auth(s.adjustInvoiceApplication, true))
	mux.HandleFunc("POST /api/v1/admin/invoice-applications/{id}/document", s.auth(s.uploadInvoiceApplicationDocument, true))
	mux.HandleFunc("POST /api/v1/admin/invoice-applications/{id}/adjustments/{adjustment_id}/document", s.auth(s.uploadInvoiceApplicationDocument, true))
	mux.HandleFunc("GET /api/v1/admin/invoice-applications/{id}/documents/{document_id}/file", s.auth(s.invoiceApplicationDocumentFile, true))
}

func (s *Server) registerAccountSecurityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/v1/account/password", s.accountAuth(s.changePassword, false))
	mux.HandleFunc("GET /api/v1/account/mfa", s.accountAuth(s.mfaStatus, true))
	mux.HandleFunc("POST /api/v1/account/mfa/enroll", s.accountAuth(s.mfaEnroll, true))
	mux.HandleFunc("POST /api/v1/account/mfa/confirm", s.accountAuth(s.mfaConfirm, true))
	mux.HandleFunc("POST /api/v1/account/mfa/disable", s.accountAuth(s.mfaDisable, true))
	mux.HandleFunc("GET /api/v1/api-keys", s.accountAuth(s.listAPIKeys, true))
	mux.HandleFunc("POST /api/v1/api-keys", s.accountAuth(s.createAPIKey, true))
	mux.HandleFunc("DELETE /api/v1/api-keys/{id}", s.accountAuth(s.revokeAPIKey, true))
}

func (s *Server) registerContactRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/contacts", s.auth(s.contacts, false))
	mux.HandleFunc("POST /api/v1/contacts", s.auth(s.createContact, false))
	mux.HandleFunc("PATCH /api/v1/contacts/{id}", s.auth(s.updateContact, false))
	mux.HandleFunc("DELETE /api/v1/contacts/{id}", s.auth(s.deleteContact, false))
}
