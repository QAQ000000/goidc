package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

const adminSessionCookie = "zjmfgo_admin_session"
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
	adminCookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	if _, err = r.Cookie(adminSessionCookie); err == nil {
		writeError(w, http.StatusConflict, "already_impersonating", "当前已经以客户身份登录")
		return
	}
	customerToken, err := newSessionToken()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if err = store.LockAdminCustomer(r.Context(), tx, identityFrom(r).ID, customerID); err != nil {
		s.fail(w, r, err)
		return
	}
	var active bool
	if err = tx.QueryRow(r.Context(), "SELECT status='active' FROM users WHERE id=$1 AND role='customer' FOR SHARE", customerID).Scan(&active); err != nil {
		s.fail(w, r, err)
		return
	}
	if !active {
		writeError(w, http.StatusConflict, "customer_disabled", "停用客户不能切换登录")
		return
	}
	if _, err = tx.Exec(r.Context(), "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '2 hours')", tokenHash(customerToken), customerID); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "auth.impersonation_started", "user", customerID, map[string]any{"customer_id": customerID, "expires_in_seconds": int64(impersonationSessionLifetime / time.Second)}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	// Keep the administrator session in a separate HttpOnly cookie so the customer
	// view can return to the exact session that initiated the switch.
	s.setSessionCookie(w, adminSessionCookie, adminCookie.Value, int(impersonationSessionLifetime/time.Second))
	s.setSessionCookie(w, "zjmfgo_session", customerToken, int(impersonationSessionLifetime/time.Second))
	write(w, http.StatusOK, map[string]any{"customer_id": customerID, "expires_in_seconds": int64(impersonationSessionLifetime / time.Second)})
}

func (s *Server) impersonationStatus(w http.ResponseWriter, r *http.Request) {
	current, currentErr := r.Cookie("zjmfgo_session")
	adminCookie, adminErr := r.Cookie(adminSessionCookie)
	if currentErr != nil || adminErr != nil {
		write(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	var adminName string
	err := s.db.Pool.QueryRow(r.Context(), `SELECT admin.display_name
FROM sessions customer_session
JOIN users customer ON customer.id=customer_session.user_id AND customer.role='customer' AND customer.status='active'
JOIN sessions admin_session ON admin_session.token_hash=$1 AND admin_session.expires_at>now()
JOIN users admin ON admin.id=admin_session.user_id AND admin.role IN ('admin','super_admin') AND admin.status='active'
WHERE customer_session.token_hash=$2 AND customer_session.expires_at>now() AND customer_session.user_id=$3`, tokenHash(adminCookie.Value), tokenHash(current.Value), identityFrom(r).ID).Scan(&adminName)
	if errors.Is(err, pgx.ErrNoRows) {
		write(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"active": true, "admin_name": adminName})
}

func (s *Server) exitImpersonation(w http.ResponseWriter, r *http.Request) {
	current, currentErr := r.Cookie("zjmfgo_session")
	adminCookie, adminErr := r.Cookie(adminSessionCookie)
	if currentErr != nil || adminErr != nil {
		writeError(w, http.StatusConflict, "not_impersonating", "当前不是客户身份登录")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var adminID, customerID int64
	err = tx.QueryRow(r.Context(), `SELECT admin.id,customer.id
FROM sessions customer_session
JOIN users customer ON customer.id=customer_session.user_id AND customer.role='customer'
JOIN sessions admin_session ON admin_session.token_hash=$1 AND admin_session.expires_at>now()
JOIN users admin ON admin.id=admin_session.user_id AND admin.role IN ('admin','super_admin') AND admin.status='active'
WHERE customer_session.token_hash=$2 AND customer_session.expires_at>now()`, tokenHash(adminCookie.Value), tokenHash(current.Value)).Scan(&adminID, &customerID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "not_impersonating", "客户身份登录已失效")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(current.Value)); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, adminID, "auth.impersonation_ended", "user", customerID, map[string]any{"customer_id": customerID}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.setSessionCookie(w, "zjmfgo_session", adminCookie.Value, 86400)
	s.clearSessionCookie(w, adminSessionCookie)
	write(w, http.StatusOK, map[string]any{"restored": true, "admin_id": adminID})
}
