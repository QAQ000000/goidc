package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type catalogAuthorityFixture struct {
	adminBalanceFixture
	operation, path, key, scope, action string
	body                                any
	success                             int
}

func newCatalogAuthorityFixture(t *testing.T, operation string) catalogAuthorityFixture {
	t.Helper()
	f := catalogAuthorityFixture{adminBalanceFixture: newAdminBalanceFixture(t), operation: operation, key: "catalog-original-intent", success: 201}
	source := managedProductID(t, f.h, f.cookie, productInput("catalog-source", nil))
	switch operation {
	case "create":
		f.path, f.scope, f.action = "/api/v1/admin/products", "admin:product.create", "product.created"
		f.body = productInput("catalog-authority-created", nil)
	case "duplicate":
		f.path, f.scope, f.action = fmt.Sprintf("/api/v1/admin/products/%d/duplicate", source), "admin:product.duplicate", "product.duplicated"
		f.body = map[string]any{"name": "Catalog copy", "slug": "catalog-authority-copy"}
	case "pricing":
		f.path, f.action, f.success = fmt.Sprintf("/api/v1/admin/products/%d/pricing", source), "product.pricing.updated", 200
		f.body = map[string]any{"billing_cycle": "quarterly", "price_cents": 5200, "setup_fee_cents": 150, "active": true, "expected_revision": 1}
	default:
		t.Fatalf("unknown catalog fixture %q", operation)
	}
	return f
}

func (f catalogAuthorityFixture) mutate(secret string) *httptest.ResponseRecorder {
	headers := map[string]string{"Idempotency-Key": f.key}
	cookie := f.cookie
	if secret != "" {
		cookie = ""
		headers["Authorization"] = "Bearer " + secret
	}
	return requestFrom(f.h, "POST", f.path, f.body, "192.0.2.90:5678", cookie, headers)
}

func (f catalogAuthorityFixture) snapshot(t *testing.T) string {
	t.Helper()
	var out string
	err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'products',(SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY id),'[]'::jsonb) FROM products p),
 'prices',(SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY id),'[]'::jsonb) FROM product_prices p),
 'audits',(SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY id),'[]'::jsonb) FROM audit_events a WHERE actor_id=$1 AND action LIKE 'product.%'),
 'receipts',(SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY scope,idempotency_key),'[]'::jsonb) FROM mutation_operations m WHERE actor_id=$1))::text`, f.actor).Scan(&out)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f catalogAuthorityFixture) assertSnapshot(t *testing.T, before string) {
	t.Helper()
	if after := f.snapshot(t); after != before {
		t.Fatalf("rejected catalog mutation changed committed business/audit/receipt facts\nbefore=%s\nafter=%s", before, after)
	}
}

func (f catalogAuthorityFixture) credential(secret string) (string, string, string) {
	if secret != "" {
		return "api_keys", "secret_hash", tokenHash(secret)
	}
	return "sessions", "token_hash", tokenHash(cookieValue(f.cookie))
}

func TestCatalogIntentRecovery(t *testing.T) {
	for _, operation := range []string{"create", "duplicate"} {
		t.Run(operation, func(t *testing.T) {
			f := newCatalogAuthorityFixture(t, operation)
			responses := make(chan *httptest.ResponseRecorder, 2)
			for range 2 {
				go func() { responses <- f.mutate("") }()
			}
			first, second := awaitBalanceResponse(t, responses), awaitBalanceResponse(t, responses)
			expect(t, first, 201)
			expect(t, second, 201)
			id := data(t, first)["id"]
			if id != data(t, second)["id"] {
				t.Fatal("same intent created multiple products")
			}
			before := f.snapshot(t)
			// Simulate a successful operation whose response was lost. Recovery must
			// survive later product/source edits and return the original identity.
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET revision=revision+1,updated_at=now() WHERE id=$1", int64(id.(float64))); err != nil {
				t.Fatal(err)
			}
			replay := f.mutate("")
			expect(t, replay, 201)
			if data(t, replay)["id"] != id {
				t.Fatal("replay did not recover original id")
			}
			// A reused key with a new body must never claim success for the old body.
			if operation == "create" {
				in := productInput("catalog-changed-intent", nil)
				f.body = in
			} else {
				f.body = map[string]any{"name": "Changed copy", "slug": "catalog-changed-copy"}
			}
			before = f.snapshot(t)
			changed := f.mutate("")
			expect(t, changed, 409)
			if errorData(t, changed)["code"] != "idempotency_conflict" {
				t.Fatalf("wrong changed-intent error: %s", changed.Body.String())
			}
			f.assertSnapshot(t, before)
			var count int
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mutation_operations WHERE actor_id=$1 AND scope=$2 AND idempotency_key=$3", f.actor, f.scope, f.key).Scan(&count); err != nil || count != 1 {
				t.Fatalf("receipts=%d err=%v", count, err)
			}
			// Legacy callers without a key retain their original unique-slug behavior.
			f.key = ""
			expect(t, f.mutate(""), 201)
			expect(t, f.mutate(""), 409)
		})
	}
}

func TestCatalogIntentRecoveryAfterProviderRemoval(t *testing.T) {
	f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) { t.Error("catalog create/recovery contacted provider") }, false)
	in := productInput("nokvm-recoverable-intent", nil)
	in.ProvisionMode = "nokvm"
	in.DeliveryConfig = &f.delivery
	first := request(f.h, "POST", "/api/v1/admin/products", in, f.root, "catalog-provider-original-intent")
	expect(t, first, 201)
	provider, key := f.db.Provisioners, f.db.ProvisionEncryptionKey
	f.db.Provisioners = nil
	f.db.ProvisionEncryptionKey = nil
	defer func() { f.db.Provisioners = provider; f.db.ProvisionEncryptionKey = key }()
	second := request(f.h, "POST", "/api/v1/admin/products", in, f.root, "catalog-provider-original-intent")
	expect(t, second, 201)
	if data(t, second)["id"] != data(t, first)["id"] {
		t.Fatal("provider configuration change lost committed intent")
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/products", in, f.root, "catalog-provider-new-intent"), 409)
}

func TestCatalogIntentAuditRollback(t *testing.T) {
	for _, operation := range []string{"create", "duplicate"} {
		t.Run(operation, func(t *testing.T) {
			f := newCatalogAuthorityFixture(t, operation)
			before := f.snapshot(t)
			if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_catalog_intent_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='`+f.action+`' THEN RAISE EXCEPTION 'injected catalog audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_catalog_intent_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_catalog_intent_audit()`); err != nil {
				t.Fatal(err)
			}
			expect(t, f.mutate(""), 500)
			f.assertSnapshot(t, before)
			if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER reject_catalog_intent_audit ON audit_events"); err != nil {
				t.Fatal(err)
			}
			result := f.mutate("")
			expect(t, result, 201)
			again := f.mutate("")
			expect(t, again, 201)
			if data(t, result)["id"] != data(t, again)["id"] {
				t.Fatal("retry after rollback failed to persist one recoverable intent")
			}
		})
	}
}

func TestCatalogCredentialAuthorityWaits(t *testing.T) {
	for _, operation := range []string{"create", "duplicate", "pricing"} {
		for _, credential := range []string{"session", "api_key"} {
			for _, scenario := range []string{"catalog_revoked", "catalog_expiry", "credential_row_expiry", "audit_expiry", "receipt_expiry"} {
				if operation == "pricing" && scenario == "receipt_expiry" {
					continue
				}
				modes := []string{"new"}
				if operation != "pricing" && scenario != "audit_expiry" {
					modes = append(modes, "replay")
				}
				for _, mode := range modes {
					t.Run(operation+"/"+credential+"/"+scenario+"/"+mode, func(t *testing.T) {
						f := newCatalogAuthorityFixture(t, operation)
						if mode == "replay" {
							expect(t, f.mutate(""), f.success)
						}
						secret := ""
						if credential == "api_key" {
							secret = f.apiKey(t)
						}
						before := f.snapshot(t)
						blocker, err := f.db.Pool.Begin(t.Context())
						if err != nil {
							t.Fatal(err)
						}
						defer blocker.Rollback(context.Background())
						switch scenario {
						case "receipt_expiry":
							_, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("mutation:%d:%s:%s", f.actor, f.scope, f.key))
						case "audit_expiry":
							_, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('catalog-authority-audit',0))")
							if err == nil {
								_, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION wait_catalog_authority_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='`+f.action+`' THEN PERFORM pg_advisory_xact_lock(hashtextextended('catalog-authority-audit',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER wait_catalog_authority_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_catalog_authority_audit()`)
							}
						default:
							_, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(70108197)")
						}
						if err != nil {
							t.Fatal(err)
						}
						table, column, hash := f.credential(secret)
						expire := func() {
							t.Helper()
							if _, err := f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()+interval '1 second' WHERE "+column+"=$1", hash); err != nil {
								t.Fatal(err)
							}
						}
						if scenario == "receipt_expiry" || scenario == "audit_expiry" {
							expire()
						}
						done := make(chan *httptest.ResponseRecorder, 1)
						go func() { done <- f.mutate(secret) }()
						waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
						if scenario == "catalog_revoked" {
							if secret == "" {
								_, err = f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", hash)
							} else {
								_, err = f.db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=clock_timestamp() WHERE secret_hash=$1", hash)
							}
							if err != nil {
								t.Fatal(err)
							}
						} else if scenario == "catalog_expiry" || scenario == "credential_row_expiry" {
							expire()
						}
						if scenario == "credential_row_expiry" {
							credentialLock, err := f.db.Pool.Begin(t.Context())
							if err != nil {
								t.Fatal(err)
							}
							defer credentialLock.Rollback(context.Background())
							if _, err = credentialLock.Exec(t.Context(), "SELECT user_id FROM "+table+" WHERE "+column+"=$1 FOR UPDATE", hash); err != nil {
								t.Fatal(err)
							}
							if err = blocker.Commit(t.Context()); err != nil {
								t.Fatal(err)
							}
							waitUploadBlockedBy(t, f.db, credentialLock.Conn().PgConn().PID())
							waitBalanceCredentialExpiry(t, f.adminBalanceFixture, secret)
							if err = credentialLock.Commit(t.Context()); err != nil {
								t.Fatal(err)
							}
						} else {
							if scenario != "catalog_revoked" {
								waitBalanceCredentialExpiry(t, f.adminBalanceFixture, secret)
							}
							if err = blocker.Commit(t.Context()); err != nil {
								t.Fatal(err)
							}
						}
						expect(t, awaitBalanceResponse(t, done), 401)
						f.assertSnapshot(t, before)
					})
				}
			}
		}
	}
}

func TestCatalogCredentialPasswordRotationLockOrder(t *testing.T) {
	for _, operation := range []string{"create", "duplicate", "pricing"} {
		for _, credential := range []string{"session", "api_key"} {
			t.Run(operation+"/"+credential, func(t *testing.T) {
				f := newCatalogAuthorityFixture(t, operation)
				secret := ""
				if credential == "api_key" {
					secret = f.apiKey(t)
				}
				login := request(f.h, "POST", "/api/v1/auth/login", map[string]string{"username": "balance-operation-admin", "password": "integration-password", "portal": "admin"}, "", "")
				expect(t, login, 200)
				session := login.Result().Cookies()[0]
				rotationCookie := session.Name + "=" + session.Value
				blocker, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback(context.Background())
				if _, err = blocker.Exec(t.Context(), "SELECT user_id FROM sessions WHERE token_hash=$1 FOR UPDATE", tokenHash(cookieValue(rotationCookie))); err != nil {
					t.Fatal(err)
				}
				rotation := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					rotation <- portalRequest(f.h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "rotated-catalog-password"}, rotationCookie, "admin")
				}()
				waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
				before := f.snapshot(t)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- f.mutate(secret) }()
				waitQueryLock(t, f.db, "SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE")
				if err = blocker.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				expect(t, awaitBalanceResponse(t, rotation), 200)
				expect(t, awaitBalanceResponse(t, done), 401)
				// Password rotation has its own audit; inspect only catalog rows/receipts.
				f.assertSnapshot(t, before)
				var count int
				if err = f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM products WHERE slug IN ('catalog-authority-created','catalog-authority-copy')").Scan(&count); err != nil || count != 0 {
					t.Fatalf("catalog mutation survived rotation: count=%d err=%v", count, err)
				}
				if err = f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mutation_operations WHERE actor_id=$1 AND scope IN ('admin:product.create','admin:product.duplicate')", f.actor).Scan(&count); err != nil || count != 0 {
					t.Fatalf("catalog receipt survived rotation: count=%d err=%v", count, err)
				}
				if operation == "pricing" {
					var revision int
					if err = f.db.Pool.QueryRow(t.Context(), "SELECT revision FROM products WHERE slug='catalog-source'").Scan(&revision); err != nil || revision != 1 {
						t.Fatalf("pricing survived rotation: revision=%d err=%v", revision, err)
					}
				}
			})
		}
	}
}

// A revoked read-only supplier probe cannot finalize through an actor-only
// context. Its original intent stays pending; a fresh authenticated intent can
// safely re-run the read without creating any upstream business operation.
func TestCatalogSupplierProbeRevokedFinalizationRecovery(t *testing.T) {
	f := supplierSetup(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	old := f.remote.Config.Handler
	f.remote.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cart/all" {
			once.Do(func() { close(entered); <-release })
		}
		old.ServeHTTP(w, r)
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	path := fmt.Sprintf("/api/v1/admin/suppliers/%d/test-connection", f.supplierID)
	body := map[string]any{"expected_version": 1}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request(f.h, "POST", path, body, f.root, "catalog-probe-revoked-intent") }()
	select {
	case <-entered:
	case <-time.After(7 * time.Second):
		t.Fatal("probe did not reach read-only upstream")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(f.root))); err != nil {
		t.Fatal(err)
	}
	close(release)
	expect(t, awaitBalanceResponse(t, done), 401)
	var pending string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT result->>'message' FROM mutation_operations WHERE scope='admin:supplier.connection_test' AND idempotency_key='catalog-probe-revoked-intent'").Scan(&pending); err != nil || pending != "测试进行中" {
		t.Fatalf("revoked probe unexpectedly finalized: %q %v", pending, err)
	}
	login := request(f.h, "POST", "/api/v1/auth/login", map[string]string{"username": "supplier-root", "password": "integration-password", "portal": "admin"}, "", "")
	expect(t, login, 200)
	session := login.Result().Cookies()[0]
	cookie := session.Name + "=" + session.Value
	stale := request(f.h, "POST", path, body, cookie, "catalog-probe-revoked-intent")
	expect(t, stale, 409)
	if errorData(t, stale)["code"] != "supplier_test_in_progress" {
		t.Fatal("pending result was silently re-run", stale.Body.String())
	}
	recovered := request(f.h, "POST", path, body, cookie, "catalog-probe-new-authenticated-intent")
	expect(t, recovered, 200)
	if data(t, recovered)["ok"] != true {
		t.Fatal("fresh read-only intent did not recover", recovered.Body.String())
	}
	if f.creates.Load() != 0 || f.pays.Load() != 0 {
		t.Fatal("read-only recovery created or paid upstream business")
	}
}
