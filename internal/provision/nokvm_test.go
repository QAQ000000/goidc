package provision

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureResources() Resources {
	return Resources{AreaID: 2, Core: 4, CPUMode: "host", Memory: 4096, Disk: 80, NetOut: 100, NetIn: 200, Snapshots: 3, Backups: 2, TemplateID: 7, IPNum: 1, FlowLimit: 500, NATACL: 10, NATWeb: 5, NodeID: 9, Login: "root"}
}

func TestNOKVMProtocolLifecycle(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		ts, rnd := q.Get("time"), q.Get("random")
		// The random hexadecimal string can precede the decimal timestamp.
		first, second := ts, rnd
		if rnd < ts {
			first, second = rnd, ts
		}
		sum := md5.Sum([]byte(first + second + "zz-supplier-token"))
		if len(rnd) != 6 || len(ts) != 10 || len(q) != 3 || q.Get("signature") != strings.ToUpper(hex.EncodeToString(sum[:])) {
			t.Errorf("wire signature mismatch")
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method == "POST" {
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Error("form content type")
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			want := url.Values{"users_id": {"3"}, "username": {"zjmfgo-user-3"}, "areas_id": {"2"}, "core": {"4"}, "cpu_mode": {"host"}, "memory": {"4096"}, "data_disk_size": {"80"}, "net_out": {"100"}, "net_in": {"200"}, "snapshoot": {"3"}, "backups": {"2"}, "templates_id": {"7"}, "sys_pwd": {"fixture-secret-password"}, "vnc_pwd": {"fixture-vnc-password"}, "expire_time": {"2999-01-01 00:00:00"}, "ip_num": {"1"}, "flow_limit": {"500"}, "nat_acl_limit": {"10"}, "nat_web_limit": {"5"}, "nodes_id": {"9"}}
			if r.PostForm.Encode() != want.Encode() {
				t.Errorf("create form %v", r.PostForm)
			}
			fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"vm-42","public_ip":[{"id":8,"ip":"192.0.2.8"}],"sys_pwd":"do-not-return"}}`)
		} else if strings.Contains(r.URL.Path, "run_status") {
			fmt.Fprint(w, `{"code":0,"data":{"code":13}}`)
		} else if r.URL.Path == "/api/virtual/42" && r.Method == "GET" {
			fmt.Fprint(w, `{"code":0,"data":{"name":"vm-42","ip_list":[{"ip":"2001:db8::8"}],"sys_pwd":"do-not-return"}}`)
		} else {
			fmt.Fprint(w, `{"code":0,"message":"secret remote message"}`)
		}
	}))
	defer srv.Close()
	adapter, err := New(Profile{ID: "fixture", Name: "测试", URL: srv.URL, Token: "zz-supplier-token"})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Action: "create", UserID: 3, ServiceID: 5, Resources: fixtureResources(), Credentials: Credentials{Password: "fixture-secret-password", VNCPassword: "fixture-vnc-password"}}
	result, err := adapter.Execute(context.Background(), request)
	if err != nil || result.UpstreamID != "42" || len(result.IPs) != 1 {
		t.Fatal(result, err)
	}
	request.UpstreamID = result.UpstreamID
	for _, action := range []string{"suspend", "resume", "sync", "terminate"} {
		request.Action = action
		result, err = adapter.Execute(context.Background(), request)
		if err != nil {
			t.Fatal(action, err)
		}
		if action == "sync" && (result.Power != "suspended" || len(result.IPs) != 1) {
			t.Fatal(result)
		}
	}
	want := "POST /api/virtual|GET /api/virtual_pause/42|GET /api/virtual_restore_pause/42|GET /api/virtual/42|GET /api/virtual_run_status/42|DELETE /api/virtual/42"
	if strings.Join(calls, "|") != want {
		t.Fatal(calls)
	}
	if strings.Contains(fmt.Sprint(adapter.Summary()), "supplier-token") || strings.Contains(fmt.Sprint(adapter.Summary()), srv.URL) {
		t.Fatal("profile summary leaked secret")
	}
}

func TestNOKVMUnknownOutcomesAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		unknown    bool
	}{
		{"missing_code", `{"message":"token-secret"}`, 200, true},
		{"null_data", `{"code":0,"data":null}`, 200, true},
		{"invalid_id", `{"code":0,"data":{"id":"../42"}}`, 200, true},
		{"rejected", `{"code":8,"message":"token-secret"}`, 200, false},
		{"http_failure", `{"code":0,"data":{"id":42}}`, 502, true},
		{"oversized", strings.Repeat("x", (1<<20)+1), 200, true},
		{"invalid_json", `{"code":0,"data":{"id":42}} trailing token-secret`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			n, err := New(Profile{ID: "fixture", Name: "fixture", URL: srv.URL, Token: "token-secret"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = n.Execute(t.Context(), Request{Action: "create", UserID: 1, ServiceID: 1, Resources: fixtureResources(), Credentials: Credentials{Password: "long-fixture-password", VNCPassword: "long-vnc-password"}})
			var failure *Error
			if !errors.As(err, &failure) || failure.Unknown != tc.unknown || strings.Contains(err.Error(), "token-secret") || strings.Contains(err.Error(), srv.URL) {
				t.Fatal(err)
			}
		})
	}
	t.Run("redirect", func(t *testing.T) {
		visited := false
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { visited = true }))
		defer target.Close()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
		defer srv.Close()
		n, _ := New(Profile{ID: "fixture", Name: "fixture", URL: srv.URL, Token: "secret"})
		_, err := n.Execute(t.Context(), Request{Action: "terminate", UpstreamID: "42"})
		if err == nil || visited {
			t.Fatal("followed signed supplier redirect")
		}
	})
	t.Run("tls", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"code":0}`) }))
		defer srv.Close()
		n, _ := New(Profile{ID: "fixture", Name: "fixture", URL: srv.URL, Token: "secret"})
		if _, err := n.Execute(t.Context(), Request{Action: "terminate", UpstreamID: "42"}); err == nil {
			t.Fatal("accepted untrusted TLS")
		}
	})
}

func TestNOKVMValidationAndSecretBinding(t *testing.T) {
	for _, u := range []string{"http://supplier.example", "https://a:b@host", "https://host/api", "https://host?token=x", "https://host#fragment", "file:///tmp/fixture"} {
		if _, err := New(Profile{ID: "fixture", Name: "fixture", URL: u, Token: "secret"}); err == nil {
			t.Fatal(u)
		}
	}
	key := []byte("01234567890123456789012345678901")
	value, err := NewCredentials()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal(key, 1, value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), value.Password) {
		t.Fatal("plaintext storage")
	}
	got, err := Unseal(key, 1, sealed)
	if err != nil || got != value {
		t.Fatal(got, err)
	}
	if _, err = Unseal(key, 2, sealed); err == nil {
		t.Fatal("cross-service credential replay")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err = Unseal(key, 1, sealed); err == nil {
		t.Fatal("tampered credentials")
	}
}

func TestNOKVMTimeoutDoesNotRetryCreate(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
		fmt.Fprint(w, `{"code":0,"data":{"id":42}}`)
	}))
	defer srv.Close()
	defer close(release)
	n, _ := New(Profile{ID: "fixture", Name: "fixture", URL: srv.URL, Token: "timeout-secret"})
	n.client.Timeout = 25 * time.Millisecond
	_, err := n.Execute(t.Context(), Request{Action: "create", UserID: 1, ServiceID: 1, Resources: fixtureResources(), Credentials: Credentials{Password: "long-fixture-password", VNCPassword: "long-vnc-password"}})
	var failure *Error
	if !errors.As(err, &failure) || !failure.Unknown || calls.Load() != 1 {
		t.Fatal("timeout safety", calls.Load(), err)
	}
}
