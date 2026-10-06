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

func TestNOKVMPasswordProtocolAndConfirmation(t *testing.T) {
	const password = "fixture-A&+=%secret123!"
	for _, mode := range []string{"confirmed", "rejected", "unknown", "mismatch", "duplicate", "busy"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get("signature") != Signature("fixture-token", q.Get("time"), q.Get("random")) {
					t.Error("unsigned request")
				}
				switch {
				case strings.Contains(r.URL.Path, "run_status"):
					if mode == "busy" {
						fmt.Fprint(w, `{"code":0,"data":{"code":4}}`)
					} else {
						fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
					}
				case r.URL.Path == "/api/virtual_reset_password/42":
					calls.Add(1)
					r.ParseForm()
					if r.Method != "PUT" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.PostForm.Get("type") != "system" || r.PostForm.Get("password") != password || len(r.PostForm) != 2 {
						t.Error("password wire protocol")
					}
					if mode == "rejected" {
						fmt.Fprint(w, `{"code":8}`)
					} else if mode == "unknown" {
						fmt.Fprint(w, `{"message":"remote-secret"}`)
					} else {
						fmt.Fprint(w, `{"code":0}`)
					}
				case r.URL.Path == "/api/virtual/42":
					if mode == "mismatch" {
						fmt.Fprint(w, `{"code":0,"data":{"sys_pwd":"remote-secret"}}`)
					} else if mode == "duplicate" {
						w.Write([]byte(`{"code":0,"data":{"sys_pwd":"remote-secret","SYS_PWD":"fixture-A&+=%secret123!"}}`))
					} else {
						fmt.Fprintf(w, `{"code":0,"data":{"sys_pwd":%q}}`, password)
					}
				default:
					t.Error("unexpected endpoint", r.URL.Path)
				}
			}))
			defer srv.Close()
			n, _ := New(Profile{ID: "fixture", Name: "fixture", URL: srv.URL, Token: "fixture-token"})
			result, err := n.Execute(t.Context(), Request{Action: "reset_password", UpstreamID: "42", Credentials: Credentials{Password: password}})
			if mode == "confirmed" {
				if err != nil || !result.PasswordConfirmed || !result.CommandAccepted || calls.Load() != 1 || strings.Contains(fmt.Sprint(result), password) {
					t.Fatal("confirmed reset", err)
				}
			} else {
				var failure *Error
				unknown := mode != "rejected" && mode != "busy"
				if !errors.As(err, &failure) || failure.Unknown != unknown || strings.Contains(err.Error(), password) || strings.Contains(err.Error(), "remote-secret") {
					t.Fatal(err)
				}
				want := int32(1)
				if mode == "busy" {
					want = 0
				}
				if calls.Load() != want {
					t.Fatal("reset count", calls.Load())
				}
			}
		})
	}
}

func TestPasswordValidationAndJobSecretBinding(t *testing.T) {
	for _, password := range []string{"short", "12345678901", strings.Repeat("a", 129), "valid-size\nsecret", "nonascii-密码123", "password with spaces"} {
		if ValidSystemPassword(password) {
			t.Fatal("accepted invalid system password")
		}
	}
	key := []byte("01234567890123456789012345678901")
	value := Credentials{Password: "fixture-password123!"}
	sealed, err := SealJobCredentials(key, 1, "fixture-job-a", value)
	if err != nil || strings.Contains(string(sealed), value.Password) {
		t.Fatal(err)
	}
	got, err := UnsealJobCredentials(key, 1, "fixture-job-a", sealed)
	if err != nil || got != value {
		t.Fatal(err)
	}
	if _, err = UnsealJobCredentials(key, 2, "fixture-job-a", sealed); err == nil {
		t.Fatal("cross service job secret")
	}
	if _, err = UnsealJobCredentials(key, 1, "fixture-job-b", sealed); err == nil {
		t.Fatal("cross job secret")
	}
	if _, err = Unseal(key, 1, sealed); err == nil {
		t.Fatal("pending secret usable as current credential")
	}
}
