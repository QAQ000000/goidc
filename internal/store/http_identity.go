package store

import (
	"context"
	"net"

	"github.com/codex/zjmfgo/internal/correlation"
)

// HTTPIdentity is the active account resolved from an opaque credential hash.
// Permission grants and route policy remain the caller's responsibility.
type HTTPIdentity struct {
	ID       int64
	Username string
	Email    string
	Name     string
	Role     string
}

func (s *Store) SessionIdentity(ctx context.Context, hash string) (HTTPIdentity, error) {
	var id HTTPIdentity
	err := s.Pool.QueryRow(ctx, `SELECT u.id,u.username,u.email,u.display_name,u.role FROM sessions s JOIN users u ON u.id=s.user_id
 WHERE s.token_hash=$1 AND s.expires_at>now() AND u.status='active'`, hash).Scan(&id.ID, &id.Username, &id.Email, &id.Name, &id.Role)
	return id, err
}

func (s *Store) APIKeyIdentity(ctx context.Context, hash string) (HTTPIdentity, error) {
	var id HTTPIdentity
	err := s.Pool.QueryRow(ctx, `SELECT u.id,u.username,u.email,u.display_name,u.role FROM api_keys k JOIN users u ON u.id=k.user_id
 WHERE k.secret_hash=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now()) AND u.status='active'`, hash).Scan(&id.ID, &id.Username, &id.Email, &id.Name, &id.Role)
	if err == nil {
		// Usage timestamps have always been best effort and must not turn a
		// valid credential into a failed request.
		_, _ = s.Pool.Exec(ctx, "UPDATE api_keys SET last_used_at=now() WHERE secret_hash=$1", hash)
	}
	return id, err
}

func (s *Store) CustomerResourceAPIEnabled(ctx context.Context) (bool, error) {
	var enabled bool
	err := s.Pool.QueryRow(ctx, "SELECT enabled FROM resource_api_settings WHERE id=1").Scan(&enabled)
	return enabled, err
}

// APIAccessRecord intentionally contains no query, request body or credentials.
type APIAccessRecord struct {
	ActorID        int64
	Method         string
	Path           string
	StatusCode     int
	DurationMillis int64
	RemoteIP       net.IP
}

func (s *Store) RecordAPIAccess(ctx context.Context, record APIAccessRecord) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO admin_api_logs(actor_id,method,path,status_code,duration_ms,remote_ip,correlation_id)
VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''))`, record.ActorID, record.Method, record.Path, record.StatusCode, record.DurationMillis, record.RemoteIP, correlation.ID(ctx))
	return err
}
