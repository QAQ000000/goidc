package httpapi

import "net/http"

func (s *Server) registerAdminNotificationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/notification-templates/page", s.auth(s.notificationTemplatesPage, true))
	mux.HandleFunc("GET /api/v1/admin/notification-templates", s.auth(s.notificationTemplates, true))
	mux.HandleFunc("POST /api/v1/admin/notification-templates", s.auth(s.createNotificationTemplate, true))
	mux.HandleFunc("PATCH /api/v1/admin/notification-templates/{id}", s.auth(s.updateNotificationTemplate, true))
	mux.HandleFunc("GET /api/v1/admin/email-outbox", s.auth(s.emailOutbox, true))
	mux.HandleFunc("GET /api/v1/admin/logs", s.auth(s.adminLogPage, true))
	mux.HandleFunc("POST /api/v1/admin/email-outbox/{id}/retry", s.auth(s.retryEmail, true))
	mux.HandleFunc("GET /api/v1/admin/sms-outbox", s.auth(s.smsOutbox, true))
	mux.HandleFunc("POST /api/v1/admin/sms-outbox/{id}/retry", s.auth(s.retrySMS, true))
}
