package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// setCertificationSettings flips switches directly so runtime behaviour can be
// tested in isolation from the settings write endpoint (covered by
// certification_settings_test.go).
func setCertificationSettings(t *testing.T, db *store.Store, sql string) {
	t.Helper()
	if _, err := db.Pool.Exec(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
}

func TestCertificationMasterSwitchGatesSubmissionAndConversion(t *testing.T) {
	db, h := testServer(t)
	submitterID, submitter := user(t, db, h, "cert-gate-submitter", "customer")
	converterID, converter := user(t, db, h, "cert-gate-converter", "customer")
	payload := map[string]any{"name": "关闭开关用户", "id_number": "110101199001011234"}
	submitPath := "/api/v1/account/certification"
	setCertificationSettings(t, db, "UPDATE certification_settings SET enabled=false WHERE id=1")

	denied := request(h, http.MethodPost, submitPath, map[string]any{"type": "individual", "payload": payload}, submitter, "")
	expect(t, denied, http.StatusConflict)
	if code := errorData(t, denied)["code"]; code != "certification_disabled" {
		t.Fatalf("unexpected disabled submit error %v", code)
	}
	if message := errorData(t, denied)["message"]; message != "实名认证已关闭" {
		t.Fatalf("unexpected disabled submit message %v", message)
	}
	// A rejected submit must not create a profile.
	var profiles int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM certification_profiles WHERE user_id=$1", submitterID).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if profiles != 0 {
		t.Fatalf("disabled submission created %d profiles", profiles)
	}

	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO certification_profiles(user_id,certification_type,status,provider_id) VALUES($1,'individual','approved','manual')`, converterID); err != nil {
		t.Fatal(err)
	}
	convertPath := "/api/v1/account/certification/convert-company"
	convertBody := map[string]any{"payload": map[string]any{"name": "关闭开关企业", "id_number": "110101199001011234", "company_name": "关闭示例科技有限公司", "company_registration_number": "91310000MACLOSED01"}, "expected_version": 1}
	denied = request(h, http.MethodPost, convertPath, convertBody, converter, "cert-gate-conversion-1")
	expect(t, denied, http.StatusConflict)
	if code := errorData(t, denied)["code"]; code != "certification_disabled" {
		t.Fatalf("unexpected disabled conversion error %v", code)
	}
	var kind, status string
	if err := db.Pool.QueryRow(t.Context(), "SELECT certification_type,status FROM certification_profiles WHERE user_id=$1", converterID).Scan(&kind, &status); err != nil {
		t.Fatal(err)
	}
	if kind != "individual" || status != "approved" {
		t.Fatalf("disabled conversion mutated the profile: kind=%s status=%s", kind, status)
	}

	// Re-enabling restores both paths without a regression to 202.
	setCertificationSettings(t, db, "UPDATE certification_settings SET enabled=true WHERE id=1")
	accepted := request(h, http.MethodPost, submitPath, map[string]any{"type": "individual", "payload": payload}, submitter, "")
	expect(t, accepted, http.StatusAccepted)
	converted := request(h, http.MethodPost, convertPath, convertBody, converter, "cert-gate-conversion-2")
	expect(t, converted, http.StatusAccepted)
	if data(t, converted)["status"] != "pending" {
		t.Fatalf("unexpected conversion payload %v", converted.Body.String())
	}
}

func TestCertificationAccountViewExposesEnabledSwitch(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "cert-enabled-customer", "customer")
	path := "/api/v1/account/certification"
	view := request(h, http.MethodGet, path, nil, customer, "")
	expect(t, view, http.StatusOK)
	if data(t, view)["enabled"] != true {
		t.Fatalf("expected enabled=true by default: %v", view.Body.String())
	}
	setCertificationSettings(t, db, "UPDATE certification_settings SET enabled=false WHERE id=1")
	view = request(h, http.MethodGet, path, nil, customer, "")
	expect(t, view, http.StatusOK)
	if data(t, view)["enabled"] != false {
		t.Fatalf("expected enabled=false after switch off: %v", view.Body.String())
	}
	setCertificationSettings(t, db, "DELETE FROM certification_settings WHERE id=1")
	view = request(h, http.MethodGet, path, nil, customer, "")
	expect(t, view, http.StatusOK)
	if data(t, view)["enabled"] != false {
		t.Fatalf("missing settings row must read as disabled: %v", view.Body.String())
	}
}

func TestCertificationAutoUpdateNameOnApproval(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "cert-autoname-root", "super_admin")
	userID, customer := user(t, db, h, "cert-autoname-customer", "customer")
	reviewPath := func(profileID, expected int64, key string, decision string, note string) *httptest.ResponseRecorder {
		t.Helper()
		return request(h, http.MethodPost, fmt.Sprintf("/api/v1/admin/certifications/%d/review", profileID), map[string]any{"decision": decision, "note": note, "expected_version": expected}, root, key)
	}
	currentVersion := func(profileID int64) int64 {
		t.Helper()
		var version int64
		if err := db.Pool.QueryRow(t.Context(), "SELECT version FROM certification_profiles WHERE id=$1", profileID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		return version
	}
	usernameOf := func() string {
		t.Helper()
		var username string
		if err := db.Pool.QueryRow(t.Context(), "SELECT username FROM users WHERE id=$1", userID).Scan(&username); err != nil {
			t.Fatal(err)
		}
		return username
	}

	submit := request(h, http.MethodPost, "/api/v1/account/certification", map[string]any{"type": "individual", "payload": map[string]any{"name": "认证姓名乙", "id_number": "110101199001012222"}}, customer, "")
	expect(t, submit, http.StatusAccepted)
	profileID := int64(data(t, submit)["profile_id"].(float64))

	// Switch off: approving must not touch the account name.
	expect(t, reviewPath(profileID, currentVersion(profileID), "cert-autoname-review-off", "approved", ""), http.StatusOK)
	if username := usernameOf(); username != "cert-autoname-customer" {
		t.Fatalf("auto_update_name off still renamed the customer: %s", username)
	}

	// Switch on: the next approved review writes the certified name.
	setCertificationSettings(t, db, "UPDATE certification_settings SET auto_update_name=true WHERE id=1")
	resubmit := request(h, http.MethodPost, "/api/v1/account/certification", map[string]any{"type": "individual", "payload": map[string]any{"name": "认证姓名甲", "id_number": "110101199001012222"}}, customer, "")
	expect(t, resubmit, http.StatusAccepted)
	expect(t, reviewPath(profileID, currentVersion(profileID), "cert-autoname-review-on", "approved", ""), http.StatusOK)
	if username := usernameOf(); username != "认证姓名甲" {
		t.Fatalf("approved auto_update_name did not write the certified name: %s", username)
	}
	var events int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='certification.name_updated' AND entity_id=$1", fmt.Sprint(profileID)).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("expected one name_updated audit event, got %d", events)
	}
	// Re-approving an identical name (via a new submission) skips the write.
	resubmit = request(h, http.MethodPost, "/api/v1/account/certification", map[string]any{"type": "individual", "payload": map[string]any{"name": "认证姓名甲", "id_number": "110101199001012222"}}, customer, "")
	expect(t, resubmit, http.StatusAccepted)
	expect(t, reviewPath(profileID, currentVersion(profileID), "cert-autoname-review-same", "approved", ""), http.StatusOK)
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='certification.name_updated' AND entity_id=$1", fmt.Sprint(profileID)).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("identical name rewrite was not skipped: %d events", events)
	}

	// A rejection never renames.
	resubmit = request(h, http.MethodPost, "/api/v1/account/certification", map[string]any{"type": "individual", "payload": map[string]any{"name": "认证姓名丙", "id_number": "110101199001012222"}}, customer, "")
	expect(t, resubmit, http.StatusAccepted)
	expect(t, reviewPath(profileID, currentVersion(profileID), "cert-autoname-review-reject", "rejected", "资料不清晰"), http.StatusOK)
	if username := usernameOf(); username != "认证姓名甲" {
		t.Fatalf("rejection renamed the customer: %s", username)
	}
}

// TestCertificationAutoUpdateNameSkipsTakenUsername pins the deliberate
// deviation from legacy: clients.username had no unique constraint while Go
// usernames double as login identifiers, so a duplicate keeps the approval and
// records certification.name_update_skipped instead of failing the review.
func TestCertificationAutoUpdateNameSkipsTakenUsername(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "cert-autoname-conflict-root", "super_admin")
	customerID, customer := user(t, db, h, "cert-autoname-conflict-customer", "customer")
	// Occupy the certified name so the writeback hits the unique index.
	user(t, db, h, "cert-autoname-name-owner", "customer")
	setCertificationSettings(t, db, "UPDATE certification_settings SET auto_update_name=true WHERE id=1")
	submit := request(h, http.MethodPost, "/api/v1/account/certification", map[string]any{"type": "individual", "payload": map[string]any{"name": "cert-autoname-name-owner", "id_number": "110101199001014444"}}, customer, "")
	expect(t, submit, http.StatusAccepted)
	profileID := int64(data(t, submit)["profile_id"].(float64))
	expect(t, request(h, http.MethodPost, fmt.Sprintf("/api/v1/admin/certifications/%d/review", profileID), map[string]any{"decision": "approved", "expected_version": 1}, root, "cert-autoname-conflict-review"), http.StatusOK)
	var username, status string
	if err := db.Pool.QueryRow(t.Context(), "SELECT u.username,p.status FROM users u JOIN certification_profiles p ON p.user_id=u.id WHERE u.id=$1", customerID).Scan(&username, &status); err != nil {
		t.Fatal(err)
	}
	if username != "cert-autoname-conflict-customer" || status != "approved" {
		t.Fatalf("taken username must skip the writeback, not fail the review: username=%s status=%s", username, status)
	}
	var skipped int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='certification.name_update_skipped' AND entity_id=$1 AND payload->>'reason'='username_taken'", fmt.Sprint(profileID)).Scan(&skipped); err != nil {
		t.Fatal(err)
	}
	if skipped != 1 {
		t.Fatalf("expected one username_taken skip audit, got %d", skipped)
	}
}

func TestCertificationPhoneMatchFollowsLegacyLenientSemantics(t *testing.T) {
	db, h := testServer(t)
	userID, customer := user(t, db, h, "cert-phone-customer", "customer")
	setCertificationSettings(t, db, "UPDATE certification_settings SET require_phone_match=true WHERE id=1")
	path := "/api/v1/account/certification"

	// (c) account without a bound phone always passes, even on a mismatch.
	expect(t, request(h, http.MethodPost, path, map[string]any{"type": "individual", "payload": map[string]any{"name": "未绑定手机", "id_number": "110101199001013333", "phone": "+8613900000000"}}, customer, ""), http.StatusAccepted)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone='+8613800000001' WHERE id=$1", userID); err != nil {
		t.Fatal(err)
	}
	// (a) payload without a phone is never checked.
	expect(t, request(h, http.MethodPost, path, map[string]any{"type": "individual", "payload": map[string]any{"name": "不带手机", "id_number": "110101199001013333"}}, customer, ""), http.StatusAccepted)
	// (b) matching phone passes.
	expect(t, request(h, http.MethodPost, path, map[string]any{"type": "individual", "payload": map[string]any{"name": "手机一致", "id_number": "110101199001013333", "phone": "+8613800000001"}}, customer, ""), http.StatusAccepted)
	// A mismatch is rejected with the legacy message.
	mismatch := request(h, http.MethodPost, path, map[string]any{"type": "individual", "payload": map[string]any{"name": "手机不一致", "id_number": "110101199001013333", "phone": "+8613800000002"}}, customer, "")
	expect(t, mismatch, http.StatusConflict)
	if code := errorData(t, mismatch)["code"]; code != "certification_phone_mismatch" {
		t.Fatalf("unexpected phone mismatch error %v", code)
	}
	if message := errorData(t, mismatch)["message"]; message != "手机号与绑定手机不一致" {
		t.Fatalf("unexpected phone mismatch message %v", message)
	}
	// Switch off: the same mismatch is accepted again.
	setCertificationSettings(t, db, "UPDATE certification_settings SET require_phone_match=false WHERE id=1")
	expect(t, request(h, http.MethodPost, path, map[string]any{"type": "individual", "payload": map[string]any{"name": "开关关闭", "id_number": "110101199001013333", "phone": "+8613800000002"}}, customer, ""), http.StatusAccepted)
}
