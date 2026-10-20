package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
)

// Exercise the existing receipt/field protocol while the original credential
// expires after an actual PostgreSQL lock wait, including the audit before commit.
func TestCustomerCustomFieldCredentialExpiryRollsBack(t *testing.T) {
	for _, op := range []string{"create", "update", "delete"} {
		for _, kind := range []string{"session", "api_key"} {
			for _, wait := range []string{"receipt", "audit"} {
				t.Run(op+"/"+kind+"/"+wait, func(t *testing.T) {
					f := newAdminBalanceFixture(t)
					body := map[string]any{"field_key": "authority_field", "label": "Authority", "field_type": "text", "active": true, "choices": []string{}}
					method, path, operation := "POST", "/api/v1/admin/customer-fields", "customer_custom_field.create"
					if op != "create" {
						initial := request(f.h, method, path, body, f.cookie, "field-authority-initial")
						expect(t, initial, 201)
						id := int64(data(t, initial)["id"].(float64))
						path = fmt.Sprintf("%s/%d", path, id)
						operation = fmt.Sprintf("customer_custom_field.%s:%d", op, id)
						body["expected_version"] = 1
						if op == "update" {
							method = "PATCH"
							body["label"] = "Changed"
						} else {
							method = "DELETE"
							body = map[string]any{"expected_version": 1}
						}
					}
					secret := ""
					if kind == "api_key" {
						secret = f.apiKey(t)
					}
					snapshot := func() string {
						var value string
						err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM customer_custom_fields x),(SELECT to_jsonb(x) FROM customer_field_catalogue x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_events x WHERE action LIKE 'customer_custom_field.%'),(SELECT jsonb_agg(to_jsonb(x) ORDER BY idempotency_key) FROM mutation_operations x WHERE scope LIKE 'admin:customer_custom_field.%'))::text`).Scan(&value)
						if err != nil {
							t.Fatal(err)
						}
						return value
					}
					before := snapshot()
					gate, err := f.db.Pool.Begin(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					defer gate.Rollback(context.Background())
					key := "field-authority-expiry"
					if wait == "receipt" {
						_, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("mutation:%d:admin:%s:%s", f.actor, operation, key))
					} else {
						_, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(741098543)")
						if err == nil {
							_, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION wait_field_authority_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'customer_custom_field.%' THEN PERFORM pg_advisory_xact_lock(741098543); END IF; RETURN NEW; END $$; CREATE TRIGGER wait_field_authority_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_field_authority_audit()`)
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					authority := customerAccountAuthorityFixture{adminBalanceFixture: f}
					authority.expireSoon(t, secret)
					headers := map[string]string{"Idempotency-Key": key}
					cookie := f.cookie
					if secret != "" {
						cookie = ""
						headers["Authorization"] = "Bearer " + secret
					}
					done := make(chan *httptest.ResponseRecorder, 1)
					go func() { done <- requestFrom(f.h, method, path, body, "192.0.2.89:5678", cookie, headers) }()
					waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
					waitBalanceCredentialExpiry(t, f, secret)
					if err = gate.Commit(t.Context()); err != nil {
						t.Fatal(err)
					}
					expect(t, awaitBalanceResponse(t, done), 401)
					if after := snapshot(); after != before {
						t.Fatal("expired field write changed definition/catalogue/audit/receipt")
					}
				})
			}
		}
	}
}
