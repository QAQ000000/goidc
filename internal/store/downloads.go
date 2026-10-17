package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

type DownloadInput struct {
	CategoryID      *int64  `json:"category_id"`
	Title           string  `json:"title"`
	Description     string  `json:"description"`
	Active          bool    `json:"active"`
	AccessLevel     string  `json:"access_level"`
	SourceType      string  `json:"source_type"`
	RemoteURL       string  `json:"remote_url"`
	ProductIDs      []int64 `json:"product_ids"`
	ExpectedVersion int64   `json:"expected_version"`
}
type DownloadFile struct {
	Filename, MIME string
	Content        []byte
}
type DownloadCategoryInput struct {
	ParentID        *int64 `json:"parent_id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	SortOrder       int    `json:"sort_order"`
	Active          bool   `json:"active"`
	ExpectedVersion int64  `json:"expected_version"`
}

// A dedicated lock serializes category visibility, file replacement and authorization.
// The shared RBAC lock orders permission checks after concurrent role changes.
func (s *Store) DownloadTx(ctx context.Context, actorID int64, manage bool) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err == nil {
		_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108198)")
	}
	if err == nil && manage {
		var allowed bool
		err = tx.QueryRow(ctx, `SELECT coalesce(u.role='super_admin' OR (u.role='admin' AND r.active AND 'downloads.view'=ANY(r.permissions) AND 'downloads.manage'=ANY(r.permissions)),false)
   FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1 AND u.status='active'`, actorID).Scan(&allowed)
		if err == nil && !allowed {
			err = conflict("permission_denied", "没有管理下载中心的权限")
		}
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func validateDownload(in *DownloadInput) error {
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	in.RemoteURL = strings.TrimSpace(in.RemoteURL)
	if in.Title == "" || !contentTextValid(in.Title, 255, false) || !contentTextValid(in.Description, 10000, true) || (in.CategoryID != nil && *in.CategoryID < 1) || (in.AccessLevel != "public" && in.AccessLevel != "login" && in.AccessLevel != "product") || (in.SourceType != "local" && in.SourceType != "remote") || len(in.ProductIDs) > 200 {
		return conflict("invalid_download", "文件名称、分类、访问权限或来源无效")
	}
	slices.Sort(in.ProductIDs)
	in.ProductIDs = slices.Compact(in.ProductIDs)
	for _, id := range in.ProductIDs {
		if id < 1 {
			return conflict("invalid_download", "关联产品无效")
		}
	}
	if in.AccessLevel == "product" && len(in.ProductIDs) == 0 {
		return conflict("invalid_download", "请选择至少一个允许下载的产品")
	}
	if in.SourceType == "remote" {
		u, err := url.Parse(in.RemoteURL)
		if err != nil || !contentTextValid(in.RemoteURL, 2048, false) || strings.ContainsAny(in.RemoteURL, "\\ \t\n\r") || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
			return conflict("invalid_download_url", "外部链接必须是完整的 HTTP 或 HTTPS 地址")
		}
	} else if in.RemoteURL != "" {
		return conflict("invalid_download", "本地文件不能填写外部链接")
	}
	return nil
}
func (s *Store) SaveDownload(ctx context.Context, actorID, id int64, in DownloadInput, file *DownloadFile) (int64, error) {
	if err := validateDownload(&in); err != nil {
		return 0, err
	}
	if file != nil && (in.SourceType != "local" || len(file.Content) == 0 || len(file.Content) > 50<<20 || !contentTextValid(file.Filename, 255, false) || strings.ContainsAny(file.Filename, "/\\") || file.MIME == "") {
		return 0, conflict("invalid_download_file", "上传文件无效")
	}
	tx, err := s.DownloadTx(ctx, actorID, true)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var current int64
	var oldSource string
	if id != 0 {
		if err = tx.QueryRow(ctx, "SELECT version,source_type FROM downloads WHERE id=$1 FOR UPDATE", id).Scan(&current, &oldSource); err != nil {
			return 0, err
		}
		if current != in.ExpectedVersion {
			return 0, conflict("version_conflict", "文件已变更，请重新加载")
		}
	}
	if in.SourceType == "local" && file == nil && (id == 0 || oldSource != "local") {
		return 0, conflict("download_file_required", "请选择上传文件")
	}
	if in.CategoryID != nil {
		var unused int64
		if err = tx.QueryRow(ctx, "SELECT id FROM download_categories WHERE id=$1 FOR SHARE", *in.CategoryID).Scan(&unused); err != nil {
			return 0, err
		}
	}
	for _, p := range in.ProductIDs {
		var unused int64
		if err = tx.QueryRow(ctx, "SELECT id FROM products WHERE id=$1 FOR SHARE", p).Scan(&unused); err != nil {
			return 0, err
		}
	}
	var hash, name, mime string
	var payload []byte
	if file != nil {
		digest := sha256.Sum256(file.Content)
		hash = hex.EncodeToString(digest[:])
		name = file.Filename
		mime = file.MIME
		payload = file.Content
	}
	if id == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO downloads(category_id,title,description,active,access_level,source_type,remote_url,filename,mime_type,size_bytes,sha256,content,author_id)
    VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id`, in.CategoryID, in.Title, in.Description, in.Active, in.AccessLevel, in.SourceType, in.RemoteURL, name, mime, len(payload), hash, payload, actorID).Scan(&id)
	} else {
		if in.SourceType == "local" && file == nil {
			_, err = tx.Exec(ctx, `UPDATE downloads SET category_id=$2,title=$3,description=$4,active=$5,access_level=$6,version=version+1,updated_at=now() WHERE id=$1`, id, in.CategoryID, in.Title, in.Description, in.Active, in.AccessLevel)
		} else {
			_, err = tx.Exec(ctx, `UPDATE downloads SET category_id=$2,title=$3,description=$4,active=$5,access_level=$6,source_type=$7,remote_url=$8,filename=$9,mime_type=$10,size_bytes=$11,sha256=$12,content=$13,version=version+1,updated_at=now() WHERE id=$1`, id, in.CategoryID, in.Title, in.Description, in.Active, in.AccessLevel, in.SourceType, in.RemoteURL, name, mime, len(payload), hash, payload)
		}
	}
	if err != nil {
		return 0, err
	}
	// Preserve the identity of unchanged bindings, including imported mappings.
	if _, err = tx.Exec(ctx, "DELETE FROM download_products WHERE download_id=$1 AND NOT(product_id=ANY(coalesce($2::bigint[],'{}'::bigint[])))", id, in.ProductIDs); err != nil {
		return 0, err
	}
	for _, p := range in.ProductIDs {
		if _, err = tx.Exec(ctx, "INSERT INTO download_products(download_id,product_id) VALUES($1,$2) ON CONFLICT(download_id,product_id) DO NOTHING", id, p); err != nil {
			return 0, err
		}
	}
	action := "download.created"
	if current > 0 {
		action = "download.updated"
	}
	if err = Audit(ctx, tx, actorID, action, "download", id, map[string]any{"title": in.Title, "active": in.Active, "access_level": in.AccessLevel, "product_ids": in.ProductIDs, "source_type": in.SourceType, "file_replaced": file != nil, "sha256": hash, "version": current + 1}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}
func (s *Store) DeleteDownload(ctx context.Context, actorID, id, version int64) error {
	tx, err := s.DownloadTx(ctx, actorID, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current int64
	var title string
	if err = tx.QueryRow(ctx, "SELECT version,title FROM downloads WHERE id=$1 FOR UPDATE", id).Scan(&current, &title); err != nil {
		return err
	}
	if current != version {
		return conflict("version_conflict", "文件已变更，请重新加载")
	}
	if _, err = tx.Exec(ctx, "DELETE FROM downloads WHERE id=$1", id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actorID, "download.deleted", "download", id, map[string]any{"title": title, "version": version}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) SaveDownloadCategory(ctx context.Context, actorID, id int64, in DownloadCategoryInput) (int64, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Name == "" || !contentTextValid(in.Name, 255, false) || !contentTextValid(in.Description, 1000, true) || in.SortOrder < 0 || in.SortOrder > 1000000 || (in.ParentID != nil && *in.ParentID < 1) {
		return 0, conflict("invalid_category", "分类字段无效")
	}
	tx, err := s.DownloadTx(ctx, actorID, true)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if in.ParentID != nil {
		var parent, cycle bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM download_categories WHERE id=$1),$1=$2 OR download_category_contains($2,$1)", *in.ParentID, id).Scan(&parent, &cycle); err != nil {
			return 0, err
		}
		if !parent {
			return 0, pgx.ErrNoRows
		}
		if cycle {
			return 0, conflict("category_cycle", "上级分类不能是自身或自己的子分类")
		}
	}
	var current int64
	if id == 0 {
		err = tx.QueryRow(ctx, "INSERT INTO download_categories(name,description,sort_order,active,parent_id) VALUES($1,$2,$3,$4,$5) RETURNING id", in.Name, in.Description, in.SortOrder, in.Active, in.ParentID).Scan(&id)
	} else {
		if current, err = LockVersion(ctx, tx, "download_categories", id, in.ExpectedVersion, "分类已变更，请重新加载"); err != nil {
			return 0, err
		}
		_, err = tx.Exec(ctx, "UPDATE download_categories SET name=$2,description=$3,sort_order=$4,active=$5,parent_id=$6,version=version+1,updated_at=now() WHERE id=$1", id, in.Name, in.Description, in.SortOrder, in.Active, in.ParentID)
	}
	if err != nil {
		return 0, err
	}
	action := "download_category.created"
	if current > 0 {
		action = "download_category.updated"
	}
	if err = Audit(ctx, tx, actorID, action, "download_category", id, map[string]any{"name": in.Name, "parent_id": in.ParentID, "active": in.Active, "version": current + 1}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}
func (s *Store) DeleteDownloadCategory(ctx context.Context, actorID, id, version int64) error {
	tx, err := s.DownloadTx(ctx, actorID, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current int64
	var used bool
	if err = tx.QueryRow(ctx, "SELECT version FROM download_categories WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
		return err
	}
	if version != current {
		return conflict("version_conflict", "分类已变更，请重新加载")
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM downloads WHERE category_id=$1) OR EXISTS(SELECT 1 FROM download_categories WHERE parent_id=$1)", id).Scan(&used); err != nil {
		return err
	}
	if used {
		return conflict("category_in_use", "分类仍有文件或子分类，请先移动或删除")
	}
	if _, err = tx.Exec(ctx, "DELETE FROM download_categories WHERE id=$1", id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actorID, "download_category.deleted", "download_category", id, map[string]any{"version": version}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
