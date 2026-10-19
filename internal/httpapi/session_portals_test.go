package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// An explicit empty header cancels the legacy fixture's admin-page convenience:
// these requests exercise the real default customer audience, including dual cookies.
func portalRequest(h http.Handler, method, path string, body any, cookies, portal string) *httptest.ResponseRecorder {
	return requestFrom(h, method, path, body, "192.0.2.10:1234", cookies, map[string]string{authPortalHeader: portal})
}
func TestPortalSessionCookiesCoexistAndLogoutIndependently(t *testing.T) {
	db, h := testServer(t)
	aid, admin := user(t, db, h, "portal-cookie-admin", "super_admin")
	cid, customer := user(t, db, h, "portal-cookie-customer", "customer")
	dual := admin + "; " + customer
	for _, portal := range []string{"customer", "admin"} {
		body := map[string]string{"portal": portal, "password": "integration-password"}
		if portal == "admin" {
			body["username"] = "portal-cookie-admin"
		} else {
			body["identifier"] = "portal-cookie-customer@example.test"
		}
		login := portalRequest(h, "POST", "/api/v1/auth/login", body, dual, portal)
		expect(t, login, 200)
		cookies := login.Result().Cookies()
		name := customerSessionCookie
		if portal == "admin" {
			name = adminSessionCookie
		}
		if len(cookies) != 1 || cookies[0].Name != name || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
			t.Fatalf("login overwrote another audience: %v", cookies)
		}
	}
	check := func(portal string, id int64) {
		t.Helper()
		res := portalRequest(h, "GET", "/api/v1/me", nil, dual, portal)
		expect(t, res, 200)
		if int64(data(t, res)["id"].(float64)) != id {
			t.Fatal("wrong identity", res.Body.String())
		}
	}
	check("", cid)
	check("admin", aid)
	logout := portalRequest(h, "POST", "/api/v1/auth/logout", nil, dual, "")
	expect(t, logout, 200)
	cleared := logout.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != customerSessionCookie || cleared[0].MaxAge != -1 {
		t.Fatal("customer logout cleared wrong cookie")
	}
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, dual, ""), 401)
	check("admin", aid)
	// A fresh independent customer session survives administrator logout as well.
	_, customer = user(t, db, h, "portal-cookie-another", "customer")
	dual = admin + "; " + customer
	logout = portalRequest(h, "POST", "/api/v1/auth/logout", nil, dual, "admin")
	expect(t, logout, 200)
	if cookies := logout.Result().Cookies(); len(cookies) != 1 || cookies[0].Name != adminSessionCookie {
		t.Fatal("admin logout cleared wrong cookie")
	}
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, dual, "admin"), 401)
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, dual, ""), 200)
}
func TestPortalSessionCredentialsNeverFallbackAcrossCookies(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "portal-no-fallback-admin", "super_admin")
	cid, customer := user(t, db, h, "portal-no-fallback-customer", "customer")
	for _, cookie := range []string{admin, customerSessionCookie + "=" + cookieValue(admin), adminSessionCookie + "=" + cookieValue(customer), admin + "; " + customerSessionCookie + "=invalid"} {
		expect(t, portalRequest(h, "GET", "/api/v1/cart", nil, cookie, ""), 401)
		expect(t, portalRequest(h, "POST", "/api/v1/orders", map[string]any{}, cookie, "admin"), 401)
	}
	expect(t, portalRequest(h, "GET", "/api/v1/admin/dashboard", nil, customer, "customer"), 401)
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, admin, ""), 401)
	for _, dual := range []string{admin + "; " + customer, customer + "; " + admin} {
		res := portalRequest(h, "GET", "/api/v1/me", nil, dual, "")
		expect(t, res, 200)
		if int64(data(t, res)["id"].(float64)) != cid {
			t.Fatal("cookie order changed customer identity")
		}
		expect(t, portalRequest(h, "GET", "/api/v1/cart", nil, dual, "admin"), 200)
		expect(t, portalRequest(h, "GET", "/api/v1/admin/dashboard", nil, dual, "customer"), 200)
	}
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, admin, "invalid"), 400)
}
func TestPortalSessionSharedAccountUsesExplicitAudience(t *testing.T) {
	db, h := testServer(t)
	aid, admin := user(t, db, h, "portal-shared-admin", "super_admin")
	cid, customer := user(t, db, h, "portal-shared-customer", "customer")
	dual := admin + "; " + customer
	for _, tc := range []struct {
		portal string
		id     int64
	}{{"", cid}, {"customer", cid}, {"admin", aid}} {
		res := portalRequest(h, "POST", "/api/v1/api-keys", map[string]any{"name": "portal-key"}, dual, tc.portal)
		expect(t, res, 201)
		var owner int64
		if err := db.Pool.QueryRow(t.Context(), "SELECT user_id FROM api_keys WHERE id=$1", int64(data(t, res)["id"].(float64))).Scan(&owner); err != nil || owner != tc.id {
			t.Fatal("API key selected wrong account", owner, err)
		}
	}
	res := portalRequest(h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "admin-audience-password"}, dual, "admin")
	expect(t, res, 200)
	for _, portal := range []string{"customer", "admin"} {
		expect(t, portalRequest(h, "GET", "/api/v1/me", nil, dual, portal), 200)
	}
	login := portalRequest(h, "POST", "/api/v1/auth/login", map[string]string{"identifier": "portal-shared-customer@example.test", "password": "integration-password", "portal": "customer"}, dual, "")
	expect(t, login, 200)
}
func TestPortalSessionMFAIssuesSeparateCookie(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "portal-mfa-cookie-admin", "super_admin")
	_, customer := user(t, db, h, "portal-mfa-cookie-customer", "customer")
	dual := admin + "; " + customer
	for _, portal := range []string{"admin", "customer"} {
		enroll := portalRequest(h, "POST", "/api/v1/account/mfa/enroll", map[string]any{}, dual, portal)
		expect(t, enroll, 200)
		secret := data(t, enroll)["secret"].(string)
		recovery := data(t, enroll)["recovery_codes"].([]any)[0].(string)
		expect(t, portalRequest(h, "POST", "/api/v1/account/mfa/confirm", map[string]string{"code": testTOTP(secret, time.Now())}, dual, portal), 200)
		body := map[string]string{"portal": portal, "password": "integration-password"}
		name := customerSessionCookie
		if portal == "admin" {
			body["username"] = "portal-mfa-cookie-admin"
			name = adminSessionCookie
		} else {
			body["identifier"] = "portal-mfa-cookie-customer@example.test"
		}
		login := portalRequest(h, "POST", "/api/v1/auth/login", body, dual, portal)
		expect(t, login, 401)
		complete := portalRequest(h, "POST", "/api/v1/auth/login/mfa", map[string]string{"portal": portal, "challenge": errorData(t, login)["challenge"].(string), "code": recovery}, dual, portal)
		expect(t, complete, 200)
		if cookies := complete.Result().Cookies(); len(cookies) != 1 || cookies[0].Name != name {
			t.Fatal("MFA overwrote other audience", cookies)
		}
	}
}
func TestPortalSessionImpersonationRequiresBoundSession(t *testing.T) {
	db, h := testServer(t)
	aid, admin := user(t, db, h, "portal-bound-admin", "admin")
	cid, customer := user(t, db, h, "portal-bound-customer", "customer")
	dual := admin + "; " + customer
	status := portalRequest(h, "GET", "/api/v1/auth/impersonation", nil, dual, "")
	expect(t, status, 200)
	if data(t, status)["active"] != false {
		t.Fatal("independent sessions misclassified")
	}
	expect(t, portalRequest(h, "POST", "/api/v1/auth/impersonation/exit", nil, dual, ""), 409)
	expect(t, portalRequest(h, "GET", "/api/v1/cart", nil, dual, ""), 200)
	start := portalRequest(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/impersonate", cid), nil, dual, "admin")
	expect(t, start, 200)
	cookies := start.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != customerSessionCookie {
		t.Fatal("impersonation changed admin cookie")
	}
	impersonated := admin + "; " + customerSessionCookie + "=" + cookies[0].Value
	status = portalRequest(h, "GET", "/api/v1/auth/impersonation", nil, impersonated, "")
	expect(t, status, 200)
	if data(t, status)["active"] != true {
		t.Fatal("trusted binding inactive")
	}
	expect(t, portalRequest(h, "GET", "/api/v1/admin/dashboard", nil, impersonated, "admin"), 200)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=array_remove(permissions,'customers.manage') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", aid); err != nil {
		t.Fatal(err)
	}
	expect(t, portalRequest(h, "GET", "/api/v1/cart", nil, impersonated, ""), 401)
	// Revocation invalidates only the delegated credential; the customer's genuine
	// independent session survives, and cleanup remains possible.
	expect(t, portalRequest(h, "GET", "/api/v1/cart", nil, dual, ""), 200)
	exit := portalRequest(h, "POST", "/api/v1/auth/impersonation/exit", nil, impersonated, "")
	expect(t, exit, 200)
	expect(t, portalRequest(h, "POST", "/api/v1/auth/impersonation/exit", nil, impersonated, ""), 409)
	if cookies := exit.Result().Cookies(); len(cookies) != 1 || cookies[0].Name != customerSessionCookie || cookies[0].MaxAge != -1 {
		t.Fatal("exit changed admin session")
	}
	expect(t, portalRequest(h, "GET", "/api/v1/admin/dashboard", nil, admin, "admin"), 200)
	// Re-authorize, then leave a stale delegated customer cookie after logging out
	// of the parent. A new administrator login must be able to start delegation.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=array_append(permissions,'customers.manage') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", aid); err != nil {
		t.Fatal(err)
	}
	start = portalRequest(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/impersonate", cid), nil, dual, "admin")
	expect(t, start, 200)
	child := customerSessionCookie + "=" + start.Result().Cookies()[0].Value
	expect(t, portalRequest(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/impersonate", cid), nil, admin+"; "+child, "admin"), 409)
	expect(t, portalRequest(h, "POST", "/api/v1/auth/logout", nil, admin+"; "+child, "admin"), 200)
	expect(t, portalRequest(h, "GET", "/api/v1/cart", nil, admin+"; "+child, ""), 401)
	login := portalRequest(h, "POST", "/api/v1/auth/login", map[string]string{"username": "portal-bound-admin", "password": "integration-password", "portal": "admin"}, child, "admin")
	expect(t, login, 200)
	newAdmin := adminSessionCookie + "=" + login.Result().Cookies()[0].Value
	expect(t, portalRequest(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/impersonate", cid), nil, newAdmin+"; "+child, "admin"), 200)
}
func TestPortalSessionCSRFAndMaintenanceStayIsolated(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "portal-maint-admin", "super_admin")
	_, customer := user(t, db, h, "portal-maint-customer", "customer")
	dual := admin + "; " + customer
	r := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
	r.Header.Set("Cookie", dual)
	r.Header.Set(authPortalHeader, "admin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	expect(t, w, 403)
	for _, portal := range []string{"admin", "customer"} {
		expect(t, portalRequest(h, "GET", "/api/v1/me", nil, dual, portal), 200)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET maintenance_enabled=true WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/catalog", "/api/v1/cart", "/api/v1/orders"} {
		expect(t, portalRequest(h, "GET", path, nil, dual, "admin"), 503)
	}
	expect(t, portalRequest(h, "GET", "/api/v1/admin/dashboard", nil, dual, "admin"), 200)
	expect(t, portalRequest(h, "GET", "/api/v1/me/notification-preferences", nil, dual, "admin"), 200)
	expect(t, portalRequest(h, "GET", "/api/v1/me/notification-preferences", nil, dual, "customer"), 503)
	// An old administrator session in the customer cookie stays anonymous.
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, strings.Replace(admin, adminSessionCookie, customerSessionCookie, 1), ""), 401)
}

func TestPortalSessionPublicReadersStayCustomerOnly(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "portal-readers-admin", "super_admin")
	_, customer := user(t, db, h, "portal-readers-customer", "customer")
	article := articleInput("knowledge", "portal-private-readers", "published")
	article.AccessLevel = "login"
	aid := contentID(t, h, admin, article)
	articlePath := fmt.Sprintf("/api/v1/knowledgebase/%d", aid)
	download := downloadInput()
	download.AccessLevel = "login"
	did := downloadID(t, h, admin, download)
	downloadPath := fmt.Sprintf("/api/v1/downloads/%d/file", did)
	for _, cookies := range []string{admin, admin + "; " + customerSessionCookie + "=invalid"} {
		for _, portal := range []string{"", "admin"} {
			expect(t, portalRequest(h, "GET", articlePath, nil, cookies, portal), 404)
			expect(t, portalRequest(h, "GET", downloadPath, nil, cookies, portal), 401)
			list := portalRequest(h, "GET", "/api/v1/knowledgebase", nil, cookies, portal)
			expect(t, list, 200)
			if data(t, list)["total"] != float64(0) {
				t.Fatal("staff session exposed customer article")
			}
			list = portalRequest(h, "GET", "/api/v1/downloads", nil, cookies, portal)
			expect(t, list, 200)
			if data(t, list)["items"].([]any)[0].(map[string]any)["can_download"] != false {
				t.Fatal("staff session granted customer download")
			}
		}
	}
	dual := admin + "; " + customer
	expect(t, portalRequest(h, "GET", articlePath, nil, dual, "admin"), 200)
	expect(t, portalRequest(h, "GET", downloadPath, nil, dual, "admin"), 200)
	expect(t, portalRequest(h, "GET", fmt.Sprintf("/api/v1/admin/content/%d", aid), nil, dual, ""), 200)
	expect(t, portalRequest(h, "GET", fmt.Sprintf("/api/v1/admin/downloads/%d", did), nil, dual, ""), 200)
}
