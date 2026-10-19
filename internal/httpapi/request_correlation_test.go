package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/correlation"
	"github.com/codex/zjmfgo/internal/store"
)

func TestRequestCorrelationEarlyRejection(t *testing.T) {
	var logs bytes.Buffer
	s := &Server{db: &store.Store{}, log: slog.New(slog.NewJSONHandler(&logs, nil))}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/v1/auth/login?password=private-query", nil)
	r.Header.Set("X-Request-ID", "0123456789abcdef0123456789abcdef")
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d", w.Code)
	}
	id := w.Header().Get("X-Request-ID")
	if len(id) != 32 || id == r.Header.Get("X-Request-ID") {
		t.Fatalf("API did not generate a fresh ID: %q", id)
	}
	var body struct {
		Error struct {
			CorrelationID string `json:"correlation_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.CorrelationID != id {
		t.Fatalf("error/header identity differs: %s (%v)", w.Body.String(), err)
	}
	if !strings.Contains(logs.String(), id) || strings.Contains(logs.String(), "private-query") {
		t.Fatal("early error log missing correlation or disclosing query")
	}
}

func TestRequestCorrelationPanicAndSecretOmission(t *testing.T) {
	var logs bytes.Buffer
	s := &Server{log: slog.New(slog.NewJSONHandler(&logs, nil))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback/{secret}", func(w http.ResponseWriter, r *http.Request) {
		if correlation.ID(r.Context()) != w.Header().Get("X-Request-ID") {
			t.Fatal("context/header differ")
		}
		panic("private-panic")
	})
	w := httptest.NewRecorder()
	s.correlateRequests(mux).ServeHTTP(w, httptest.NewRequest("GET", "/callback/private-path?token=private-query", nil))
	if w.Code != 500 || !strings.Contains(w.Body.String(), w.Header().Get("X-Request-ID")) {
		t.Fatal(w.Body.String())
	}
	text := logs.String()
	for _, secret := range []string{"private-panic", "private-path", "private-query"} {
		if strings.Contains(text, secret) {
			t.Fatalf("log disclosed %s", secret)
		}
	}
	if !strings.Contains(text, "GET /callback/{secret}") || !strings.Contains(text, "request panic") {
		t.Fatal(text)
	}
}

func TestRequestCorrelationConcurrentIsolation(t *testing.T) {
	s := &Server{log: slog.New(slog.DiscardHandler)}
	var ids sync.Map
	h := s.correlateRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := correlation.ID(r.Context())
		if id != w.Header().Get("X-Request-ID") || len(id) != 32 {
			t.Error("missing request identity")
		}
		if _, duplicate := ids.LoadOrStore(id, true); duplicate {
			t.Error("request IDs reused")
		}
		writeError(w, 401, "unauthorized", "请先登录")
	}))
	var workers sync.WaitGroup
	for range 50 {
		workers.Go(func() {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/unknown", nil))
			if w.Code != 401 {
				t.Error("status differs")
			}
		})
	}
	workers.Wait()
}

func TestRequestCorrelationCommittedPanicAborts(t *testing.T) {
	s := &Server{log: slog.New(slog.DiscardHandler)}
	h := s.correlateRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("partial"))
		panic("secret")
	}))
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Error("committed panic must abort the HTTP stream")
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/file", nil))
}
