package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestRequestRouteSurvivesBrowserSessionProjection(t *testing.T) {
	for _, test := range []struct {
		name, method, path, route string
		status                    int
	}{
		{"health", "GET", "/healthz?token=private-query", "GET /healthz", 200},
		{"parameterized route", "GET", "/api/v1/admin/customers/private-customer?token=private-query", "GET /api/v1/admin/customers/{id}", 401},
		{"unmatched", "GET", "/private-unmatched?token=private-query", "unmatched", 404},
		{"early csrf refusal", "POST", "/api/v1/admin/customers/private-customer?token=private-query", "unmatched", 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			s := &Server{db: &store.Store{}, log: slog.New(slog.NewJSONHandler(&logs, nil))}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
			if w.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, test.status, w.Body.String())
			}
			events := requestRouteLogEvents(t, logs.String())
			if len(events) != 1 || events[0]["msg"] != "request completed" || events[0]["route"] != test.route {
				t.Fatalf("matched route template lost through request clone: %s", logs.String())
			}
			if events[0]["correlation_id"] != w.Header().Get("X-Request-ID") {
				t.Fatalf("completion log lost request correlation: %s", logs.String())
			}
			for _, secret := range []string{"private-query", "private-customer", "private-unmatched"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("raw path or query leaked: %s", logs.String())
				}
			}
		})
	}
}

func TestRequestRouteProjectionPanicKeepsTemplateAndPathValues(t *testing.T) {
	var logs bytes.Buffer
	s := &Server{db: &store.Store{}, log: slog.New(slog.NewJSONHandler(&logs, nil))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /route-fixture/{secret}", func(_ http.ResponseWriter, r *http.Request) {
		if value := r.PathValue("secret"); value != "private-value" {
			t.Fatalf("ServeMux path value was lost: %q", value)
		}
		panic("private-panic")
	})
	handler := s.correlateRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		projected := s.projectBrowserSession(r, "customer")
		defer rememberRequestRoute(projected)
		mux.ServeHTTP(w, projected)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/route-fixture/private-value?token=private-query", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("panic response = %d: %s", w.Code, w.Body.String())
	}
	events := requestRouteLogEvents(t, logs.String())
	if len(events) != 2 {
		t.Fatalf("panic/completion logs missing: %s", logs.String())
	}
	for _, event := range events {
		if event["route"] != "GET /route-fixture/{secret}" || event["correlation_id"] != w.Header().Get("X-Request-ID") {
			t.Fatalf("panic lost matched route or correlation: %s", logs.String())
		}
	}
	for _, secret := range []string{"private-value", "private-query", "private-panic"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("panic diagnostics disclosed private input: %s", logs.String())
		}
	}
}

func TestRequestRouteTrackerConcurrentIsolation(t *testing.T) {
	s := &Server{log: slog.New(slog.DiscardHandler)}
	var workers sync.WaitGroup
	for requestID := range 16 {
		workers.Go(func() {
			pattern := fmt.Sprintf("GET /private-fixture-%d/{id}", requestID)
			s.correlateRequests(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				if got := requestRoute(r); got != "unmatched" {
					t.Errorf("new request inherited a route: %q", got)
				}
				var users sync.WaitGroup
				for range 8 {
					users.Go(func() {
						projected := r.Clone(r.Context())
						projected.Pattern = pattern
						rememberRequestRoute(projected)
						if got := requestRoute(r); got != projected.Pattern {
							t.Errorf("tracker changed between clones: %q", got)
						}
					})
				}
				users.Wait()
			})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/private-value", nil))
		})
	}
	workers.Wait()
}

func requestRouteLogEvents(t *testing.T, logs string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid diagnostic log %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}
