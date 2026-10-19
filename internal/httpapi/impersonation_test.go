package httpapi

import (
	"fmt"
	"net/http"
	"testing"
)

func TestCustomerImpersonationSessionSwitchAndRestore(t *testing.T) {
	db, h := testServer(t)
	adminID, adminCookie := user(t, db, h, "impersonation-admin", "super_admin")
	customerID, _ := user(t, db, h, "impersonation-customer", "customer")

	started := request(h, http.MethodPost, fmt.Sprintf("/api/v1/admin/customers/%d/impersonate", customerID), nil, adminCookie, "")
	expect(t, started, http.StatusOK)
	var customerToken string
	adminToken := cookieValue(adminCookie)
	for _, cookie := range started.Result().Cookies() {
		switch cookie.Name {
		case "zjmfgo_session":
			customerToken = cookie.Value
		case adminSessionCookie:
			adminToken = cookie.Value
		}
	}
	if customerToken == "" || len(started.Result().Cookies()) != 1 {
		t.Fatalf("impersonation cookies not issued correctly: session=%t admin=%t", customerToken != "", adminToken != "")
	}
	customerSession := "zjmfgo_session=" + customerToken + "; " + adminSessionCookie + "=" + adminToken
	me := request(h, http.MethodGet, "/api/v1/me", nil, customerSession, "")
	expect(t, me, http.StatusOK)
	if int64(data(t, me)["id"].(float64)) != customerID || data(t, me)["role"] != "customer" {
		t.Fatalf("impersonated identity is wrong: %s", me.Body.String())
	}
	status := request(h, http.MethodGet, "/api/v1/auth/impersonation", nil, customerSession, "")
	expect(t, status, http.StatusOK)
	if data(t, status)["active"] != true {
		t.Fatalf("impersonation status is inactive: %s", status.Body.String())
	}

	exited := request(h, http.MethodPost, "/api/v1/auth/impersonation/exit", nil, customerSession, "")
	expect(t, exited, http.StatusOK)
	var restored string
	for _, cookie := range exited.Result().Cookies() {
		if cookie.Name == "zjmfgo_session" {
			restored = cookie.Value
		}
	}
	if restored != "" {
		t.Fatalf("administrator session was not restored")
	}
	restoredMe := request(h, http.MethodGet, "/api/v1/me", "", adminCookie, "")
	expect(t, restoredMe, http.StatusOK)
	if int64(data(t, restoredMe)["id"].(float64)) != adminID || data(t, restoredMe)["role"] != "super_admin" {
		t.Fatalf("restored identity is wrong: %s", restoredMe.Body.String())
	}
	var startedAudit, endedAudit int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE action='auth.impersonation_started'),count(*) FILTER (WHERE action='auth.impersonation_ended') FROM audit_events WHERE entity_id=$1`, fmt.Sprint(customerID)).Scan(&startedAudit, &endedAudit); err != nil {
		t.Fatal(err)
	}
	if startedAudit != 1 || endedAudit != 1 {
		t.Fatalf("impersonation audit incomplete: started=%d ended=%d", startedAudit, endedAudit)
	}
}

func cookieValue(cookie string) string {
	for i := 0; i < len(cookie); i++ {
		if cookie[i] == '=' {
			end := len(cookie)
			for j := i + 1; j < len(cookie); j++ {
				if cookie[j] == ';' {
					end = j
					break
				}
			}
			return cookie[i+1 : end]
		}
	}
	return ""
}
