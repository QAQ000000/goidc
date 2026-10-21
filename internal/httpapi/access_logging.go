package httpapi

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/accesslog"
	"github.com/codex/zjmfgo/internal/correlation"
)

// StartAccessLogging is called by the API process after schema checks and before
// accepting requests. Tests may omit it when access diagnostics are irrelevant.
// The writer uses its own single connection; no request waits for a log INSERT.
func (s *Server) StartAccessLogging(ctx context.Context) error {
	s.accessLogMu.Lock()
	defer s.accessLogMu.Unlock()
	if s.accessLog != nil {
		return nil
	}
	writer, err := s.db.OpenAPIAccessWriter(ctx)
	if err != nil {
		return err
	}
	options := accesslog.DefaultOptions()
	options.Report = func(stats accesslog.Stats) {
		s.log.Warn("access diagnostics degraded", "dropped", stats.Dropped, "write_failed", stats.WriteFailed, "cleanup_failed", stats.CleanupFailed, "cleanup_budget_exhausted", stats.CleanupBudgetExhausted, "shutdown_discarded", stats.ShutdownDiscarded, "queue_depth", stats.QueueDepth)
	}
	s.accessLog = accesslog.New(writer, options)
	return nil
}

// CloseAccessLogging must run after HTTP shutdown and before Store.Close.
func (s *Server) CloseAccessLogging(ctx context.Context) error {
	s.accessLogMu.RLock()
	sink := s.accessLog
	s.accessLogMu.RUnlock()
	if sink == nil {
		return nil
	}
	return sink.Shutdown(ctx)
}

func (s *Server) accessLogStats() accesslog.Stats {
	s.accessLogMu.RLock()
	sink := s.accessLog
	s.accessLogMu.RUnlock()
	if sink == nil {
		return accesslog.Stats{Status: "disabled"}
	}
	return sink.Snapshot()
}

func (s *Server) enqueueAPIAccess(r *http.Request, actor int64, status int, started time.Time) {
	s.accessLogMu.RLock()
	sink := s.accessLog
	s.accessLogMu.RUnlock()
	if sink == nil {
		return
	}
	path := r.URL.Path
	if _, matched, ok := strings.Cut(r.Pattern, " "); ok {
		path = matched
	}
	sink.TryEnqueue(accesslog.Record{ActorID: actor, Method: r.Method, Path: path, StatusCode: status, DurationMillis: time.Since(started).Milliseconds(), RemoteIP: net.ParseIP(s.clientIP(r)), OccurredAt: time.Now().UTC(), CorrelationID: correlation.ID(r.Context())})
}
