package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNOKVMStorageWireProtocolAndMembership(t *testing.T) {
	for _, kind := range []string{"snapshot", "backup"} {
		t.Run(kind, func(t *testing.T) {
			path, _ := storagePath(kind)
			var calls []string
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if len(q) != 3 || q.Get("signature") != Signature("storage-fixture-token", q.Get("time"), q.Get("random")) {
					t.Error("invalid signed storage request")
				}
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.URL.Path == path+"/42" && r.Method == http.MethodGet {
					fmt.Fprint(w, `{"code":0,"data":[{"id":9007199254740993,"name":"系统盘还原点","created_at":"2026-10-06 12:00:00","disk_path":"supplier-private-path","sys_pwd":"supplier-secret"},{"id":"7","name":"较新还原点","created_at":"2026-10-06T05:00:00Z"}]}`)
					return
				}
				if r.Method == http.MethodPost {
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					if r.PostForm.Encode() != "virtual=42" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
						t.Error("storage create wire body")
					}
				} else if r.ContentLength > 0 {
					t.Error("delete/restore must not invent a form")
				}
				fmt.Fprint(w, `{"code":0,"message":"supplier-secret accepted"}`)
			}))
			defer remote.Close()
			n, err := New(Profile{ID: "fixture", Name: "fixture", URL: remote.URL, Token: "storage-fixture-token"})
			if err != nil {
				t.Fatal(err)
			}
			points, err := n.ListStorage(t.Context(), kind, "42")
			if err != nil || len(points) != 2 || points[0].ID != "7" || points[1].ID != "9007199254740993" || points[1].CreatedAt.Format("2006-01-02T15:04:05Z") != "2026-10-06T04:00:00Z" {
				t.Fatal(points, err)
			}
			encoded, _ := json.Marshal(points)
			if strings.Contains(string(encoded), "supplier-") {
				t.Fatal("storage listing leaked raw supplier fields")
			}
			for _, action := range []string{"create", "delete", "restore"} {
				id := "9007199254740993"
				if action == "create" {
					id = ""
				}
				result, err := n.StorageAction(t.Context(), kind, action, "42", id)
				var failure *Error
				if !errors.As(err, &failure) || failure.Code != "storage_confirmation_pending" || !failure.Unknown || !result.CommandAccepted || result.UpstreamID != "42" {
					t.Fatal("acceptance was declared complete", result, err)
				}
			}
			before := len(calls)
			_, err = n.StorageAction(t.Context(), kind, "restore", "42", "99")
			var failure *Error
			if !errors.As(err, &failure) || failure.Code != "storage_point_not_found" || failure.Unknown || len(calls) != before+1 {
				t.Fatal("foreign restore point reached a command", err, calls)
			}
			want := []string{"GET " + path + "/42", "POST " + path + "/", "GET " + path + "/42", "DELETE " + path + "/9007199254740993", "GET " + path + "/42", "GET " + path + "/9007199254740993/edit", "GET " + path + "/42"}
			if strings.Join(calls, "|") != strings.Join(want, "|") {
				t.Fatal(calls)
			}
		})
	}
}

func TestNOKVMStorageInvalidListsCannotAuthorizeCommands(t *testing.T) {
	for _, payload := range []string{
		`{"code":0,"data":null}`, `{"code":0,"data":{}}`,
		`{"code":0,"data":[{"id":1,"ID":2,"name":"one","created_at":"2026-10-06T04:00:00Z"}]}`,
		`{"code":0,"data":[{"id":1,"name":"one","created_at":"2026-10-06T04:00:00Z"},{"id":1,"name":"two","created_at":"2026-10-06T04:00:00Z"}]}`,
		`{"code":0,"data":[{"id":0,"name":"one","created_at":"2026-10-06T04:00:00Z"}]}`,
		`{"code":0,"data":[{"id":9223372036854775808,"name":"one","created_at":"2026-10-06T04:00:00Z"}]}`,
		`{"code":0,"data":[{"id":1,"name":"one\nsecret","created_at":"2026-10-06T04:00:00Z"}]}`,
		`{"code":0,"data":[{"id":1,"name":"one","created_at":"invalid-supplier-secret"}]}`,
		`{"code":0,"data":[{"id":1,"name":"","created_at":"2026-10-06T04:00:00Z"}]}`,
		`{"code":7,"message":"supplier-private-message"}`,
	} {
		var writes atomic.Int32
		remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/snapshot/42" {
				writes.Add(1)
			}
			fmt.Fprint(w, payload)
		}))
		n, err := New(Profile{ID: "fixture", Name: "fixture", URL: remote.URL, Token: "fixture-token"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = n.StorageAction(t.Context(), "snapshot", "delete", "42", "1")
		var failure *Error
		if !errors.As(err, &failure) || failure.Unknown || writes.Load() != 0 || strings.Contains(err.Error(), "supplier-") {
			t.Fatal("unsafe storage preflight", err, writes.Load())
		}
		remote.Close()
	}
}

func TestNOKVMStorageCommandOutcomeAndValidation(t *testing.T) {
	for _, tc := range []struct {
		body    string
		status  int
		unknown bool
	}{
		{`{"code":8,"message":"supplier-secret"}`, 200, false},
		{`{"code":0,"CODE":8}`, 200, true}, {`{`, 200, true}, {`{"code":0}`, 503, true},
	} {
		remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
		n, _ := New(Profile{ID: "fixture", Name: "fixture", URL: remote.URL, Token: "fixture-token"})
		_, err := n.StorageAction(t.Context(), "backup", "create", "42", "")
		var failure *Error
		if !errors.As(err, &failure) || failure.Unknown != tc.unknown || strings.Contains(err.Error(), "supplier-secret") {
			t.Fatal("wrong write outcome", err)
		}
		remote.Close()
	}
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{"code":0,"data":[]}`) }))
	defer remote.Close()
	n, _ := New(Profile{ID: "fixture", Name: "fixture", URL: remote.URL, Token: "fixture-token"})
	for _, args := range [][4]string{{"other", "create", "42", ""}, {"backup", "other", "42", ""}, {"backup", "create", "0", ""}, {"snapshot", "create", "42", "7"}, {"backup", "delete", "42", "../7"}, {"snapshot", "restore", "42", "0"}} {
		if _, err := n.StorageAction(t.Context(), args[0], args[1], args[2], args[3]); err == nil {
			t.Fatal("invalid request accepted", args)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := n.StorageAction(ctx, "snapshot", "create", "42", "")
	var failure *Error
	if !errors.As(err, &failure) || failure.Unknown || calls.Load() != 0 {
		t.Fatal("cancelled/invalid requests sent commands", err)
	}
	points, err := n.ListStorage(t.Context(), "snapshot", "42")
	if err != nil || points == nil || len(points) != 0 {
		t.Fatal("empty list contract", points, err)
	}
}
