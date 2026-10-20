package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

// FileAccountIdentity resolves a credential only in the physical account table
// selected by the fixed file endpoint. Customer/public readers never authenticate
// an administrator; a rejected or absent credential remains anonymous.
func FileAccountIdentity(ctx context.Context, q TicketScopeQuery, hash string, apiKey, admin bool) (int64, error) {
	if hash == "" {
		return 0, nil
	}
	table := "users"
	if admin {
		table = "administrators"
	}
	query := `SELECT u.id FROM sessions s JOIN ` + table + ` u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>clock_timestamp() AND u.status='active'`
	if apiKey {
		query = `SELECT u.id FROM api_keys k JOIN ` + table + ` u ON u.id=k.user_id WHERE k.secret_hash=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp()) AND u.status='active'`
	}
	var actor int64
	err := q.QueryRow(ctx, query, hash).Scan(&actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return actor, err
}

func (s *Store) PublicCustomerContentAccess(ctx context.Context, hash string, apiKey bool) (int, error) {
	if hash == "" {
		return 0, nil
	}
	var actor int64
	query := `SELECT u.id FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>clock_timestamp() AND u.status='active'`
	if apiKey {
		query = `SELECT u.id FROM api_keys k JOIN users u ON u.id=k.user_id WHERE k.secret_hash=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp()) AND u.status='active'`
	}
	err := s.Pool.QueryRow(ctx, query, hash).Scan(&actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var active bool
	err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services WHERE user_id=$1 AND status='active' AND NOT import_review_required)`, actor).Scan(&active)
	if active {
		return 2, err
	}
	return 1, err
}
func (s *Store) customerAccountFileReadTx(ctx context.Context, actor int64, admin bool) (pgx.Tx, error) {
	tx, err := s.CustomerFileTx(ctx, actor, admin, false)
	if err != nil {
		return nil, err
	}
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok {
		tx.Rollback(ctx)
		return nil, ticketAccountError(401, "unauthorized", "登录已失效")
	}
	current, err := FileAccountIdentity(ctx, tx, credential.Hash, credential.APIKey, admin)
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	if current == 0 || current != actor {
		tx.Rollback(ctx)
		return nil, ticketAccountError(401, "unauthorized", "登录已失效")
	}
	if !admin {
		var enabled bool
		if err = tx.QueryRow(ctx, `SELECT enabled FROM download_settings WHERE id=1`).Scan(&enabled); err != nil {
			tx.Rollback(ctx)
			return nil, err
		}
		if !enabled {
			tx.Rollback(ctx)
			return nil, ticketAccountError(403, "downloads_disabled", "文件下载已关闭")
		}
	}
	return tx, nil
}

const customerAccountFileColumns = `f.id,f.customer_id,f.title,f.remarks,f.filename,f.mime_type,f.size_bytes,f.sha256,f.downloads,f.created_at,f.updated_at`
const adminCustomerAccountFileColumns = customerAccountFileColumns + `,f.version,f.uploader_id,u.display_name AS uploader_name,c.display_name AS customer_name,c.username AS customer_username`

func (s *Store) CustomerAccountFilePage(ctx context.Context, actor int64, admin bool, filter *int64, query string, limit, offset int) (map[string]any, error) {
	tx, err := s.customerAccountFileReadTx(ctx, actor, admin)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	columns, scope := customerAccountFileColumns, "f.customer_id=$1"
	if admin {
		columns = adminCustomerAccountFileColumns
		scope = "admin_customer_allowed($1,f.customer_id)"
		if filter != nil {
			if err = LockAdminCustomer(ctx, tx, actor, *filter); err != nil {
				return nil, err
			}
		}
	}
	if err = ticketFileCredentialCurrent(ctx, tx, actor); err != nil {
		return nil, err
	}
	where := ` FROM customer_files f JOIN users c ON c.id=f.customer_id JOIN account_identities u ON u.id=f.uploader_id WHERE ` + scope + ` AND ($2::bigint IS NULL OR f.customer_id=$2) AND ($3='' OR f.title ILIKE '%'||$3||'%' OR f.remarks ILIKE '%'||$3||'%' OR f.filename ILIKE '%'||$3||'%')`
	page, err := ticketAccountPage(ctx, tx, "SELECT "+columns+where, "id DESC", []any{actor, filter, query}, limit, offset)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return page, nil
}
func (s *Store) AdminCustomerAccountFileDetail(ctx context.Context, actor, id int64) (map[string]any, error) {
	tx, err := s.customerAccountFileReadTx(ctx, actor, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var owner int64
	if err = tx.QueryRow(ctx, `SELECT customer_id FROM customer_files WHERE id=$1`, id).Scan(&owner); err != nil {
		return nil, err
	}
	if err = LockAdminCustomer(ctx, tx, actor, owner); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, "SELECT "+adminCustomerAccountFileColumns+` FROM customer_files f JOIN users c ON c.id=f.customer_id JOIN account_identities u ON u.id=f.uploader_id WHERE f.id=$1`, id)
	if err != nil {
		return nil, err
	}
	item, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if err = ticketFileCredentialCurrent(ctx, tx, actor); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return item, nil
}
