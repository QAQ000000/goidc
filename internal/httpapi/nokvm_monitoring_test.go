package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNOKVMMonitoringHTTPPermissionsAndRedaction(t *testing.T) {
	f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "virtual_monitoring") {
			fmt.Fprintf(w, `{"code":0,"data":[{"CreatedAt":%q,"Cpu":"{\"value\":42}","Network":"{\"in\":4,\"out\":2}","Disk":"{\"vda\":[1,2]}"}]}`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
			return
		}
		if r.URL.Path == "/api/virtual/42" {
			fmt.Fprint(w, `{"code":0,"data":{"flow":{"code":0,"data":{"in":100,"out":50}},"flow_limit":0}}`)
			return
		}
		if strings.Contains(r.URL.Path, "run_status") {
			fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"monitor-vm","public_ip":[]}}`)
	}, true)
	f.process(t)
	path := fmt.Sprintf("/api/v1/services/%d/monitoring?period=1h", f.sid)
	expect(t, request(f.h, "GET", path, nil, f.other, ""), 404)
	readerID, reader := user(t, f.db, f.h, "monitor-reader-http", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	adminPath := strings.Replace(path, "/services/", "/admin/services/", 1)
	expect(t, request(f.h, "GET", adminPath, nil, reader, ""), 200)
	body := request(f.h, "GET", path, nil, f.customer, "")
	expect(t, body, 200)
	if body.Header().Get("Cache-Control") != "no-store" || strings.Contains(body.Body.String(), "fixture-private-token") || !strings.Contains(body.Body.String(), "42") {
		t.Fatal("monitoring response", body.Body.String())
	}
	for _, period := range []string{"bad", "8d", ""} {
		endpoint := path
		if period != "" {
			endpoint = fmt.Sprintf("/api/v1/services/%d/monitoring?period=%s", f.sid, period)
		}
		expect(t, request(f.h, "GET", endpoint, nil, f.customer, ""), map[bool]int{true: 200, false: 409}[period == ""])
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET status='suspended' WHERE id=$1", f.sid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", path, nil, f.customer, ""), 200)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET status='terminated' WHERE id=$1", f.sid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", path, nil, f.customer, ""), 409)
}

func TestNOKVMMonitoringRevalidatesAuthorizationAfterNetwork(t *testing.T) {
	for _, change := range []string{"logout", "disable", "revoke_role", "scope", "resource", "terminate"} {
		t.Run(change, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "monitoring") {
					close(entered)
					<-release
					fmt.Fprint(w, `{"code":0,"data":[]}`)
					return
				}
				if strings.Contains(r.URL.Path, "run_status") {
					fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
					return
				}
				fmt.Fprint(w, `{"code":0,"data":{"id":42}}`)
			}, true)
			f.process(t)
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			actor, cookie := f.uid, f.customer
			path := fmt.Sprintf("/api/v1/services/%d/monitoring", f.sid)
			if change == "revoke_role" || change == "scope" {
				actor, cookie = user(t, f.db, f.h, "monitor-revalidate-reader", "admin")
				path = fmt.Sprintf("/api/v1/admin/services/%d/monitoring", f.sid)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- request(f.h, "GET", path, nil, cookie, "") }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("monitor dispatch missing")
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			tx, err := f.db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, "SELECT id FROM services WHERE id=$1 FOR UPDATE", f.sid); err != nil {
				t.Fatal("network retained service locks", err)
			}
			tx.Rollback(ctx)
			var query string
			var param any = actor
			switch change {
			case "logout":
				query = "DELETE FROM sessions WHERE user_id=$1"
			case "disable":
				query = "UPDATE users SET status='disabled' WHERE id=$1"
			case "revoke_role":
				query = "UPDATE admin_roles SET permissions=ARRAY[]::text[] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)"
			case "scope":
				query = "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1"
			case "resource":
				query = "UPDATE services SET upstream_id='43' WHERE id=$1"
				param = f.sid
			case "terminate":
				query = "UPDATE services SET status='terminated' WHERE id=$1"
				param = f.sid
			}
			if _, err = f.db.Pool.Exec(t.Context(), query, param); err != nil {
				t.Fatal(err)
			}
			close(release)
			response := <-done
			expected := 409
			if change == "logout" {
				expected = 401
			}
			if change == "disable" || change == "revoke_role" {
				expected = 403
			}
			if change == "scope" {
				expected = 404
			}
			expect(t, response, expected)
			if strings.Contains(response.Body.String(), "samples") {
				t.Fatal("monitor data returned after authorization changed")
			}
		})
	}
}
