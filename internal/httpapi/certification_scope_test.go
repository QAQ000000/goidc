package httpapi

import (
	"fmt"
	"net/http"
	"testing"
)

// Out-of-scope administrators must not see or decide certifications belonging to
// customers outside their sales scope. The detail and history endpoints already
// enforced this; the list and review endpoints did not.
func TestCertificationReviewHonorsCustomerScope(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "cert-scope-root", "super_admin")
	staffID, staff := user(t, db, h, "cert-scope-staff", "admin")
	// Scope 'owned' means the administrator only handles customers assigned to
	// them, so an unassigned applicant is out of scope.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET sales_enabled=true,customer_scope='owned' WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}

	victimID, victim := user(t, db, h, "cert-scope-victim", "customer")
	expect(t, request(h, http.MethodPost, "/api/v1/account/certification", map[string]any{
		"type":    "individual",
		"payload": map[string]any{"name": "范围外申请人", "id_number": "110101199001011234"},
	}, victim, "cert-scope-submit"), http.StatusAccepted)

	var profileID int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM certification_profiles WHERE user_id=$1", victimID).Scan(&profileID); err != nil {
		t.Fatal(err)
	}
	// Guard the fixture itself: this administrator really is out of scope.
	var allowed bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT admin_customer_allowed($1,$2)", staffID, victimID).Scan(&allowed); err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("fixture invalid: administrator is unexpectedly in scope")
	}

	// The list must not disclose the out-of-scope record.
	list := request(h, http.MethodGet, "/api/v1/admin/certifications", nil, staff, "")
	expect(t, list, http.StatusOK)
	for _, row := range listData(t, list) {
		if int64(row["id"].(float64)) == profileID {
			t.Fatalf("list disclosed out-of-scope certification %d", profileID)
		}
	}

	// Detail and history already refused it; keep them pinned.
	detailPath := fmt.Sprintf("/api/v1/admin/certifications/%d", profileID)
	expect(t, request(h, http.MethodGet, detailPath, nil, staff, ""), http.StatusNotFound)
	expect(t, request(h, http.MethodGet, detailPath+"/history", nil, staff, ""), http.StatusOK)

	// The review must not change an out-of-scope decision, and must not leak the
	// existence of the record through a distinguishable error.
	reviewPath := detailPath + "/review"
	review := request(h, http.MethodPost, reviewPath, map[string]any{
		"decision": "rejected", "note": "out-of-scope review", "expected_version": 1,
	}, staff, "cert-scope-review")
	if review.Code == http.StatusOK {
		t.Fatalf("out-of-scope review was accepted")
	}
	expect(t, review, http.StatusNotFound)

	var status string
	var version int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT status,version FROM certification_profiles WHERE id=$1", profileID).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || version != 1 {
		t.Fatalf("out-of-scope review mutated the profile: status=%s version=%d", status, version)
	}

	// Scope follows ownership: once the applicant belongs to the reviewer, the
	// same endpoint shows the record and the review succeeds.
	expect(t, request(h, http.MethodPut, fmt.Sprintf("/api/v1/admin/customers/%d/sales-owner", victimID), map[string]any{"sales_owner_id": staffID, "expected_version": 1}, root, ""), http.StatusOK)

	assignedList := request(h, http.MethodGet, "/api/v1/admin/certifications", nil, staff, "")
	expect(t, assignedList, http.StatusOK)
	found := false
	for _, row := range listData(t, assignedList) {
		if int64(row["id"].(float64)) == profileID {
			found = true
		}
	}
	if !found {
		t.Fatalf("in-scope administrator cannot see an applicant they own")
	}
	expect(t, request(h, http.MethodGet, detailPath, nil, staff, ""), http.StatusOK)

	approved := request(h, http.MethodPost, reviewPath, map[string]any{
		"decision": "approved", "note": "in-scope review", "expected_version": 1,
	}, staff, "cert-scope-review-in-scope")
	expect(t, approved, http.StatusOK)
	if err := db.Pool.QueryRow(t.Context(), "SELECT status FROM certification_profiles WHERE id=$1", profileID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "approved" {
		t.Fatalf("in-scope review did not apply: status=%s", status)
	}
}
