package httpapi

import (
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/jackc/pgx/v5"
)

// adminSystemUpgrade is deliberately read-only. Applying migrations remains an
// explicit deployment operation so a browser refresh cannot mutate production.
func (s *Server) adminSystemUpgrade(w http.ResponseWriter, r *http.Request) {
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	compiled := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			compiled = append(compiled, entry.Name())
		}
	}
	sort.Strings(compiled)
	appliedRows, err := s.db.Pool.Query(r.Context(), "SELECT version FROM schema_migrations")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	appliedValues, err := pgx.CollectRows(appliedRows, pgx.RowTo[string])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	applied := make(map[string]bool, len(appliedValues))
	for _, version := range appliedValues {
		applied[version] = true
	}
	pending := make([]string, 0)
	for _, version := range compiled {
		if !applied[version] {
			pending = append(pending, version)
		}
	}
	latestApplied := ""
	for _, version := range compiled {
		if applied[version] {
			latestApplied = version
		}
	}
	status := "up_to_date"
	if len(pending) > 0 {
		status = "pending"
	}
	write(w, http.StatusOK, map[string]any{
		"status":             status,
		"build_version":      os.Getenv("ZJMFGO_BUILD_VERSION"),
		"latest_applied":     latestApplied,
		"latest_available":   compiled[len(compiled)-1],
		"pending_migrations": pending,
		"migration_count":    map[string]int{"compiled": len(compiled), "applied": len(appliedValues), "pending": len(pending)},
	})
}
