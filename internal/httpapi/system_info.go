package httpapi

import (
	"net/http"
	"os"
	"runtime"
	"strings"

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

func (s *Server) adminSystemDatabase(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 240 {
		writeError(w, http.StatusBadRequest, "invalid_database_filter", "表名称搜索过长")
		return
	}
	var database, serverVersion, migration string
	var sizeBytes int64
	if err := s.db.Pool.QueryRow(r.Context(), `SELECT current_database(),version(),pg_database_size(current_database()),coalesce((SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1),'')`).Scan(&database, &serverVersion, &sizeBytes, &migration); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "数据库信息不可用")
			return
		}
		s.fail(w, r, err)
		return
	}
	stat := s.db.Pool.Stat()
	// Scope to the application's search_path schema, including in isolated tests.
	// n_live_tup is PostgreSQL's estimate; never claim this as an exact row count.
	where := ` FROM pg_stat_user_tables WHERE schemaname=current_schema() AND ($1='' OR relname ILIKE '%'||$1||'%')`
	page, err := listPage(r.Context(), s.db.Pool, "SELECT count(*)"+where,
		`SELECT relname AS name,n_live_tup AS estimated_rows,pg_total_relation_size(relid) AS size_bytes`+where+` ORDER BY relname`, []any{query}, limit, offset, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{
		"items": page["items"], "total": page["total"], "limit": limit, "offset": offset,
		"status":            "ready",
		"database":          database,
		"server_version":    serverVersion,
		"database_size":     sizeBytes,
		"migration_version": migration,
		"pool": map[string]any{
			"max_connections":      stat.MaxConns(),
			"total_connections":    stat.TotalConns(),
			"idle_connections":     stat.IdleConns(),
			"acquired_connections": stat.AcquiredConns(),
			"empty_acquires":       stat.EmptyAcquireCount(),
		},
	})
}
