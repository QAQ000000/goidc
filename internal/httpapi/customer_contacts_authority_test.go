package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

type contactAuthorityFixture struct {
	adminBalanceFixture
	contact int64
}

func newContactAuthorityFixture(t *testing.T) contactAuthorityFixture {
	t.Helper()
	f := contactAuthorityFixture{adminBalanceFixture: newAdminBalanceFixture(t)}
	if err := f.db.Pool.QueryRow(t.Context(), "INSERT INTO customer_contacts(customer_id,username,email,status) VALUES($1,'original','contact-authority@example.test','active') RETURNING id", f.customer).Scan(&f.contact); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f contactAuthorityFixture) callContact(method string, body any, key, secret string, customer bool) *httptest.ResponseRecorder {
	cookie := f.cookie
	path := fmt.Sprintf("/api/v1/admin/customers/%d/contacts", f.customer)
	if customer {
		cookie = f.customerAuth
		path = "/api/v1/contacts"
	}
	if method == "PATCH" || method == "DELETE" {
		path += fmt.Sprintf("/%d", f.contact)
	}
	headers := map[string]string{}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	if secret != "" {
		cookie = ""
		headers["Authorization"] = "Bearer " + secret
	}
	return requestFrom(f.h, method, path, body, "192.0.2.87:5678", cookie, headers)
}
func (f contactAuthorityFixture) snapshot(t *testing.T) string {
	t.Helper()
	var out string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'contacts',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY id),'[]'::jsonb) FROM customer_contacts c),
 'audits',(SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY id),'[]'::jsonb) FROM audit_events a WHERE action LIKE 'customer_contact.%'),
 'receipts',(SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY actor_id,scope,idempotency_key),'[]'::jsonb) FROM mutation_operations m WHERE scope LIKE 'customer_contact.%'))::text`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}
func (f contactAuthorityFixture) unchanged(t *testing.T, before string) {
	t.Helper()
	if got := f.snapshot(t); got != before {
		t.Fatalf("rejected contact operation changed rows/audit/receipt\nbefore=%s\nafter=%s", before, got)
	}
}
func TestCustomerContactsVersionReplayAndIsolation(t *testing.T) {
	f := newContactAuthorityFixture(t)
	create := map[string]any{"username": "billing", "email": "contact-new@example.test", "invoice_emails": true, "status": "active"}
	const key = "contact-create-stable-key"
	first := f.callContact("POST", create, key, "", false)
	expect(t, first, 201)
	id := int64(data(t, first)["id"].(float64))
	replay := f.callContact("POST", create, key, "", false)
	expect(t, replay, 200)
	if data(t, replay)["id"] != float64(id) || data(t, replay)["version"] != float64(1) || data(t, replay)["replayed"] != true {
		t.Fatal("create did not return original durable receipt", replay.Body.String())
	}
	expect(t, f.callContact("POST", map[string]any{"email": "other-new@example.test"}, key, "", false), 409)
	listed := f.callContact("GET", nil, "", "", false)
	expect(t, listed, 200)
	if len(listData(t, listed)) != 2 || listData(t, listed)[0]["version"] != float64(1) {
		t.Fatal("list has no contact version", listed.Body.String())
	}
	body := map[string]any{"username": "updated", "expected_version": 1}
	expect(t, f.callContact("PATCH", body, "contact-update-stable-key", "", true), 200)
	before := f.snapshot(t)
	updatedReplay := f.callContact("PATCH", body, "contact-update-stable-key", "", true)
	expect(t, updatedReplay, 200)
	if data(t, updatedReplay)["version"] != float64(2) || data(t, updatedReplay)["replayed"] != true {
		t.Fatal(updatedReplay.Body.String())
	}
	f.unchanged(t, before)
	expect(t, f.callContact("PATCH", body, "", "", false), 409)
	expect(t, f.callContact("DELETE", map[string]any{"expected_version": 1}, "", "", true), 409)
	f.unchanged(t, before)
	// Compatibility requests retain their old contract while every successful
	// write increments the version used by new clients.
	expect(t, f.callContact("PATCH", map[string]any{"support_emails": true}, "", "", true), 200)
	expect(t, f.callContact("DELETE", map[string]any{"expected_version": 3}, "contact-delete-stable-key", "", true), 200)
	deleted := f.snapshot(t)
	expect(t, f.callContact("DELETE", map[string]any{"expected_version": 3}, "contact-delete-stable-key", "", true), 200)
	f.unchanged(t, deleted)
	_, other := user(t, f.db, f.h, "contact-foreign-customer", "customer")
	foreignPath := fmt.Sprintf("/api/v1/contacts/%d", id)
	expect(t, request(f.h, "PATCH", foreignPath, map[string]any{"username": "foreign"}, other, ""), 404)
	// An administrator's original cookie never becomes a customer credential.
	if w := request(f.h, "POST", "/api/v1/contacts", create, f.cookie, ""); w.Code < 400 {
		t.Fatal("administrator cookie accepted as ordinary customer", w.Body.String())
	}
	before = f.snapshot(t)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=array_remove(permissions,'customers.manage') WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", f.actor); err != nil {
		t.Fatal(err)
	}
	expect(t, f.callContact("POST", create, key, "", false), 403)
	f.unchanged(t, before)
}
func waitContactBlockedCount(t *testing.T, db *store.Store, blocker uint32, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	for {
		var count int
		err := db.Pool.QueryRow(ctx, `WITH RECURSIVE waiters AS (
   SELECT pid FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid))
   UNION SELECT a.pid FROM pg_stat_activity a JOIN waiters w ON w.pid=ANY(pg_blocking_pids(a.pid))
  ) SELECT count(*) FROM waiters`, blocker).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count >= want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("contact requests did not reach the held row lock", count, want)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func TestCustomerContactsConcurrentVersionAndAuditRollback(t *testing.T) {
	f := newContactAuthorityFixture(t)
	blocker, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), "SELECT id FROM customer_contacts WHERE id=$1 FOR UPDATE", f.contact); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 2)
	for _, name := range []string{"writer-a", "writer-b"} {
		go func(name string) {
			done <- f.callContact("PATCH", map[string]any{"username": name, "expected_version": 1}, "", "", false)
		}(name)
	}
	waitContactBlockedCount(t, f.db, blocker.Conn().PgConn().PID(), 2)
	if err = blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	a, b := awaitBalanceResponse(t, done), awaitBalanceResponse(t, done)
	if !((a.Code == 200 && b.Code == 409) || (a.Code == 409 && b.Code == 200)) {
		t.Fatal("both stale editors committed or neither succeeded", a.Code, b.Code, a.Body.String(), b.Body.String())
	}
	var version, audits int
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT version,(SELECT count(*) FROM audit_events WHERE action='customer_contact.updated') FROM customer_contacts WHERE id=$1", f.contact).Scan(&version, &audits); err != nil || version != 2 || audits != 1 {
		t.Fatal("CAS changed more than one business fact", version, audits, err)
	}
	if _, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_contact_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'customer_contact.%' THEN RAISE EXCEPTION 'contact audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_contact_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_contact_audit()`); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		body := map[string]any{"expected_version": 2}
		if method == "POST" {
			body = map[string]any{"email": "rollback-contact@example.test"}
		}
		out := f.callContact(method, body, "contact-rollback-"+method, "", false)
		expect(t, out, 500)
		f.unchanged(t, before)
	}
}
func TestCustomerContactsRevocationAfterAuthorityWait(t *testing.T) {
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		for _, scenario := range []string{"permission", "session", "api_key", "scope"} {
			t.Run(method+"/"+scenario, func(t *testing.T) {
				f := newContactAuthorityFixture(t)
				secret := ""
				if scenario == "api_key" {
					secret = f.apiKey(t)
				}
				before := f.snapshot(t)
				blocker, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback(context.Background())
				if _, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(70108190)"); err != nil {
					t.Fatal(err)
				}
				body := map[string]any{"expected_version": 1}
				if method == "POST" {
					body = map[string]any{"email": "revoked-contact@example.test"}
				}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- f.callContact(method, body, "contact-revoked-"+method, secret, false) }()
				waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
				status := 401
				switch scenario {
				case "permission":
					permission := "customers.manage"
					if method == "GET" {
						permission = "customers.view"
					}
					_, err = blocker.Exec(t.Context(), "UPDATE admin_roles SET permissions=array_remove(permissions,$2) WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", f.actor, permission)
					status = 403
				case "scope":
					_, err = blocker.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor)
					status = 404
				case "session":
					_, err = blocker.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(f.cookie)))
				case "api_key":
					_, err = blocker.Exec(t.Context(), "UPDATE api_keys SET revoked_at=clock_timestamp() WHERE secret_hash=$1", tokenHash(secret))
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = blocker.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				expect(t, awaitBalanceResponse(t, done), status)
				f.unchanged(t, before)
			})
		}
	}
}
func waitContactCredentialExpiry(t *testing.T, f contactAuthorityFixture, table, column, hash string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	for {
		var expired bool
		if err := f.db.Pool.QueryRow(ctx, "SELECT expires_at<=clock_timestamp() FROM "+table+" WHERE "+column+"=$1", hash).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("contact fixture credential never expired")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func TestCustomerContactsCredentialExpiryAtEveryWait(t *testing.T) {
	for _, credential := range []string{"session", "api_key", "customer_session"} {
		for _, stage := range []string{"credential", "contact", "audit", "receipt"} {
			t.Run(credential+"/"+stage, func(t *testing.T) {
				f := newContactAuthorityFixture(t)
				customer := credential == "customer_session"
				secret := ""
				table, column, hash := "sessions", "token_hash", tokenHash(cookieValue(f.cookie))
				if credential == "api_key" {
					secret = f.apiKey(t)
					table, column, hash = "api_keys", "secret_hash", tokenHash(secret)
				}
				actor := f.actor
				if customer {
					actor = f.customer
					hash = tokenHash(cookieValue(f.customerAuth))
				}
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()+interval '1 second' WHERE "+column+"=$1", hash); err != nil {
					t.Fatal(err)
				}
				before := f.snapshot(t)
				blocker, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback(context.Background())
				key := "contact-expiry-stable-key"
				switch stage {
				case "credential":
					_, err = blocker.Exec(t.Context(), "SELECT user_id FROM "+table+" WHERE "+column+"=$1 FOR UPDATE", hash)
				case "contact":
					_, err = blocker.Exec(t.Context(), "SELECT id FROM customer_contacts WHERE id=$1 FOR UPDATE", f.contact)
				case "receipt":
					_, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("mutation:%d:customer_contact.update.%d:%d:%s", actor, f.contact, f.customer, key))
				case "audit":
					_, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('contact-audit-expiry',0))")
					if err == nil {
						_, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION wait_contact_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='customer_contact.updated' THEN PERFORM pg_advisory_xact_lock(hashtextextended('contact-audit-expiry',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER wait_contact_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_contact_audit()`)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					done <- f.callContact("PATCH", map[string]any{"username": "must-not-commit", "expected_version": 1}, key, secret, customer)
				}()
				waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
				waitContactCredentialExpiry(t, f, table, column, hash)
				if err = blocker.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				expect(t, awaitBalanceResponse(t, done), 401)
				f.unchanged(t, before)
			})
		}
	}
}
