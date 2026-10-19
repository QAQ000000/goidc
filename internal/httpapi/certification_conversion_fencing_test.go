package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func testCertificationConversionReceiptWaitFence(t *testing.T, revokeSession bool) {
	t.Helper()
	for _, replay := range []bool{false, true} {
		name := "new_conversion"
		if replay {
			name = "existing_receipt"
		}
		t.Run(name, func(t *testing.T) {
			db, h := testServer(t)
			uid, customer := user(t, db, h, "cert-conversion-wait-customer", "customer")
			if _, err := db.Pool.Exec(t.Context(), `INSERT INTO certification_profiles(user_id,certification_type,status,provider_id) VALUES($1,'individual','approved','manual')`, uid); err != nil {
				t.Fatal(err)
			}
			payload := map[string]any{
				"name": "Conversion Fixture", "id_number": "CONVERSION-FIXTURE-ONLY",
				"company_name": "Conversion Fixture Company", "company_registration_number": "CONVERSION-COMPANY-FIXTURE",
			}
			body := map[string]any{"payload": payload, "expected_version": 1}
			key := "cert-conversion-wait-key"
			path := "/api/v1/account/certification/convert-company"
			wantKind, wantStatus, wantVersion, wantCount := "individual", "approved", 1, 0
			if replay {
				expect(t, request(h, http.MethodPost, path, body, customer, key), http.StatusAccepted)
				wantKind, wantStatus, wantVersion, wantCount = "company", "pending", 2, 1
			}
			raw, err := json.Marshal(struct {
				Payload         map[string]any
				ExpectedVersion int64
			}{payload, 1})
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(raw)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			blocker, err := db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if replayed, err := store.ReplayOperation(ctx, blocker, uid, "admin:certification.convert_company", key, hex.EncodeToString(hash[:]), nil); err != nil || replayed != replay {
				t.Fatalf("receipt lock setup: replayed=%v want=%v err=%v", replayed, replay, err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- request(h, http.MethodPost, path, body, customer, key) }()
			waitQueryLock(t, db, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))")
			wantHTTP, wantCode := http.StatusConflict, "certification_disabled"
			if revokeSession {
				wantHTTP, wantCode = http.StatusUnauthorized, "unauthorized"
				if _, err = db.Pool.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(customer))); err != nil {
					t.Fatal(err)
				}
			} else if _, err = db.Pool.Exec(ctx, "UPDATE certification_settings SET enabled=false WHERE id=1"); err != nil {
				t.Fatal(err)
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case response := <-done:
				expect(t, response, wantHTTP)
				if code := errorData(t, response)["code"]; code != wantCode {
					t.Fatalf("wrong conversion fence: code=%v want=%s", code, wantCode)
				}
			case <-ctx.Done():
				t.Fatal("conversion stalled after receipt wait was released")
			}
			var kind, status string
			var version, submissions, jobs, audits, receipts int
			if err = db.Pool.QueryRow(t.Context(), `SELECT certification_type,status,version,
 (SELECT count(*) FROM certification_submissions WHERE user_id=$1),
 (SELECT count(*) FROM certification_provider_jobs j JOIN certification_submissions s ON s.id=j.submission_id WHERE s.user_id=$1),
 (SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='certification.company_conversion_requested'),
 (SELECT count(*) FROM mutation_operations WHERE actor_id=$1 AND scope='admin:certification.convert_company')
 FROM certification_profiles WHERE user_id=$1`, uid).Scan(&kind, &status, &version, &submissions, &jobs, &audits, &receipts); err != nil {
				t.Fatal(err)
			}
			if kind != wantKind || status != wantStatus || version != wantVersion || submissions != wantCount || jobs != wantCount || audits != wantCount || receipts != wantCount {
				t.Fatalf("denied conversion changed state: kind=%s status=%s version=%d submissions=%d jobs=%d audits=%d receipts=%d", kind, status, version, submissions, jobs, audits, receipts)
			}
		})
	}
}

func TestCertificationConversionReceiptWaitRechecksSession(t *testing.T) {
	testCertificationConversionReceiptWaitFence(t, true)
}

func TestCertificationConversionReceiptWaitRechecksMasterSwitch(t *testing.T) {
	testCertificationConversionReceiptWaitFence(t, false)
}
