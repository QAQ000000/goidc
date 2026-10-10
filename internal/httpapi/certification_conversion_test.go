package httpapi

import (
	"net/http"
	"testing"
)

func TestCertificationCompanyConversionIsTransactionalAndIdempotent(t *testing.T) {
	db, h := testServer(t)
	userID, cookie := user(t, db, h, "cert-conversion-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO certification_profiles(user_id,certification_type,status,provider_id) VALUES($1,'individual','approved','manual')`, userID); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"name": "李代表", "id_number": "110101199001010011", "company_name": "示例科技有限公司", "company_registration_number": "91310000MA1EXAMPLE"}
	body := map[string]any{"payload": payload, "expected_version": 1}
	path := "/api/v1/account/certification/convert-company"
	expect(t, request(h, http.MethodPost, path, body, "", ""), http.StatusUnauthorized)
	created := request(h, http.MethodPost, path, body, cookie, "cert-conversion-1")
	expect(t, created, http.StatusAccepted)
	result := data(t, created)
	if result["status"] != "pending" || result["version"] != float64(2) {
		t.Fatalf("unexpected conversion result: %v", result)
	}
	var kind, status string
	var version int64
	var submissions, audits int
	if err := db.Pool.QueryRow(t.Context(), "SELECT certification_type,status,version FROM certification_profiles WHERE user_id=$1", userID).Scan(&kind, &status, &version); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM certification_submissions WHERE user_id=$1", userID).Scan(&submissions); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='certification.company_conversion_requested'", userID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if kind != "company" || status != "pending" || version != 2 || submissions != 1 || audits != 1 {
		t.Fatalf("conversion transaction incomplete: kind=%s status=%s version=%d submissions=%d audits=%d", kind, status, version, submissions, audits)
	}
	replay := request(h, http.MethodPost, path, body, cookie, "cert-conversion-1")
	expect(t, replay, http.StatusOK)
	if data(t, replay)["replayed"] != true {
		t.Fatal("expected idempotent replay")
	}
	payload["company_name"] = "另一家公司"
	expect(t, request(h, http.MethodPost, path, map[string]any{"payload": payload, "expected_version": 1}, cookie, "cert-conversion-1"), http.StatusConflict)
	expect(t, request(h, http.MethodPost, path, body, cookie, "cert-conversion-2"), http.StatusConflict)
}
