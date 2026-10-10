package httpapi

import (
	"net/http"
	"os"
	"runtime"

	"github.com/jackc/pgx/v5"
)

func (s *Server) adminSystemRuntime(w http.ResponseWriter, r *http.Request) {
	var database, serverVersion, migration string
	var sizeBytes int64
	err := s.db.Pool.QueryRow(r.Context(), `SELECT current_database(),version(),pg_database_size(current_database()),coalesce((SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1),'')`).Scan(&database, &serverVersion, &sizeBytes, &migration)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"runtime": "go", "go_version": runtime.Version(), "go_os": runtime.GOOS, "go_arch": runtime.GOARCH, "goroutines": runtime.NumGoroutine(), "database": database, "database_version": serverVersion, "database_size": sizeBytes, "migration_version": migration, "build_version": os.Getenv("ZJMFGO_BUILD_VERSION")})
}

func (s *Server) adminSystemAbout(w http.ResponseWriter, r *http.Request) {
	var databaseVersion string
	if err := s.db.Pool.QueryRow(r.Context(), "SELECT version()").Scan(&databaseVersion); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "数据库信息不可用")
			return
		}
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"product": "智简魔方", "backend": "Go API", "frontend": "Next.js + React", "database": databaseVersion, "go_version": runtime.Version(), "build_version": os.Getenv("ZJMFGO_BUILD_VERSION"), "runtime_contract": "api-v1"})
}
