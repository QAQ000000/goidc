package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/httpapi"
	"github.com/codex/zjmfgo/internal/modules/manifest"
	"github.com/codex/zjmfgo/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if handled, err := manifest.WriteCLI(os.Args[1:], os.Stdout, "api"); handled {
		if err != nil {
			log.Error("module manifest unavailable", "error", err)
			os.Exit(1)
		}
		return
	}
	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration invalid", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := runAPI(ctx, cfg, log); err != nil {
		log.Error("API stopped", "error", err)
		os.Exit(1)
	}
}

// runAPI owns every runtime resource so startup and serving errors unwind their
// cleanup before main exits nonzero. The diagnostics pool connects lazily and
// does not introduce another database availability check during startup.
func runAPI(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		// A driver error can contain deployment connection details.
		return errors.New("database unavailable")
	}
	defer db.Close()
	if err = db.CheckSchema(ctx); err != nil {
		return fmt.Errorf("schema invalid: %w", err)
	}
	api := httpapi.New(db, cfg, log)
	if err = api.StartAccessLogging(ctx); err != nil {
		return errors.New("access diagnostics unavailable")
	}
	defer func() {
		drain, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := api.CloseAccessLogging(drain); err != nil {
			log.Warn("access diagnostics drain timed out")
		}
	}()
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	return serveAPI(ctx, srv, log, 10*time.Second)
}

func serveAPI(ctx context.Context, srv *http.Server, log *slog.Logger, shutdownTimeout time.Duration) error {
	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("server failed: %w", err)
	}
	log.Info("API started", "address", listener.Addr().String())
	return serveHTTP(ctx, srv, listener, shutdownTimeout)
}

// serveHTTP returns only after accepting has stopped. Successful Shutdown also
// waits for active requests; on deadline Close cancels their connection contexts
// before runAPI stops diagnostics admission and closes the business database.
func serveHTTP(ctx context.Context, srv *http.Server, listener net.Listener, shutdownTimeout time.Duration) error {
	// A fatal Serve error can leave existing connections alive too.
	defer srv.Close()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server failed: %w", err)
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		shutdownErr := srv.Shutdown(shutdown)
		if shutdownErr != nil {
			// Shutdown only closes listeners when its deadline expires. Close is
			// required to cancel active requests and unblock their database work.
			closeErr := srv.Close()
			<-done
			return fmt.Errorf("server shutdown failed: %w", errors.Join(shutdownErr, closeErr))
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server failed: %w", err)
		}
		return nil
	}
}
