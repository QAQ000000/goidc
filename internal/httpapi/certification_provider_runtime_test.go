package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/certification"
	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/store"
)

var httpCertificationProviderOnce sync.Once

type httpCertificationProvider struct{}

func (httpCertificationProvider) Submit(_ context.Context, in certification.Submission) (certification.Result, error) {
	if in.RequestID == "" || in.Payload["name"] != "Certified Fixture" {
		return certification.Result{}, errors.New("invalid fixture request")
	}
	return certification.Result{Status: "approved", ProviderSubject: "fixture-approval", Message: "private provider message"}, nil
}

func (httpCertificationProvider) Query(context.Context, certification.Query) (certification.Result, error) {
	return certification.Result{}, errors.New("unexpected query")
}

func registerHTTPCertificationProvider(t *testing.T) {
	t.Helper()
	httpCertificationProviderOnce.Do(func() {
		err := certification.RegisterFactory(channel.Descriptor{
			ID: "http_cert_fixture", Name: "HTTP certification fixture", Category: channel.Certification,
			Capabilities: []string{"submit", "query", "query_by_request_id"},
			Fields:       []channel.Field{{Key: "tenant", Label: "Tenant", Type: channel.Text, Required: true}, {Key: "api_key", Label: "API key", Type: channel.Text, Secret: true, Required: true}},
		}, func(cfg module.Config) (certification.Provider, error) {
			if cfg.Values["tenant"] != "fixture" || cfg.Secrets["api_key"] != "fixture-private-key" {
				return nil, errors.New("invalid fixture configuration")
			}
			return httpCertificationProvider{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func certificationSubmitBody() map[string]any {
	return map[string]any{"type": "individual", "payload": map[string]any{"name": "Certified Fixture", "id_number": "FIXTURE-IDENTITY-ONLY"}}
}

func TestCertificationSubmissionReceiptConcurrencyAndRollback(t *testing.T) {
	db, h := testServer(t)
	uid, customer := user(t, db, h, "cert-receipt-customer", "customer")
	path := "/api/v1/account/certification"
	body := certificationSubmitBody()
	done := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { done <- request(h, http.MethodPost, path, body, customer, "cert-submit-same-key") }()
	}
	ids := map[float64]bool{}
	for range 2 {
		select {
		case out := <-done:
			if out.Code != http.StatusAccepted && out.Code != http.StatusOK {
				t.Fatalf("unexpected submission response: %d %s", out.Code, out.Body.String())
			}
			ids[data(t, out)["id"].(float64)] = true
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent submission stalled")
		}
	}
	var submissions, jobs, audits, receipts int
	err := db.Pool.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM certification_submissions WHERE user_id=$1),
 (SELECT count(*) FROM certification_provider_jobs j JOIN certification_submissions s ON s.id=j.submission_id WHERE s.user_id=$1),
 (SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='certification.submitted'),
 (SELECT count(*) FROM mutation_operations WHERE actor_id=$1 AND scope='certification:submit')`, uid).Scan(&submissions, &jobs, &audits, &receipts)
	if err != nil || len(ids) != 1 || submissions != 1 || jobs != 1 || audits != 1 || receipts != 1 {
		t.Fatalf("submission atomicity: ids=%v submissions=%d jobs=%d audits=%d receipts=%d err=%v", ids, submissions, jobs, audits, receipts, err)
	}
	changed := certificationSubmitBody()
	changed["payload"].(map[string]any)["name"] = "Changed Fixture"
	expect(t, request(h, http.MethodPost, path, changed, customer, "cert-submit-same-key"), http.StatusConflict)
	expect(t, request(h, http.MethodPost, path, body, customer, "short"), http.StatusBadRequest)
	// Older clients may omit the new optional key.
	expect(t, request(h, http.MethodPost, path, body, customer, ""), http.StatusAccepted)
	_, second := user(t, db, h, "cert-receipt-second", "customer")
	if _, err = db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_cert_submit_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='certification.submitted' THEN RAISE EXCEPTION 'fixture audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_cert_submit_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_cert_submit_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, http.MethodPost, path, body, second, "cert-submit-rollback"), http.StatusInternalServerError)
	var left int
	if err = db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM mutation_operations WHERE idempotency_key='cert-submit-rollback'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("failed submission retained receipt: %d %v", left, err)
	}
	if _, err = db.Pool.Exec(t.Context(), `DROP TRIGGER reject_cert_submit_audit ON audit_events; DROP FUNCTION reject_cert_submit_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, http.MethodPost, path, body, second, "cert-submit-rollback"), http.StatusAccepted)
}

func TestCertificationSubmissionReplayRechecksCredentialAfterWait(t *testing.T) {
	db, h := testServer(t)
	uid, customer := user(t, db, h, "cert-receipt-revoke", "customer")
	body := certificationSubmitBody()
	key := "cert-submit-revoked-key"
	path := "/api/v1/account/certification"
	expect(t, request(h, http.MethodPost, path, body, customer, key), http.StatusAccepted)
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	hash := settingMutationHash(struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}{"individual", body["payload"].(map[string]any)})
	if _, err = store.ReplayOperation(t.Context(), tx, uid, "certification:submit", key, hash, nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request(h, http.MethodPost, path, body, customer, key) }()
	waitQueryLock(t, db, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))")
	if _, err = db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(strings.TrimPrefix(customer, "zjmfgo_session="))); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		expect(t, out, http.StatusUnauthorized)
	case <-time.After(10 * time.Second):
		t.Fatal("revoked replay stalled")
	}
}

func TestCertificationProviderApprovalEnablesOrderAndAPIKey(t *testing.T) {
	registerHTTPCertificationProvider(t)
	db, h := testServer(t)
	_, root := user(t, db, h, "cert-provider-root", "super_admin")
	uid, customer := user(t, db, h, "cert-provider-customer", "customer")
	instance := request(h, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{
		"id": "cert-http-fixture", "category": "certification", "implementation_id": "http_cert_fixture", "display_name": "HTTP fixture",
		"enabled": true, "config": map[string]any{"tenant": "fixture"}, "secure_config": map[string]string{"api_key": "fixture-private-key"},
	}, root, "")
	expect(t, instance, http.StatusCreated)
	if _, err := db.Pool.Exec(t.Context(), `UPDATE certification_settings SET default_provider_id='http_cert_fixture',default_instance_id='cert-http-fixture',require_for_purchase=true,auto_update_name=true; UPDATE resource_api_settings SET require_certification=true,require_verified_phone=false`); err != nil {
		t.Fatal(err)
	}
	pid := product(t, db, 10)
	version, item := add(t, h, customer, pid, 1)
	checkout := store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}
	expect(t, request(h, http.MethodPost, "/api/v1/orders", checkout, customer, "cert-provider-order-before"), http.StatusConflict)
	expect(t, request(h, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "before certification"}, customer, ""), http.StatusConflict)
	created := request(h, http.MethodPost, "/api/v1/account/certification", certificationSubmitBody(), customer, "cert-provider-submit")
	expect(t, created, http.StatusAccepted)
	processed, err := db.ProcessCertificationJob(t.Context())
	if err != nil || !processed {
		t.Fatalf("provider processing: processed=%v err=%v", processed, err)
	}
	view := request(h, http.MethodGet, "/api/v1/account/certification", nil, customer, "")
	expect(t, view, http.StatusOK)
	if data(t, view)["status"] != "approved" || data(t, view)["processing_state"] != "finished" {
		t.Fatalf("provider approval not applied: %s", view.Body.String())
	}
	for _, secret := range []string{"FIXTURE-IDENTITY-ONLY", "private provider message", "fixture-private-key", "fixture-approval"} {
		if strings.Contains(view.Body.String(), secret) {
			t.Fatalf("public certification DTO leaked private provider material")
		}
	}
	expect(t, request(h, http.MethodPost, "/api/v1/orders", checkout, customer, "cert-provider-order-after"), http.StatusCreated)
	expect(t, request(h, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "after certification"}, customer, ""), http.StatusCreated)
	var name string
	if err = db.Pool.QueryRow(t.Context(), "SELECT display_name FROM users WHERE id=$1", uid).Scan(&name); err != nil || name != "Certified Fixture" {
		t.Fatalf("provider approval name update: name=%s err=%v", name, err)
	}
	submissionID := int64(data(t, created)["id"].(float64))
	if _, err = db.Pool.Exec(t.Context(), `UPDATE certification_submissions SET payload_encrypted=decode('01','hex') WHERE id=$1`, submissionID); err == nil {
		t.Fatal("application identity data was mutable")
	}
	// Simulate corrupted storage after first proving ordinary writes cannot
	// alter an application. The independent schema belongs to this test only.
	if _, err = db.Pool.Exec(t.Context(), `ALTER TABLE certification_submissions DISABLE TRIGGER certification_submission_binding_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), `UPDATE certification_submissions SET payload_encrypted=decode('01','hex') WHERE id=$1`, submissionID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), `ALTER TABLE certification_submissions ENABLE TRIGGER certification_submission_binding_immutable`); err != nil {
		t.Fatal(err)
	}
	denied := request(h, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "damaged identity data"}, customer, "")
	expect(t, denied, http.StatusConflict)
	if !strings.Contains(denied.Body.String(), "api_certification_required") {
		t.Fatal("invalid encrypted identity data did not preserve eligibility gate")
	}
}
