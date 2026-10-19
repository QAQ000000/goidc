package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"
)

const impersonationSessionLifetime = 2 * time.Hour

func newSessionToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
func (s *Server) setSessionCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func (s *Server) clearSessionCookie(w http.ResponseWriter, name string) {
	s.setSessionCookie(w, name, "", -1)
}
func (s *Server) adminImpersonateCustomer(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathID(w, r)
	if !ok {
		return
	}
	if apiKeyRequest(r) {
		writeError(w, http.StatusForbidden, "session_required", "该操作需要浏览器会话")
		return
	}
	adminCookie, err := originalSessionCookie(r, "admin")
	if err != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	token, err := newSessionToken()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	currentHash := ""
	if current, err := originalSessionCookie(r, "customer"); err == nil {
		currentHash = tokenHash(current.Value)
	}
	if err = s.db.StartCustomerImpersonation(r.Context(), identityFrom(r).ID, customerID, tokenHash(adminCookie.Value), tokenHash(token), currentHash); err != nil {
		s.fail(w, r, err)
		return
	}
	s.setSessionCookie(w, customerSessionCookie, token, int(impersonationSessionLifetime/time.Second))
	write(w, 200, map[string]any{"customer_id": customerID, "expires_in_seconds": int64(impersonationSessionLifetime / time.Second)})
}
func (s *Server) impersonationStatus(w http.ResponseWriter, r *http.Request) {
	current, ce := originalSessionCookie(r, "customer")
	admin, ae := originalSessionCookie(r, "admin")
	if ce != nil || ae != nil {
		write(w, 200, map[string]any{"active": false})
		return
	}
	name, active, err := s.db.CustomerImpersonationStatus(r.Context(), identityFrom(r).ID, tokenHash(current.Value), tokenHash(admin.Value))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"active": active, "admin_name": name})
}
func (s *Server) exitImpersonation(w http.ResponseWriter, r *http.Request) {
	current, ce := originalSessionCookie(r, "customer")
	admin, ae := originalSessionCookie(r, "admin")
	if ce != nil || ae != nil {
		writeError(w, 409, "not_impersonating", "当前不是客户身份登录")
		return
	}
	id, err := s.db.EndCustomerImpersonation(r.Context(), tokenHash(current.Value), tokenHash(admin.Value))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.clearSessionCookie(w, customerSessionCookie)
	write(w, 200, map[string]any{"restored": true, "admin_id": id})
}
