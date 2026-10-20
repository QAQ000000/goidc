package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
)

type impersonationKeyEvidence struct {
	keys   string
	audits int64
}

func keyEvidenceForImpersonation(t *testing.T, db *store.Store, owner int64) impersonationKeyEvidence {
	t.Helper()
	var out impersonationKeyEvidence
	if err := db.Pool.QueryRow(t.Context(), `SELECT
 (SELECT coalesce(jsonb_agg(to_jsonb(k) ORDER BY k.id),'[]'::jsonb)::text FROM api_keys k WHERE k.user_id=$1),
 (SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='api_key.created')`, owner).Scan(&out.keys, &out.audits); err != nil {
		t.Fatal(err)
	}
	return out
}

func customerKeyDelegation(t *testing.T, h http.Handler, owner int64, cookies string) string {
	t.Helper()
	started := portalRequest(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/impersonate", owner), nil, cookies, "admin")
	expect(t, started, http.StatusOK)
	issued := started.Result().Cookies()
	if len(issued) != 1 || issued[0].Name != customerSessionCookie {
		t.Fatal("delegation did not issue exactly one customer cookie")
	}
	return customerSessionCookie + "=" + issued[0].Value
}

func expectDelegatedKeyDenied(t *testing.T, result *httptest.ResponseRecorder) {
	t.Helper()
	expect(t, result, http.StatusForbidden)
	if errorData(t, result)["code"] != "impersonation_credential_forbidden" {
		t.Fatal("delegated key request returned the wrong error")
	}
	if strings.Contains(result.Body.String(), `"secret"`) {
		t.Fatal("delegated key rejection returned a secret")
	}
}

func TestImpersonationCannotCreateIndependentAPIKey(t *testing.T) {
	db, h := testServer(t)
	aid, admin := user(t, db, h, "delegated-key-admin", "admin")
	owner, customer := user(t, db, h, "delegated-key-customer", "customer")
	// Existing customer credentials retain their exact records and remain usable.
	previous := portalRequest(h, "POST", "/api/v1/api-keys", map[string]string{"name": "independent-existing-key"}, customer, "customer")
	expect(t, previous, http.StatusCreated)
	previousSecret := data(t, previous)["secret"].(string)
	before := keyEvidenceForImpersonation(t, db, owner)
	child := customerKeyDelegation(t, h, owner, admin+"; "+customer)
	for _, tc := range []struct {
		name, cookies, expires string
	}{
		{"no_expiry_child_only", child, ""},
		{"no_expiry_dual_cookie", admin + "; " + child, ""},
		{"short_expiry", child, time.Now().UTC().Add(time.Minute).Format(time.RFC3339)},
		{"long_expiry", admin + "; " + child, time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]string{"name": "delegated-key-" + tc.name}
			if tc.expires != "" {
				body["expires_at"] = tc.expires
			}
			expectDelegatedKeyDenied(t, portalRequest(h, "POST", "/api/v1/api-keys", body, tc.cookies, "customer"))
			if keyEvidenceForImpersonation(t, db, owner) != before {
				t.Fatal("delegated key denial changed existing credentials or key audit evidence")
			}
		})
	}
	// A request that passed HTTP authentication cannot mint a key after waiting
	// for the account row, even when its administrator parent is revoked meanwhile.
	blocker, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), "SELECT id FROM users WHERE id=$1 FOR UPDATE", owner); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- portalRequest(h, "POST", "/api/v1/api-keys", map[string]string{"name": "delegated-key-after-lock"}, child, "customer")
	}()
	waitQueryLock(t, db, "SELECT role FROM users WHERE id=$1 AND status='active' FOR UPDATE")
	if _, err = db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1 AND token_hash=$2", aid, tokenHash(cookieValue(admin))); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		expectDelegatedKeyDenied(t, result)
	case <-time.After(7 * time.Second):
		t.Fatal("delegated key request stalled after account lock release")
	}
	if keyEvidenceForImpersonation(t, db, owner) != before {
		t.Fatal("delegated key denial changed credentials or key audit evidence after parent revocation")
	}
	expect(t, portalRequest(h, "POST", "/api/v1/auth/impersonation/exit", nil, admin+"; "+child, "customer"), http.StatusOK)
	expect(t, portalRequest(h, "GET", "/api/v1/cart", nil, child, "customer"), http.StatusUnauthorized)
	expect(t, portalRequest(h, "GET", "/api/v1/cart", nil, customer, "customer"), http.StatusOK)
	expect(t, requestBearer(h, "GET", "/api/v1/cart", nil, previousSecret), http.StatusOK)
}

func TestImpersonationAPIKeyGuardUsesExactSelectedSession(t *testing.T) {
	db, h := testServer(t)
	aid, admin := user(t, db, h, "selected-key-admin", "super_admin")
	owner, customer := user(t, db, h, "selected-key-customer", "customer")
	child := customerKeyDelegation(t, h, owner, admin+"; "+customer)
	for _, tc := range []struct {
		name, cookies, portal string
		owner                 int64
	}{
		{"genuine_customer_with_admin_cookie", admin + "; " + customer, "", owner},
		{"genuine_customer_explicit", customer + "; " + admin, "customer", owner},
		{"administrator_with_delegated_customer_cookie", admin + "; " + child, "admin", aid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created := portalRequest(h, "POST", "/api/v1/api-keys", map[string]string{"name": tc.name}, tc.cookies, tc.portal)
			expect(t, created, http.StatusCreated)
			id := int64(data(t, created)["id"].(float64))
			var actualOwner int64
			var unbounded bool
			if err := db.Pool.QueryRow(t.Context(), "SELECT user_id,expires_at IS NULL FROM api_keys WHERE id=$1", id).Scan(&actualOwner, &unbounded); err != nil || actualOwner != tc.owner || !unbounded {
				t.Fatal("independent key issuance changed its owner or expiry contract", err)
			}
		})
	}
	// The protection belongs to the store transaction, not only the HTTP handler.
	before := keyEvidenceForImpersonation(t, db, owner)
	_, err := db.CreateAccountAPIKey(t.Context(), owner, tokenHash(cookieValue(child)), store.AccountAPIKeyInput{Name: "direct-delegated", Prefix: "direct-delegated", SecretHash: tokenHash("direct-delegated-test-secret")})
	var denied *store.AccountBusinessError
	if !errors.As(err, &denied) || denied.Status != http.StatusForbidden || denied.Code != "impersonation_credential_forbidden" {
		t.Fatal("store allowed delegation to create an independent API key")
	}
	if keyEvidenceForImpersonation(t, db, owner) != before {
		t.Fatal("direct delegated call changed credentials or key audit evidence")
	}
}

func TestImpersonationAPIKeyBindingLookupFailsClosed(t *testing.T) {
	db, h := testServer(t)
	aid, _ := user(t, db, h, "key-lookup-admin", "super_admin")
	owner, customer := user(t, db, h, "key-lookup-customer", "customer")
	hash := tokenHash(cookieValue(customer))
	// Malformed stored binding evidence fails only its typed lookup. Audit inserts
	// remain healthy, so this catches treating a lookup error as "no binding".
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload)
 VALUES($1,'auth.impersonation_bound','impersonation_session',$2,
 jsonb_build_object('customer_id','malformed-customer-id'::text,'admin_session_hash','lookup-test-parent'))`, aid, hash); err != nil {
		t.Fatal(err)
	}
	before := keyEvidenceForImpersonation(t, db, owner)
	_, err := db.CreateAccountAPIKey(t.Context(), owner, hash, store.AccountAPIKeyInput{Name: "lookup-failure", Prefix: "lookup-failure", SecretHash: tokenHash("lookup-failure-test-secret")})
	var failure *pgconn.PgError
	if !errors.As(err, &failure) || failure.Code != "22P02" {
		t.Fatal("binding lookup failure did not fail closed")
	}
	if keyEvidenceForImpersonation(t, db, owner) != before {
		t.Fatal("binding lookup failure changed credentials or key audit evidence")
	}
}
