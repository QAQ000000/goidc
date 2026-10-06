package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	var id int64
	if err = tx.QueryRow(ctx, `INSERT INTO content_images(article_id,uploader_id,filename,mime_type,size_bytes,content) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, articleID, actorID, image.Filename, image.MIME, len(image.Content), image.Content).Scan(&id); err != nil {
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
	err := s.Pool.QueryRow(ctx, "SELECT filename,mime_type,content,article_id FROM content_images WHERE id=$1", id).Scan(&image.Filename, &image.MIME, &image.Content, &articleID)
	return image, articleID, err
}
