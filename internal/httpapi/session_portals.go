package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

const customerSessionCookie = "zjmfgo_session"
const adminSessionCookie = "zjmfgo_admin_session"
const authPortalHeader = "X-Auth-Portal"

type browserSessionContextKey struct{}
type browserPortalContextKey struct{}
type browserSessionResolution struct {
	identity store.HTTPIdentity
	err      error
}
type browserSessionState struct {
	customer, admin *http.Cookie
	resolved        map[string]browserSessionResolution
}

func sessionCookieForRole(role string) string {
	if store.LoginPortalForRole(role) == "admin" {
		return adminSessionCookie
	}
	return customerSessionCookie
}
func requestPortal(r *http.Request) string {
	portal, _ := r.Context().Value(browserPortalContextKey{}).(string)
	if portal == "admin" {
		return portal
	}
	return "customer"
}
func originalSessionCookie(r *http.Request, portal string) (*http.Cookie, error) {
	if state, ok := r.Context().Value(browserSessionContextKey{}).(*browserSessionState); ok {
		cookie := state.customer
		if portal == "admin" {
			cookie = state.admin
		}
		if cookie != nil && cookie.Value != "" {
			return cookie, nil
		}
		return nil, http.ErrNoCookie
	}
	name := customerSessionCookie
	if portal == "admin" {
		name = adminSessionCookie
	}
	return r.Cookie(name)
}

// Old business handlers pass the active browser session hash to Store admission
// checks via zjmfgo_session. Project only the selected portal into this internal
// request; retain the original browser cookies in an unforgeable context value.
// No response cookie is produced and the other portal is never a fallback.
func (s *Server) projectBrowserSession(r *http.Request, portal string) *http.Request {
	state, ok := r.Context().Value(browserSessionContextKey{}).(*browserSessionState)
	if !ok {
		customer, _ := r.Cookie(customerSessionCookie)
		admin, _ := r.Cookie(adminSessionCookie)
		state = &browserSessionState{customer: customer, admin: admin, resolved: map[string]browserSessionResolution{}}
	}
	ctx := context.WithValue(r.Context(), browserSessionContextKey{}, state)
	ctx = context.WithValue(ctx, browserPortalContextKey{}, portal)
	projected := r.Clone(ctx)
	projected.Header = r.Header.Clone()
	projected.Header.Del("Cookie")
	for _, cookie := range r.Cookies() {
		if cookie.Name != customerSessionCookie && cookie.Name != adminSessionCookie {
			projected.AddCookie(cookie)
		}
	}
	cookie := state.customer
	if portal == "admin" {
		cookie = state.admin
	}
	if cookie == nil || cookie.Value == "" {
		return projected
	}
	resolved, exists := state.resolved[portal]
	if !exists {
		resolved.identity, resolved.err = s.db.SessionIdentity(ctx, tokenHash(cookie.Value))
		if resolved.err == nil && store.LoginPortalForRole(resolved.identity.Role) != portal {
			resolved.err = pgx.ErrNoRows
		}
		state.resolved[portal] = resolved
	}
	if resolved.err == nil {
		projected.AddCookie(&http.Cookie{Name: customerSessionCookie, Value: cookie.Value})
	}
	return projected
}
func browserIdentity(r *http.Request) (store.HTTPIdentity, error) {
	state, ok := r.Context().Value(browserSessionContextKey{}).(*browserSessionState)
	if !ok {
		return store.HTTPIdentity{}, pgx.ErrNoRows
	}
	resolved, ok := state.resolved[requestPortal(r)]
	if !ok {
		return store.HTTPIdentity{}, pgx.ErrNoRows
	}
	return resolved.identity, resolved.err
}
func sharedAccountPath(path string) bool {
	return path == "/api/v1/me" || strings.HasPrefix(path, "/api/v1/me/") || path == "/api/v1/account/password" || path == "/api/v1/account/mfa" || strings.HasPrefix(path, "/api/v1/account/mfa/") || path == "/api/v1/api-keys" || strings.HasPrefix(path, "/api/v1/api-keys/") || path == "/api/v1/auth/logout" || strings.HasPrefix(path, "/api/v1/tickets/")
}
func selectedBrowserPortal(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/api/v1/admin/") {
		return "admin"
	}
	if r.URL.Path == "/api/v1/auth/impersonation" || r.URL.Path == "/api/v1/auth/impersonation/exit" {
		return "customer"
	}
	if sharedAccountPath(r.URL.Path) && r.Header.Get(authPortalHeader) == "admin" {
		return "admin"
	}
	return "customer"
}
func browserSessionMatches(r *http.Request, portal string) bool {
	id, err := browserIdentity(r)
	return err == nil && store.LoginPortalForRole(id.Role) == portal
}
