package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func pendingScopedCertification(t *testing.T) (*store.Store, http.Handler, int64, int64, int64, string, string) {
	t.Helper()
	db, h := testServer(t)
	_, root := user(t, db, h, "concurrent-cert-root", "super_admin")
	staffID, staff := user(t, db, h, "concurrent-cert-staff", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET sales_enabled=true,customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	uid, customer := user(t, db, h, "concurrent-cert-customer", "customer")
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/customers/%d/sales-owner", uid), map[string]any{"sales_owner_id": staffID, "expected_version": 1}, root, ""), 200)
	submit := request(h, "POST", "/api/v1/account/certification", map[string]any{"type": "individual", "payload": map[string]any{"name": "Alice Audit", "id_number": "CONCURRENT-CERT-ONLY"}}, customer, "")
	expect(t, submit, 202)
	return db, h, uid, staffID, int64(data(t, submit)["profile_id"].(float64)), root, staff
}

func certificationReviewRequest(h http.Handler, profile int64, cookie, key string) *httptest.ResponseRecorder {
	return request(h, "POST", fmt.Sprintf("/api/v1/admin/certifications/%d/review", profile), map[string]any{"decision": "approved", "expected_version": 1}, cookie, key)
}

func TestCertificationScopeAndCredentialRecheckedAfterProfileWait(t *testing.T) {
	for _, revoke := range []string{"sales_owner", "session"} {
		t.Run(revoke, func(t *testing.T) {
			db, h, uid, _, profile, root, staff := pendingScopedCertification(t)
			blocker, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err = blocker.Exec(t.Context(), "SELECT id FROM certification_profiles WHERE id=$1 FOR UPDATE", profile); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- certificationReviewRequest(h, profile, staff, "concurrent-cert-review") }()
			waitQueryLock(t, db, "SELECT user_id FROM certification_profiles WHERE id=$1 FOR UPDATE")
			want := 404
			if revoke == "sales_owner" {
				expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/customers/%d/sales-owner", uid), map[string]any{"sales_owner_id": nil, "expected_version": 2}, root, ""), 200)
			} else {
				want = 401
				if _, err = db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(staff))); err != nil {
					t.Fatal(err)
				}
			}
			if err = blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case out := <-done:
				expect(t, out, want)
			case <-time.After(7 * time.Second):
				t.Fatal("waiting review stalled")
			}
			var status string
			if err = db.Pool.QueryRow(t.Context(), "SELECT status FROM certification_profiles WHERE id=$1", profile).Scan(&status); err != nil || status != "pending" {
				t.Fatalf("status=%s err=%v", status, err)
			}
		})
	}
}

func TestCertificationReviewHoldsCustomerScopeUntilCommit(t *testing.T) {
	db, h, uid, staffID, profile, root, _ := pendingScopedCertification(t)
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err = db.LockCertificationReview(t.Context(), tx, staffID, profile); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- request(h, "PUT", fmt.Sprintf("/api/v1/admin/customers/%d/sales-owner", uid), map[string]any{"sales_owner_id": nil, "expected_version": 2}, root, "")
	}()
	waitQueryLock(t, db, "SELECT pg_advisory_xact_lock(hashtextextended('customer-sales:'||$1::bigint::text,0))")
	if err = db.ReviewCertificationTx(t.Context(), tx, staffID, profile, "approved", "", 1); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		expect(t, out, 200)
	case <-time.After(7 * time.Second):
		t.Fatal("sales assignment stalled")
	}
	// Internal store decisions also obey scope after the HTTP-independent review.
	if err = db.ReviewCertification(t.Context(), staffID, profile, "rejected", "", 0); err == nil {
		t.Fatal("out-of-scope internal review accepted")
	}
}

func TestCertificationReviewRechecksPermissionAfterRoleLockWait(t *testing.T) {
	db, h, _, staffID, profile, _, staff := pendingScopedCertification(t)
	blocker, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(70108190)"); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- certificationReviewRequest(h, profile, staff, "revoke-role-during-wait") }()
	waitQueryLock(t, db, "SELECT pg_advisory_xact_lock_shared(70108190)")
	if _, err = blocker.Exec(t.Context(), "UPDATE admin_roles SET permissions=array_remove(permissions,'certifications.manage') WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		expect(t, out, 403)
	case <-time.After(7 * time.Second):
		t.Fatal("role revocation stalled")
	}
	if err = db.ReviewCertification(t.Context(), staffID, profile, "approved", "", 1); err == nil {
		t.Fatal("store entry bypassed permission revocation")
	}
}

func TestCertificationDecisionNameAndProfileVersionRollbackTogether(t *testing.T) {
	db, _, uid, staffID, profile, _, _ := pendingScopedCertification(t)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE certification_settings SET auto_update_name=true"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err = db.ReviewCertificationTx(t.Context(), tx, staffID, profile, "approved", "", 1); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var name, status string
	var profiles, reviews int
	if err = db.Pool.QueryRow(t.Context(), `SELECT u.display_name,p.status,(SELECT count(*) FROM customer_profiles WHERE customer_id=u.id),(SELECT count(*) FROM certification_reviews WHERE profile_id=p.id) FROM users u JOIN certification_profiles p ON p.user_id=u.id WHERE u.id=$1`, uid).Scan(&name, &status, &profiles, &reviews); err != nil {
		t.Fatal(err)
	}
	if name != "concurrent-cert-customer" || status != "pending" || profiles != 0 || reviews != 0 {
		t.Fatalf("name=%s status=%s profiles=%d reviews=%d", name, status, profiles, reviews)
	}
}

func TestCertificationReplayRequiresCurrentScopeAndPermission(t *testing.T) {
	for _, revoke := range []string{"sales_owner", "permission", "disabled"} {
		t.Run(revoke, func(t *testing.T) {
			db, h, uid, staffID, profile, root, staff := pendingScopedCertification(t)
			expect(t, certificationReviewRequest(h, profile, staff, "current-scope-receipt"), 200)
			expect(t, certificationReviewRequest(h, profile, staff, "current-scope-receipt"), 200)
			switch revoke {
			case "sales_owner":
				expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/customers/%d/sales-owner", uid), map[string]any{"sales_owner_id": nil, "expected_version": 2}, root, ""), 200)
			case "permission":
				if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=array_remove(permissions,'certifications.manage') WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET status='disabled' WHERE id=$1", staffID); err != nil {
					t.Fatal(err)
				}
			}
			out := certificationReviewRequest(h, profile, staff, "current-scope-receipt")
			if out.Code == 200 {
				t.Fatal("revoked reviewer received a successful receipt")
			}
			var count int
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM certification_reviews WHERE profile_id=$1", profile).Scan(&count); err != nil || count != 1 {
				t.Fatalf("decisions=%d err=%v", count, err)
			}
		})
	}
}

func TestCertificationNameUpdatesProfileVersionAndPreservesFields(t *testing.T) {
	db, h, uid, _, profile, root, staff := pendingScopedCertification(t)
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO customer_profiles(customer_id,address,version) VALUES($1,'保留地址',7) ON CONFLICT(customer_id) DO UPDATE SET address='保留地址',version=7", uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE certification_settings SET auto_update_name=true"); err != nil {
		t.Fatal(err)
	}
	// Two identical requests must change the name and version only once.
	done := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { done <- certificationReviewRequest(h, profile, staff, "name-version-receipt") }()
	}
	for range 2 {
		select {
		case out := <-done:
			expect(t, out, 200)
		case <-time.After(7 * time.Second):
			t.Fatal("duplicate review stalled")
		}
	}
	var username, name, address string
	var version int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT u.username,u.display_name,p.address,p.version FROM users u JOIN customer_profiles p ON p.customer_id=u.id WHERE u.id=$1", uid).Scan(&username, &name, &address, &version); err != nil {
		t.Fatal(err)
	}
	if username != "concurrent-cert-customer" || name != "Alice Audit" || address != "保留地址" || version != 8 {
		t.Fatalf("username=%s name=%s address=%s version=%d", username, name, address, version)
	}
	expect(t, request(h, "POST", "/api/v1/auth/login", map[string]string{"identifier": "concurrent-cert-customer@example.test", "password": "integration-password"}, "", ""), 200)
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/customers/%d", uid), map[string]any{"expected_version": 7, "display_name": "旧页面姓名"}, root, "stale-name-profile"), 409)
}
