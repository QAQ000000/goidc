package httpapi

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestServiceActivityVerificationOutcomeAndUntrustedReferences(t *testing.T) {
	db, h := testServer(t)
	uid, owner := user(t, db, h, "activity-outcome-owner", "customer")
	pid := product(t, db, 4)
	sid := downloadService(t, db, h, uid, owner, pid, "activity-outcome-order")
	for _, resolution := range []string{"applied", "not_applied", "unknown"} {
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload)
VALUES($1,'service.nokvm.verified','service',$2,jsonb_build_object('resolution',$3::text,'job_id','999999999999999999999999999999','password','never-return-secret'))`, uid, fmt.Sprint(sid), resolution); err != nil {
			t.Fatal(err)
		}
	}
	path := fmt.Sprintf("/api/v1/services/%d/activity", sid)
	w := request(h, "GET", path, nil, owner, "")
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), "never-return-secret") || strings.Contains(w.Body.String(), "99999999") || data(t, w)["total"] != float64(3) {
		t.Fatal("untrusted reference or secret leaked")
	}
	seen := map[string]int{}
	for _, item := range data(t, w)["items"].([]any) {
		seen[item.(map[string]any)["status"].(string)]++
	}
	if seen["verified"] != 1 || seen["failed"] != 1 || seen["recorded"] != 1 {
		t.Fatal("verification incorrectly implied completion", seen)
	}
	for _, query := range []string{"action=", "action=create&action=sync", "category=billing&category=operation", "status=failed&status=verified", "offset=-1", "limit=101"} {
		expect(t, request(h, "GET", path+"?"+query, nil, owner, ""), 400)
	}
	w = request(h, "GET", path+"?offset=100", nil, owner, "")
	expect(t, w, 200)
	if data(t, w)["total"] != float64(3) || len(data(t, w)["items"].([]any)) != 0 {
		t.Fatal("empty page lost total")
	}
}

func TestServiceActivityWaitingRechecksCredentialsAndPermissions(t *testing.T) {
	for _, revocation := range []string{"customer_session", "customer_api_disabled", "admin_session", "admin_scope_session", "admin_key", "admin_permission", "admin_account"} {
		t.Run(revocation, func(t *testing.T) {
			db, h := testServer(t)
			uid, owner := user(t, db, h, "activity-wait-owner", "customer")
			adminID, admin := user(t, db, h, "activity-wait-admin", "admin")
			sid := downloadService(t, db, h, uid, owner, product(t, db, 4), "activity-wait-order")
			path := fmt.Sprintf("/api/v1/admin/services/%d/activity", sid)
			actor, cookie := adminID, admin
			if revocation == "customer_session" || revocation == "customer_api_disabled" {
				actor, cookie, path = uid, owner, fmt.Sprintf("/api/v1/services/%d/activity", sid)
			}
			secret := "zjmfgo_" + strings.Repeat("a", 64)
			if revocation == "admin_key" || revocation == "customer_api_disabled" {
				if _, err := db.Pool.Exec(t.Context(), `INSERT INTO api_keys(user_id,name,prefix,secret_hash) VALUES($1,'activity-test','zjmfgo_1234567890123456',$2)`, actor, tokenHash(secret)); err != nil {
					t.Fatal(err)
				}
			}
			block, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer block.Rollback(t.Context())
			waitQuery := "SELECT user_id FROM services WHERE%FOR SHARE"
			if revocation == "admin_scope_session" {
				_, err = block.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('customer-sales:'||$1::bigint::text,0))", uid)
				waitQuery = "SELECT pg_advisory_xact_lock_shared(hashtextextended('customer-sales:'%"
			} else {
				_, err = block.Exec(t.Context(), "SELECT id FROM services WHERE id=$1 FOR UPDATE", sid)
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan int, 1)
			go func() {
				if revocation == "admin_key" || revocation == "customer_api_disabled" {
					done <- requestBearer(h, "GET", path, nil, secret).Code
				} else {
					done <- request(h, "GET", path, nil, cookie, "").Code
				}
			}()
			deadline := time.Now().Add(4 * time.Second)
			for {
				var waiting bool
				if err = db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1)`, waitQuery).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("request never waited on service")
				}
				time.Sleep(10 * time.Millisecond)
			}
			want := 401
			switch revocation {
			case "customer_api_disabled":
				_, err = db.Pool.Exec(t.Context(), "UPDATE resource_api_settings SET enabled=false WHERE id=1")
				want = 403
			case "admin_key":
				_, err = db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=now() WHERE user_id=$1", actor)
			case "admin_permission":
				_, err = db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", actor)
				want = 403
			case "admin_account":
				_, err = db.Pool.Exec(t.Context(), "UPDATE administrators SET status='disabled' WHERE id=$1", actor)
			default:
				_, err = db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", actor)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = block.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case status := <-done:
				if status != want {
					t.Fatalf("revoked request returned %d want %d", status, want)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("request did not finish")
			}
		})
	}
}
