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
	if (r.Method == "GET" || r.Method == "HEAD") && (path == "/healthz" || path == "/readyz" || path == "/api/v1/site" || path == "/api/v1/me") {
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
	var enabled bool
	var message, target string
	err := s.db.Pool.QueryRow(r.Context(), "SELECT maintenance_enabled,maintenance_message,maintenance_url FROM site_settings WHERE id=1").Scan(&enabled, &message, &target)
	if err != nil {
		s.fail(w, r, err)
		return false
	}
	if !enabled {
		return true
	}
	// A browser session can preview the client site. A bearer key cannot borrow this bypass.
	if cookie, cookieErr := r.Cookie("zjmfgo_session"); cookieErr == nil && bearerCredential(r) == "" {
		var admin bool
		err = s.db.Pool.QueryRow(r.Context(), `SELECT u.role IN ('admin','super_admin') FROM sessions s JOIN users u ON u.id=s.user_id
 WHERE s.token_hash=$1 AND s.expires_at>now() AND u.status='active'`, tokenHash(cookie.Value)).Scan(&admin)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.fail(w, r, err)
			return false
		}
		if admin {
			return true
		}
	}
	if message == "" {
		message = "站点维护中，请稍后再试。"
	}
	w.Header().Set("Retry-After", "60")
	writeErrorDetails(w, http.StatusServiceUnavailable, "site_maintenance", message, map[string]any{"maintenance_url": target})
	return false
}
