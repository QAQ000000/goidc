package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/provision"
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
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", readerID); err != nil {
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
	for _, change := range []string{"logout", "logout_admin", "revoke_customer_key", "revoke_admin_key", "disable", "revoke_role", "scope", "resource", "terminate"} {
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
			if change == "logout_admin" || change == "revoke_admin_key" || change == "revoke_role" || change == "scope" {
				actor, cookie = user(t, f.db, f.h, "monitor-revalidate-reader", "admin")
				path = fmt.Sprintf("/api/v1/admin/services/%d/monitoring", f.sid)
			}
			secret := ""
			if change == "revoke_customer_key" || change == "revoke_admin_key" {
				key := request(f.h, "POST", "/api/v1/api-keys", map[string]string{"name": "monitor-revalidation"}, cookie, "")
				expect(t, key, 201)
				secret = data(t, key)["secret"].(string)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				if secret != "" {
					done <- requestBearer(f.h, "GET", path, nil, secret)
					return
				}
				done <- request(f.h, "GET", path, nil, cookie, "")
			}()
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
			case "logout", "logout_admin":
				query = "DELETE FROM sessions WHERE user_id=$1"
			case "revoke_customer_key", "revoke_admin_key":
				query = "UPDATE api_keys SET revoked_at=now() WHERE secret_hash=$1"
				param = tokenHash(secret)
			case "disable":
				query = "UPDATE users SET status='disabled' WHERE id=$1"
			case "revoke_role":
				query = "UPDATE admin_roles SET permissions=ARRAY[]::text[] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)"
			case "scope":
				query = "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1"
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
			if change == "logout" || change == "logout_admin" || change == "revoke_customer_key" || change == "revoke_admin_key" {
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

func TestNOKVMResourceReadsRevalidatePortalLogoutAfterNetwork(t *testing.T) {
	for _, kind := range []string{"storage/snapshot", "cdrom"} {
		for _, portal := range []string{"customer", "admin"} {
			t.Run(kind+"/"+portal, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/snapshot/42" || r.URL.Path == "/api/cd_rom/" {
						close(entered)
						<-release
						fmt.Fprint(w, `{"code":0,"data":[{"id":10,"name":"private-remote-data","created_at":"2026-10-06T04:00:00Z"}]}`)
						return
					}
					if r.URL.Path == "/api/virtual/42" {
						fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"resource-vm","cd_roms_id":0}}`)
						return
					}
					if strings.Contains(r.URL.Path, "run_status") {
						fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
						return
					}
					fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"resource-vm"}}`)
				}, true, func(d *provision.Delivery) { d.Resources.Snapshots = 3 })
				t.Cleanup(func() {
					select {
					case <-release:
					default:
						close(release)
					}
				})
				f.process(t)
				cookie, dual, prefix := f.customer, f.root+"; "+f.customer, ""
				if portal == "admin" {
					cookie, prefix = f.root, "/admin"
				}
				path := fmt.Sprintf("/api/v1%s/services/%d/%s", prefix, f.sid, kind)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- portalRequest(f.h, "GET", path, nil, dual, "") }()
				select {
				case <-entered:
				case response := <-done:
					t.Fatalf("resource read returned before its remote listing: status %d; %s", response.Code, response.Body.String())
				case <-time.After(5 * time.Second):
					t.Fatal("resource read did not reach the remote server")
				}
				if _, err := f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(cookie))); err != nil {
					t.Fatal(err)
				}
				close(release)
				select {
				case response := <-done:
					expect(t, response, 401)
					if strings.Contains(response.Body.String(), "private-remote-data") {
						t.Fatal("resource data escaped after selected portal logout")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("resource read did not finish after releasing the remote server")
				}
			})
		}
	}
}
