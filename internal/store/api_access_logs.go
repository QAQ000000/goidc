package store

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type APIAccessLogFilter struct {
	Method        string
	StatusCode    int
	RemoteIP      net.IP
	Username      string
	Search        string
	CorrelationID string
	From          *time.Time
	Until         *time.Time
}

// APIAccessLogs keeps the historical administrator-only log scope. Route
// authorization and HTTP input validation are performed before this call.
func (s *Store) APIAccessLogs(ctx context.Context, filter APIAccessLogFilter, limit, offset int) (map[string]any, error) {
	where := []string{"u.role IN ('admin','super_admin')"}
	args := []any{}
	add := func(value any, expression string) {
		args = append(args, value)
		where = append(where, strings.ReplaceAll(expression, "?", "$"+fmt.Sprint(len(args))))
	}
	if filter.Method != "" {
		add(filter.Method, "a.method=?")
	}
	if filter.StatusCode != 0 {
		add(filter.StatusCode, "a.status_code=?")
	}
	if filter.RemoteIP != nil {
		add(filter.RemoteIP, "a.remote_ip=?")
	}
	if filter.Username != "" {
		add("%"+filter.Username+"%", "(u.display_name ILIKE ? OR u.username ILIKE ? OR u.email ILIKE ?)")
	}
	if filter.Search != "" {
		add("%"+filter.Search+"%", "(a.path ILIKE ? OR a.method ILIKE ? OR coalesce(u.display_name,'') ILIKE ? OR a.correlation_id ILIKE ?)")
	}
	if filter.CorrelationID != "" {
		add(filter.CorrelationID, "a.correlation_id=?")
	}
	if filter.From != nil {
		add(*filter.From, "a.created_at>=?")
	}
	if filter.Until != nil {
		add(*filter.Until, "a.created_at<?")
	}
	base := " FROM admin_api_logs a LEFT JOIN users u ON u.id=a.actor_id WHERE " + strings.Join(where, " AND ")
	var total int64
	if err := s.Pool.QueryRow(ctx, "SELECT count(*)"+base, args...).Scan(&total); err != nil {
		return nil, err
	}
	args = append(args, limit, offset)
	rows, err := s.Pool.Query(ctx, `SELECT a.id,a.created_at,a.method,a.path,a.path AS description,a.status_code,a.duration_ms,a.remote_ip,a.remote_ip AS ip,
 a.actor_id,u.display_name AS username,coalesce(a.correlation_id,'') AS correlation_id`+base+
		fmt.Sprintf(" ORDER BY a.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []map[string]any{}
	}
	return map[string]any{"items": items, "total": total, "limit": limit, "offset": offset}, nil
}
