package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Recovery, administration and payment callbacks remain available during an HTTP maintenance window.
func maintenanceExempt(r *http.Request) bool {
	path := r.URL.Path
	if (r.Method == "GET" || r.Method == "POST") && strings.HasPrefix(path, "/api/v1/payments/epay_") && strings.HasSuffix(path, "/webhook") {
		return true
	}
	if (r.Method == "GET" || r.Method == "HEAD") && (path == "/healthz" || path == "/readyz" || path == "/api/v1/site" || path == "/api/v1/me" || path == "/api/v1/auth/captcha") {
		return true
	}
	if strings.HasPrefix(path, "/api/v1/admin/") {
		return true
	}
	if r.Method == "POST" {
		switch path {
		case "/api/v1/auth/login", "/api/v1/auth/login/mfa", "/api/v1/auth/logout", "/api/v1/auth/password-reset/request", "/api/v1/auth/password-reset/confirm":
			return true
		}
		return strings.HasPrefix(path, "/api/v1/payments/") && strings.HasSuffix(path, "/webhook")
	}
	return false
}

func (s *Server) maintenanceAllowed(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") || maintenanceExempt(r) {
		return true
	}
	settings, err := s.db.HTTPMaintenanceSettings(r.Context())
	enabled, message, target := settings.Enabled, settings.Message, settings.URL
	if errors.Is(err, pgx.ErrNoRows) {
		s.log.Error("site maintenance configuration missing", "path", r.URL.Path)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusServiceUnavailable, "site_configuration_unavailable", "服务配置暂不可用，请稍后重试")
		return false
	}
	if err != nil {
		s.fail(w, r, err)
		return false
	}
	if !enabled {
		return true
	}
	// Only explicitly selected shared administrator-account operations bypass
	// the client gate. An administrator cookie never previews customer business.
	if bearerCredential(r) == "" && requestPortal(r) == "admin" && sharedAccountPath(r.URL.Path) && browserSessionMatches(r, "admin") {
		return true
	}
	if message == "" {
		message = "站点维护中，请稍后再试。"
	}
	w.Header().Set("Retry-After", "60")
	writeErrorDetails(w, http.StatusServiceUnavailable, "site_maintenance", message, map[string]any{"maintenance_url": target})
	return false
}
