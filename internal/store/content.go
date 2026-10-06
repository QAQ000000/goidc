package store

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type ContentInput struct {
	ContentType     string     `json:"content_type"`
	CategoryID      *int64     `json:"category_id"`
	Title           string     `json:"title"`
	Slug            string     `json:"slug"`
	Excerpt         string     `json:"excerpt"`
	Body            string     `json:"body"`
	Status          string     `json:"status"`
	AccessLevel     string     `json:"access_level"`
	PublishedAt     *time.Time `json:"published_at"`
	ExpectedVersion int64      `json:"expected_version"`
}

type ContentCategoryInput struct {
	ContentType     string `json:"content_type"`
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	Description     string `json:"description"`
	SortOrder       int    `json:"sort_order"`
	Active          bool   `json:"active"`
	ExpectedVersion int64  `json:"expected_version"`
}

func contentTypeValid(value string) bool { return value == "news" || value == "knowledge" }

var contentSlug = regexp.MustCompile(`^[a-z][a-z0-9-]{0,119}$`)

func contentTextValid(value string, limit int, multiline bool) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) && (!multiline || (r != '\n' && r != '\r' && r != '\t'))
	}) < 0
}
func validateContent(in *ContentInput) error {
	in.ContentType = strings.ToLower(strings.TrimSpace(in.ContentType))
	in.Title = strings.TrimSpace(in.Title)
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.Excerpt = strings.TrimSpace(in.Excerpt)
	in.Body = strings.TrimSpace(in.Body)
	if in.Status == "" {
		in.Status = "draft"
	}
	if in.AccessLevel == "" {
		in.AccessLevel = "public"
	}
	if !contentTypeValid(in.ContentType) || in.Title == "" || !contentTextValid(in.Title, 240, false) || !contentSlug.MatchString(in.Slug) || !contentTextValid(in.Excerpt, 1000, true) || in.Body == "" || !contentTextValid(in.Body, 20000, true) || (in.Status != "draft" && in.Status != "published" && in.Status != "archived") || (in.AccessLevel != "public" && in.AccessLevel != "login" && in.AccessLevel != "active_service") || (in.ContentType == "news" && in.AccessLevel != "public") || (in.CategoryID != nil && *in.CategoryID < 1) {
		return conflict("invalid_content", "文章类型、标题、标识、正文或可见范围无效")
	}
	if in.PublishedAt != nil && (in.PublishedAt.Year() < 2000 || in.PublishedAt.Year() > 2200) {
		return conflict("invalid_content", "发布时间无效")
	}
	return nil
}

// Serialize category assignment/removal and recheck editorial permission after waiting.
func (s *Store) contentTx(ctx context.Context, actorID int64) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err == nil {
		_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108196)")
	}
	if err == nil {
		var allowed bool
		err = tx.QueryRow(ctx, `SELECT coalesce(u.role='super_admin' OR (u.role='admin' AND r.active AND 'content.manage'=ANY(r.permissions) AND 'content.view'=ANY(r.permissions)),false) FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1 AND u.status='active'`, actorID).Scan(&allowed)
		if err == nil && !allowed {
			err = conflict("permission_denied", "没有管理内容的权限")
		}
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func (s *Store) SaveContent(ctx context.Context, actorID, id int64, in ContentInput) (int64, error) {
	if err := validateContent(&in); err != nil {
		return 0, err
	}
	tx, err := s.contentTx(ctx, actorID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	action := "content.created"
	var oldType string
	var oldVersion int64
	var oldPublished *time.Time
	if id != 0 {
		if err = tx.QueryRow(ctx, "SELECT content_type,version,published_at FROM content_articles WHERE id=$1 FOR UPDATE", id).Scan(&oldType, &oldVersion, &oldPublished); err != nil {
			return 0, err
		}
		if oldVersion != in.ExpectedVersion {
			return 0, conflict("version_conflict", "文章已变更，请重新加载")
		}
		if oldType != in.ContentType {
			return 0, conflict("content_type_immutable", "文章类型不能改变")
		}
		action = "content.updated"
	}
	if in.CategoryID != nil {
		var categoryType string
		var active bool
		if err = tx.QueryRow(ctx, "SELECT content_type,active FROM content_categories WHERE id=$1 FOR SHARE", *in.CategoryID).Scan(&categoryType, &active); err != nil {
			return 0, err
		}
		if categoryType != in.ContentType {
			return 0, conflict("invalid_category", "文章与分类类型不一致")
		}
		if in.Status == "published" && !active {
			return 0, conflict("category_hidden", "不能发布到隐藏分类")
		}
	}
	published := in.PublishedAt
	if in.Status == "published" && published == nil {
		published = oldPublished
		if published == nil {
			now := time.Now()
			published = &now
		}
	}
	if in.Status != "published" {
		published = nil
	}
	if id == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO content_articles(content_type,category_id,title,slug,excerpt,body,status,access_level,published_at,author_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`, in.ContentType, in.CategoryID, in.Title, in.Slug, in.Excerpt, in.Body, in.Status, in.AccessLevel, published, actorID).Scan(&id)
	} else {
		_, err = tx.Exec(ctx, `UPDATE content_articles SET category_id=$2,title=$3,slug=$4,excerpt=$5,body=$6,status=$7,access_level=$8,published_at=$9,version=version+1,updated_at=now() WHERE id=$1`, id, in.CategoryID, in.Title, in.Slug, in.Excerpt, in.Body, in.Status, in.AccessLevel, published)
	}
	if err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, actorID, action, "content_article", id, map[string]any{"content_type": in.ContentType, "status": in.Status, "access_level": in.AccessLevel, "published_at": published, "previous_version": oldVersion, "version": oldVersion + 1}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) DeleteContent(ctx context.Context, actorID, id, version int64) error {
	tx, err := s.contentTx(ctx, actorID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current int64
	var title, kind string
	if err = tx.QueryRow(ctx, "SELECT version,title,content_type FROM content_articles WHERE id=$1 FOR UPDATE", id).Scan(&current, &title, &kind); err != nil {
		return err
	}
	if current != version {
		return conflict("version_conflict", "文章已变更，请重新加载")
	}
	if _, err = tx.Exec(ctx, "DELETE FROM content_articles WHERE id=$1", id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actorID, "content.deleted", "content_article", id, map[string]any{"title": title, "content_type": kind, "version": version}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) SaveContentCategory(ctx context.Context, actorID, id int64, in ContentCategoryInput) (int64, error) {
	in.ContentType = strings.ToLower(strings.TrimSpace(in.ContentType))
	in.Name = strings.TrimSpace(in.Name)
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.Description = strings.TrimSpace(in.Description)
	if !contentTypeValid(in.ContentType) || in.Name == "" || !contentTextValid(in.Name, 120, false) || !contentSlug.MatchString(in.Slug) || !contentTextValid(in.Description, 500, true) || in.SortOrder < 0 || in.SortOrder > 1000000 {
		return 0, conflict("invalid_category", "分类字段无效")
	}
	tx, err := s.contentTx(ctx, actorID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	action := "content_category.created"
	var current int64
	var oldType string
	if id == 0 {
		err = tx.QueryRow(ctx, "INSERT INTO content_categories(content_type,name,slug,description,sort_order,active) VALUES($1,$2,$3,$4,$5,$6) RETURNING id", in.ContentType, in.Name, in.Slug, in.Description, in.SortOrder, in.Active).Scan(&id)
	} else {
		if err = tx.QueryRow(ctx, "SELECT content_type,version FROM content_categories WHERE id=$1 FOR UPDATE", id).Scan(&oldType, &current); err != nil {
			return 0, err
		}
		if current != in.ExpectedVersion {
			return 0, conflict("version_conflict", "分类已变更，请重新加载")
		}
		if oldType != in.ContentType {
			return 0, conflict("content_type_immutable", "分类类型不能改变")
		}
		action = "content_category.updated"
		_, err = tx.Exec(ctx, "UPDATE content_categories SET name=$2,slug=$3,description=$4,sort_order=$5,active=$6,version=version+1,updated_at=now() WHERE id=$1", id, in.Name, in.Slug, in.Description, in.SortOrder, in.Active)
	}
	if err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, actorID, action, "content_category", id, map[string]any{"name": in.Name, "content_type": in.ContentType, "active": in.Active, "version": current + 1}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) DeleteContentCategory(ctx context.Context, actorID, id, version int64) error {
	tx, err := s.contentTx(ctx, actorID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current int64
	var name string
	if err = tx.QueryRow(ctx, "SELECT version,name FROM content_categories WHERE id=$1 FOR UPDATE", id).Scan(&current, &name); err != nil {
		return err
	}
	if current != version {
		return conflict("version_conflict", "分类已变更，请重新加载")
	}
	var used bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM content_articles WHERE category_id=$1)", id).Scan(&used); err != nil {
		return err
	}
	if used {
		return conflict("category_in_use", "分类仍有关联文章，无法删除")
	}
	if _, err = tx.Exec(ctx, "DELETE FROM content_categories WHERE id=$1", id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actorID, "content_category.deleted", "content_category", id, map[string]any{"name": name, "version": version}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
