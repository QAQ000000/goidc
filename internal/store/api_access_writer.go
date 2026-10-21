package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/accesslog"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These hard limits bound individual SQL statements, including callers outside
// the asynchronous writer. The dispatcher normally uses substantially less.
const APIAccessBatchLimit = 1000
const APIAccessPruneLimit = 1000

// APIAccessWriter owns a single separate database connection. Log backpressure
// can occupy this connection but cannot acquire or wait for a business lease.
// It deliberately retains the business database/schema, TLS and connection
// hooks; it does not remove the need to bound database log write volume.
type APIAccessWriter struct {
	pool *pgxpool.Pool
}

func (s *Store) OpenAPIAccessWriter(ctx context.Context) (*APIAccessWriter, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("API access log database is unavailable")
	}
	cfg := s.Pool.Config().Copy()
	cfg.MaxConns, cfg.MinConns = 1, 0
	if cfg.ConnConfig.ConnectTimeout == 0 || cfg.ConnConfig.ConnectTimeout > 5*time.Second {
		cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &APIAccessWriter{pool: pool}, nil
}

// WriteBatch commits one atomic INSERT, with each event's original timestamp and
// diagnostic origin. The writer's own context must never become an event's
// correlation ID; explicitly empty origins are stored as NULL.
func (w *APIAccessWriter) WriteBatch(ctx context.Context, records []accesslog.Record) error {
	return writeAPIAccessBatch(ctx, w.pool, records)
}

func writeAPIAccessBatch(ctx context.Context, pool *pgxpool.Pool, records []accesslog.Record) error {
	if len(records) == 0 {
		return nil
	}
	if len(records) > APIAccessBatchLimit {
		return fmt.Errorf("API access log batch exceeds %d records", APIAccessBatchLimit)
	}
	var query strings.Builder
	query.WriteString(`INSERT INTO admin_api_logs(actor_id,method,path,status_code,duration_ms,remote_ip,created_at,correlation_id) VALUES `)
	args := make([]any, 0, len(records)*8)
	for i, record := range records {
		if record.OccurredAt.IsZero() {
			return errors.New("API access log event timestamp is required")
		}
		if i > 0 {
			query.WriteByte(',')
		}
		n := len(args)
		fmt.Fprintf(&query, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,NULLIF($%d,''))", n+1, n+2, n+3, n+4, n+5, n+6, n+7, n+8)
		// The record has no body, headers or query fields. Strip query/fragment
		// here as an additional boundary for explicit compatibility callers.
		cleanPath := record.Path
		if at := strings.IndexAny(cleanPath, "?#"); at >= 0 {
			cleanPath = cleanPath[:at]
		}
		args = append(args, record.ActorID, record.Method, cleanPath, record.StatusCode, record.DurationMillis, record.RemoteIP, record.OccurredAt, record.CorrelationID)
	}
	_, err := pool.Exec(ctx, query.String(), args...)
	return err
}

// Prune removes at most limit events strictly older than before. Locked rows
// are skipped so concurrent retention workers and inserts do not wait on one
// another. Events on the retention boundary remain available to administrators.
func (w *APIAccessWriter) Prune(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	if limit > APIAccessPruneLimit || before.IsZero() {
		return 0, errors.New("invalid API access log retention bounds")
	}
	tag, err := w.pool.Exec(ctx, `DELETE FROM admin_api_logs log USING (
 SELECT id FROM admin_api_logs WHERE created_at<$1 ORDER BY created_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
) expired WHERE log.id=expired.id`, before, limit)
	return tag.RowsAffected(), err
}

func (w *APIAccessWriter) Close() {
	if w != nil && w.pool != nil {
		w.pool.Close()
	}
}
