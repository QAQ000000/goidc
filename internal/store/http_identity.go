package store

import (
	"context"
	"errors"
	"net"

	"github.com/codex/zjmfgo/internal/correlation"
	"github.com/jackc/pgx/v5"
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
	var owner int64
	err := s.Pool.QueryRow(ctx, "SELECT user_id FROM sessions WHERE token_hash=$1 AND expires_at>now()", hash).Scan(&owner)
	if err == nil {
		table, tableErr := authenticationTable(ctx, s.Pool, owner)
		if tableErr != nil {
			return id, tableErr
		}
		err = s.Pool.QueryRow(ctx, "SELECT u.id,u.username,u.email,u.display_name,u.role FROM "+table+" u JOIN sessions credential ON credential.user_id=u.id WHERE credential.token_hash=$1 AND credential.expires_at>clock_timestamp() AND u.status='active'", hash).Scan(&id.ID, &id.Username, &id.Email, &id.Name, &id.Role)
	}
	if err == nil && id.Role == "customer" {
		binding, bindingErr := readImpersonationBinding(ctx, s.Pool, hash)
		if bindingErr == nil {
			if binding.CustomerID != id.ID {
				return HTTPIdentity{}, pgx.ErrNoRows
			}
			if err = impersonationBindingAllowed(ctx, s.Pool, binding); err != nil {
				return HTTPIdentity{}, err
			}
		} else if !errors.Is(bindingErr, pgx.ErrNoRows) {
			return HTTPIdentity{}, bindingErr
		}
	}
	return id, err
}

func (s *Store) APIKeyIdentity(ctx context.Context, hash string) (HTTPIdentity, error) {
	var id HTTPIdentity
	var owner int64
	err := s.Pool.QueryRow(ctx, "SELECT user_id FROM api_keys WHERE secret_hash=$1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>now())", hash).Scan(&owner)
	if err == nil {
		table, tableErr := authenticationTable(ctx, s.Pool, owner)
		if tableErr != nil {
			return id, tableErr
		}
		err = s.Pool.QueryRow(ctx, "SELECT u.id,u.username,u.email,u.display_name,u.role FROM "+table+" u JOIN api_keys credential ON credential.user_id=u.id WHERE credential.secret_hash=$1 AND credential.revoked_at IS NULL AND (credential.expires_at IS NULL OR credential.expires_at>clock_timestamp()) AND u.status='active'", hash).Scan(&id.ID, &id.Username, &id.Email, &id.Name, &id.Role)
	}
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
