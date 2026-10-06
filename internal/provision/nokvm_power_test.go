package provision

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNOKVMPowerProtocol(t *testing.T) {
	for action, command := range map[string]string{"power_on": "start", "power_off": "shut", "reboot": "restart", "force_off": "compel_shut", "force_reboot": "compel_restart"} {
		t.Run(action, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if r.Method != "GET" || q.Get("signature") != Signature("fixture-token", q.Get("time"), q.Get("random")) {
					t.Error("unsigned power GET")
				}
				if calls.Add(1) == 1 {
					if r.URL.Path != "/api/virtual_run_status/42" {
						t.Error("missing preflight")
					}
					state := 1
					if action == "power_on" {
						state = 2
					}
					fmt.Fprintf(w, `{"code":0,"data":{"code":%d}}`, state)
				} else {
					if r.URL.Path != "/api/virtual_power/42/"+command {
						t.Error(r.URL.Path)
					}
					fmt.Fprint(w, `{"code":0}`)
				}
			}))
			defer srv.Close()
			n, _ := New(Profile{ID: "fixture", Name: "fixture", URL: srv.URL, Token: "fixture-token"})
			result, err := n.Execute(t.Context(), Request{Action: action, UpstreamID: "42"})
			if err != nil || !result.CommandAccepted || result.Power != "processing" || result.UpstreamID != "42" || calls.Load() != 2 {
				t.Fatal(result, err, calls.Load())
			}
		})
	}
}

func TestNOKVMPowerPreflightAndAmbiguousReplies(t *testing.T) {
	for _, tc := range []struct {
		name, action, state, reply string
		unknown, send              bool
	}{
		{"already_on", "power_on", `{"code":1}`, `{"code":0}`, false, false},
		{"already_off", "reboot", `{"code":3}`, `{"code":0}`, false, false},
		{"processing", "force_off", `{"code":4}`, `{"code":0}`, false, false},
		{"suspended", "power_off", `{"code":13}`, `{"code":0}`, false, false},
		{"limited", "force_reboot", `{"code":14}`, `{"code":0}`, false, false},
		{"invalid_state", "reboot", `{"code":999}`, `{"code":0}`, false, false},
		{"duplicate_state", "reboot", `{"code":4,"Code":1}`, `{"code":0}`, false, false},
		{"missing_reply_code", "reboot", `{"code":1}`, `{"message":"secret"}`, true, true},
		{"duplicate_reply", "reboot", `{"code":1}`, `{"code":8,"code":0}`, true, true},
		{"case_alias", "reboot", `{"code":1}`, `{"code":0,"Code":8}`, true, true},
		{"supplier_reject", "reboot", `{"code":1}`, `{"code":8}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "run_status") {
					fmt.Fprintf(w, `{"code":0,"data":%s}`, tc.state)
				} else {
					calls.Add(1)
					fmt.Fprint(w, tc.reply)
				}
			}))
			defer srv.Close()
			n, _ := New(Profile{ID: "fixture", Name: "fixture", URL: srv.URL, Token: "secret"})
			_, err := n.Execute(t.Context(), Request{Action: tc.action, UpstreamID: "42"})
			var failure *Error
			if !errors.As(err, &failure) || failure.Unknown != tc.unknown || (calls.Load() == 1) != tc.send {
				t.Fatal(err, calls.Load())
			}
		})
	}
}
