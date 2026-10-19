package httpapi

import (
	"context"
	"net/http"
	"time"
)

// Diagnostic state is independent of API readiness: workers start after the
// ready API in the release controller. No diagnostic read executes recovery.
func (s *Server) workerRuntime(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	result, err := s.db.WorkerRuntimeDiagnostics(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}
