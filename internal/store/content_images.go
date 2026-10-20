package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/jackc/pgx/v5"
	"strings"
)

const MaxContentImageBytes = 5 << 20

type ContentImage struct {
	Filename, MIME string
	Content        []byte
}

func (s *Store) SaveContentImage(ctx context.Context, actorID, articleID int64, image ContentImage) (int64, error) {
	if articleID < 1 || len(image.Content) == 0 || len(image.Content) > MaxContentImageBytes || !strings.HasPrefix(image.MIME, "image/") {
		return 0, conflict("invalid_image", "图片格式或大小无效")
	}
	tx, err := s.contentTx(ctx, actorID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = lockStorageCredential(ctx, tx, actorID); err != nil {
		return 0, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM content_articles WHERE id=$1)", articleID).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, pgx.ErrNoRows
	}
	stored, err := s.PersistFile(ctx, tx, "content_images", image.Content)
	if err != nil {
		return 0, err
	}
	if err = LockFileCredential(ctx, tx, actorID); err != nil {
		return 0, err
	}
	var id int64
	if err = tx.QueryRow(ctx, `INSERT INTO content_images(article_id,uploader_id,filename,mime_type,size_bytes,content,storage_object_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, articleID, actorID, image.Filename, image.MIME, len(image.Content), stored.Content, stored.ObjectID).Scan(&id); err != nil {
		return 0, err
	}
	digest := sha256.Sum256(image.Content)
	if err = Audit(ctx, tx, actorID, "content.image.uploaded", "content_image", id, map[string]any{"article_id": articleID, "mime_type": image.MIME, "size_bytes": len(image.Content), "sha256": hex.EncodeToString(digest[:])}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) ContentImage(ctx context.Context, id int64) (ContentImage, int64, error) {
	var image ContentImage
	var articleID int64
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return image, 0, err
	}
	defer tx.Rollback(ctx)
	var objectID *string
	if err = tx.QueryRow(ctx, "SELECT filename,mime_type,content,article_id,storage_object_id FROM content_images WHERE id=$1 FOR SHARE", id).Scan(&image.Filename, &image.MIME, &image.Content, &articleID, &objectID); err != nil {
		return image, articleID, err
	}
	image.Content, err = s.ReadFileContent(ctx, tx, "content_images", image.Content, objectID)
	if err != nil {
		return image, articleID, err
	}
	return image, articleID, tx.Commit(ctx)
}

// VisibleContentImage keeps visibility, current credential checks, private object
// reads and the final recheck in one transaction. Administrator preview is only
// requested by the fixed, separately authenticated management route.
func (s *Store) VisibleContentImage(ctx context.Context, id int64, hash, credential string, adminPreview bool) (ContentImage, error) {
	var image ContentImage
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return image, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return image, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108196)"); err != nil {
		return image, err
	}
	const query = `WITH viewer AS (
 SELECT u.id,u.role,NULL::boolean AS role_active,NULL::text[] AS permissions,
 CASE WHEN EXISTS(SELECT 1 FROM services WHERE user_id=u.id AND status='active' AND NOT import_review_required) THEN 2 ELSE 1 END AS access
 FROM users u WHERE NOT $4 AND u.status='active' AND (
 ($3='session' AND EXISTS(SELECT 1 FROM sessions s WHERE s.user_id=u.id AND s.token_hash=$2 AND s.expires_at>clock_timestamp())) OR
 ($3='key' AND EXISTS(SELECT 1 FROM api_keys k WHERE k.user_id=u.id AND k.secret_hash=$2 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp()))))
 UNION ALL
 SELECT u.id,u.role,r.active AS role_active,r.permissions,1 AS access
 FROM administrators u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE $4 AND u.status='active' AND (
 ($3='session' AND EXISTS(SELECT 1 FROM sessions s WHERE s.user_id=u.id AND s.token_hash=$2 AND s.expires_at>clock_timestamp())) OR
 ($3='key' AND EXISTS(SELECT 1 FROM api_keys k WHERE k.user_id=u.id AND k.secret_hash=$2 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp()))))
 ) SELECT i.filename,i.mime_type,i.content,i.storage_object_id FROM content_images i JOIN content_articles a ON a.id=i.article_id
 LEFT JOIN content_categories c ON c.id=a.category_id LEFT JOIN viewer v ON true WHERE i.id=$1 AND (
 ($4 AND (v.role='super_admin' OR (v.role='admin' AND v.role_active AND 'content.view'=ANY(v.permissions)))) OR
 (NOT $4 AND a.status='published' AND a.published_at IS NOT NULL AND a.published_at<=now()
 AND (a.category_id IS NULL OR c.active)
 AND NOT EXISTS(SELECT 1 FROM content_article_categories hidden_ac JOIN content_categories hidden_c ON hidden_c.id=hidden_ac.category_id WHERE hidden_ac.article_id=a.id AND NOT hidden_c.active)
 AND (a.access_level='public' OR (a.access_level='login' AND coalesce(v.access,0)>=1) OR (a.access_level='active_service' AND coalesce(v.access,0)>=2)))) FOR SHARE OF i,a`
	var objectID *string
	if err = tx.QueryRow(ctx, query, id, hash, credential, adminPreview).Scan(&image.Filename, &image.MIME, &image.Content, &objectID); err != nil {
		return image, err
	}
	image.Content, err = s.ReadFileContent(ctx, tx, "content_images", image.Content, objectID)
	if err != nil {
		return image, err
	}
	var currentInline []byte
	var currentObjectID *string
	if err = tx.QueryRow(ctx, query, id, hash, credential, adminPreview).Scan(&image.Filename, &image.MIME, &currentInline, &currentObjectID); err != nil {
		return ContentImage{}, err
	}
	if (objectID == nil) != (currentObjectID == nil) || (objectID != nil && *objectID != *currentObjectID) {
		return ContentImage{}, pgx.ErrNoRows
	}
	return image, tx.Commit(ctx)
}
