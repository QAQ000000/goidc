package httpapi

import "net/http"

func (s *Server) registerRuntimeAndAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /api/v1/admin/runtime", s.auth(s.workerRuntime, true))
	mux.HandleFunc("POST /api/host/sync", s.supplierHostSync)
	mux.HandleFunc("POST /api/v1/host/sync", s.supplierHostSync)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("GET /api/v1/auth/captcha", s.issueCaptcha)
	mux.HandleFunc("POST /api/v1/auth/login/mfa", s.loginMFA)
	mux.HandleFunc("GET /api/v1/auth/registration-fields", s.registrationFields)
	mux.HandleFunc("POST /api/v1/auth/register", s.register)
	mux.HandleFunc("GET /api/v1/auth/verification/channels", s.verificationChannels)
	mux.HandleFunc("POST /api/v1/auth/verification/request", s.requestRegistrationVerification)
	mux.HandleFunc("POST /api/v1/account/verification/request", s.accountAuth(s.requestAccountVerification, true))
	mux.HandleFunc("POST /api/v1/account/verification/confirm", s.accountAuth(s.confirmAccountVerification, true))
	mux.HandleFunc("POST /api/v1/auth/password-reset/request", s.requestPasswordReset)
	mux.HandleFunc("POST /api/v1/auth/password-reset/confirm", s.confirmPasswordReset)
	mux.HandleFunc("POST /api/v1/auth/logout", s.accountAuth(s.logout, false))
	mux.HandleFunc("GET /api/v1/auth/impersonation", s.accountAuth(s.impersonationStatus, false))
	mux.HandleFunc("POST /api/v1/auth/impersonation/exit", s.exitImpersonation)
	mux.HandleFunc("GET /api/v1/me", s.accountAuth(s.me, false))
	mux.HandleFunc("GET /api/v1/catalog", s.catalog)
	mux.HandleFunc("GET /api/v1/affiliate/click/{code}", s.affiliateClick)
	mux.HandleFunc("GET /api/v1/catalog/page", s.catalogPage)
	mux.HandleFunc("GET /api/v1/catalog/sections", s.catalogSections)
	mux.HandleFunc("GET /api/v1/site", s.publicSite)
	mux.HandleFunc("GET /api/v1/public-index", s.publicIndex)
	mux.HandleFunc("GET /api/v1/public-content/{kind}/{id}", s.publicContentPreview)
	mux.HandleFunc("GET /api/v1/content-images/{id}", s.contentImage)
}

func (s *Server) registerPublicContentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/catalog/{id}", s.product)
	mux.HandleFunc("GET /api/v1/categories", s.categories)
	mux.HandleFunc("GET /api/v1/news", s.news)
	mux.HandleFunc("GET /api/v1/news/{id}", s.newsDetail)
	mux.HandleFunc("GET /api/v1/knowledgebase", s.knowledge)
	mux.HandleFunc("GET /api/v1/knowledgebase/{id}", s.knowledgeDetail)
	mux.HandleFunc("GET /api/v1/downloads", s.downloadsList)
	mux.HandleFunc("GET /api/v1/downloads/{id}/file", s.downloadFile)
	mux.HandleFunc("GET /api/v1/download-categories", s.downloadCategories)
}
