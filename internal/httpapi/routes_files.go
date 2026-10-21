package httpapi

import "net/http"

func (s *Server) registerFileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/customer-files", s.auth(s.customerFiles, false))
	mux.HandleFunc("GET /api/v1/customer-files/{id}/file", s.auth(s.downloadCustomerFile, false))
	mux.HandleFunc("GET /api/v1/admin/customer-files", s.auth(s.customerFiles, true))
	mux.HandleFunc("GET /api/v1/admin/customer-files/owners", s.auth(s.customerFileOwners, true))
	mux.HandleFunc("GET /api/v1/admin/customer-files/{id}", s.auth(s.adminCustomerFileDetail, true))
	mux.HandleFunc("GET /api/v1/admin/customer-files/{id}/file", s.auth(s.downloadCustomerFile, true))
	mux.HandleFunc("POST /api/v1/admin/customer-files", s.auth(s.saveCustomerFile, true))
	mux.HandleFunc("PUT /api/v1/admin/customer-files/{id}", s.auth(s.saveCustomerFile, true))
	mux.HandleFunc("DELETE /api/v1/admin/customer-files/{id}", s.auth(s.deleteCustomerFiles, true))
	mux.HandleFunc("POST /api/v1/admin/customer-files/bulk-delete", s.auth(s.deleteCustomerFiles, true))
	mux.HandleFunc("GET /api/v1/admin/downloads", s.auth(s.downloadsList, true))
	mux.HandleFunc("GET /api/v1/admin/downloads/{id}", s.auth(s.adminDownloadDetail, true))
	mux.HandleFunc("POST /api/v1/admin/downloads", s.auth(s.saveDownload, true))
	mux.HandleFunc("PUT /api/v1/admin/downloads/{id}", s.auth(s.saveDownload, true))
	mux.HandleFunc("DELETE /api/v1/admin/downloads/{id}", s.auth(s.deleteDownload, true))
	mux.HandleFunc("GET /api/v1/admin/download-categories", s.auth(s.downloadCategories, true))
	mux.HandleFunc("POST /api/v1/admin/download-categories", s.auth(s.saveDownloadCategory, true))
	mux.HandleFunc("PUT /api/v1/admin/download-categories/{id}", s.auth(s.saveDownloadCategory, true))
	mux.HandleFunc("DELETE /api/v1/admin/download-categories/{id}", s.auth(s.deleteDownloadCategory, true))
	mux.HandleFunc("GET /api/v1/admin/file-groups", s.auth(s.adminFileGroups, true))
	mux.HandleFunc("POST /api/v1/admin/file-groups", s.auth(s.saveDownloadCategory, true))
	mux.HandleFunc("PUT /api/v1/admin/file-groups/{id}", s.auth(s.saveDownloadCategory, true))
	mux.HandleFunc("DELETE /api/v1/admin/file-groups/{id}", s.auth(s.deleteDownloadCategory, true))
	mux.HandleFunc("GET /api/v1/admin/download-settings", s.auth(s.downloadSettings, true))
	mux.HandleFunc("GET /api/v1/admin/download-products", s.auth(s.downloadProducts, true))
	mux.HandleFunc("PUT /api/v1/admin/download-settings", s.auth(s.downloadSettings, true))
}
