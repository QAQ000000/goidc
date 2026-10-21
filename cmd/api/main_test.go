package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func apiTestListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

func awaitAPIResult[T any](t *testing.T, result <-chan T) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("API lifecycle did not finish within test deadline")
		var zero T
		return zero
	}
}

func TestServeHTTPGracefullyDrainsActiveRequest(t *testing.T) {
	listener := apiTestListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	requestCancelled := make(chan struct{}, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
			requestCancelled <- struct{}{}
		}
	})}
	t.Cleanup(func() { srv.Close() })
	served := make(chan error, 1)
	go func() { served <- serveHTTP(ctx, srv, listener, time.Second) }()
	response := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		res, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			res.Body.Close()
			if res.StatusCode != http.StatusNoContent {
				err = errors.New("active request did not finish successfully")
			}
		}
		response <- err
	}()
	awaitAPIResult(t, entered)
	cancel()
	select {
	case err := <-served:
		t.Fatalf("server stopped before its active request finished: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := awaitAPIResult(t, response); err != nil {
		t.Fatal(err)
	}
	if err := awaitAPIResult(t, served); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestCancelled:
		t.Fatal("graceful shutdown cancelled the active request")
	default:
	}
}

func TestServeHTTPShutdownDeadlineClosesActiveRequest(t *testing.T) {
	listener := apiTestListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, requestCancelled := make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(requestCancelled)
	})}
	t.Cleanup(func() { srv.Close() })
	served := make(chan error, 1)
	go func() { served <- serveHTTP(ctx, srv, listener, 25*time.Millisecond) }()
	requestDone := make(chan struct{}, 1)
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		res, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			res.Body.Close()
		}
		requestDone <- struct{}{}
	}()
	awaitAPIResult(t, entered)
	cancel()
	if err := awaitAPIResult(t, served); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown deadline should be returned to main, got %v", err)
	}
	awaitAPIResult(t, requestCancelled)
	awaitAPIResult(t, requestDone)
}

func TestServeAPIBindConflictReturnsFailure(t *testing.T) {
	occupied := apiTestListener(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := serveAPI(context.Background(), &http.Server{Addr: occupied.Addr().String()}, log, time.Second)
	if err == nil || !strings.Contains(err.Error(), "server failed") {
		t.Fatalf("bind conflict must fail for main's nonzero exit, got %v", err)
	}
	var bindError *net.OpError
	if !errors.As(err, &bindError) || bindError.Op != "listen" {
		t.Fatalf("underlying listen failure was lost: %v", err)
	}
}
