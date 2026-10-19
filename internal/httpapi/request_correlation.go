package httpapi

import (
	"net/http"
	"time"

	"github.com/codex/zjmfgo/internal/correlation"
)

// Only a matched route template is logged: URLs may contain customer data or
// webhook secrets. Query strings, headers, bodies and panic values are omitted.
func requestRoute(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}

func (s *Server) correlateRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Generate at the API trust boundary. Caller supplied IDs never become
		// an authoritative audit identity, even when they look well formed.
		id := correlation.NewID()
		r = r.WithContext(correlation.WithID(r.Context(), id))
		w.Header().Set("X-Request-ID", id)
		recorder := &apiLogResponseWriter{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()
		defer func() {
			log := correlation.Logger(r.Context(), s.log)
			panicValue := recover()
			abort := panicValue != nil && recorder.wroteHeader
			if panicValue != nil {
				log.Error("request panic", "method", r.Method, "route", requestRoute(r))
				if !recorder.wroteHeader {
					writeError(recorder, http.StatusInternalServerError, "internal_error", "请求处理失败")
				}
			}
			log.Info("request completed", "method", r.Method, "route", requestRoute(r), "status", recorder.status, "aborted", abort, "duration_ms", time.Since(started).Milliseconds())
			if abort {
				// Already committed responses cannot become JSON errors. Let the
				// HTTP server close the stream instead of reporting clean success.
				panic(http.ErrAbortHandler)
			}
		}()
		next.ServeHTTP(recorder, r)
	})
}
