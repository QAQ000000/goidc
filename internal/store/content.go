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
	CategoryIDs     []int64    `json:"category_ids"`
	TagIDs          []int64    `json:"tag_ids"`
	SortOrder       *int       `json:"sort_order"`
	Title           string     `json:"title"`
	Slug            string     `json:"slug"`
	Excerpt         string     `json:"excerpt"`
	Body            string     `json:"body"`
	BodyHTML        string     `json:"body_html"`
	SEO             ContentSEO `json:"seo"`
	SEOTitle        string     `json:"seo_title"`
	SEODescription  string     `json:"seo_description"`
	SEOCanonical    string     `json:"seo_canonical"`
	CanonicalURL    string     `json:"canonical_url"`
	Status          string     `json:"status"`
	AccessLevel     string     `json:"access_level"`
	PublishedAt     *time.Time `json:"published_at"`
	ExpectedVersion int64      `json:"expected_version"`
}

type ContentSEO struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Canonical   string `json:"canonical"`
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
	in.BodyHTML = strings.TrimSpace(in.BodyHTML)
	in.SEO.Title = strings.TrimSpace(in.SEO.Title)
	in.SEO.Description = strings.TrimSpace(in.SEO.Description)
	in.SEO.Canonical = strings.TrimSpace(in.SEO.Canonical)
	if in.SEO.Title == "" {
		in.SEO.Title = strings.TrimSpace(in.SEOTitle)
	}
	if in.SEO.Description == "" {
		in.SEO.Description = strings.TrimSpace(in.SEODescription)
	}
	if in.SEO.Canonical == "" {
		in.SEO.Canonical = strings.TrimSpace(in.SEOCanonical)
		if in.SEO.Canonical == "" {
			in.SEO.Canonical = strings.TrimSpace(in.CanonicalURL)
		}
	}
	cleanHTML, err := SanitizeContentHTML(in.BodyHTML)
	if err != nil {
		return conflict("invalid_content", "正文 HTML 无效")
	}
	in.BodyHTML = cleanHTML
	if in.Status == "" {
		in.Status = "draft"
	}
	if in.AccessLevel == "" {
		in.AccessLevel = "public"
	}
	if !contentTypeValid(in.ContentType) || in.Title == "" || !contentTextValid(in.Title, 240, false) || !contentSlug.MatchString(in.Slug) || !contentTextValid(in.Excerpt, 1000, true) || in.Body == "" || !contentTextValid(in.Body, 20000, true) || utf8.RuneCountInString(in.BodyHTML) > 20000 || (in.Status != "draft" && in.Status != "published" && in.Status != "archived") || (in.AccessLevel != "public" && in.AccessLevel != "login" && in.AccessLevel != "active_service") || (in.ContentType == "news" && in.AccessLevel != "public") || (in.CategoryID != nil && *in.CategoryID < 1) || (in.SortOrder != nil && (*in.SortOrder < 0 || *in.SortOrder > 1000000)) || len(in.CategoryIDs) > 30 || len(in.TagIDs) > 30 || !contentTextValid(in.SEO.Title, 240, false) || !contentTextValid(in.SEO.Description, 1000, true) || !validCanonical(in.SEO.Canonical) {
		return conflict("invalid_content", "文章类型、标题、标识、正文或可见范围无效")
	}
	if in.PublishedAt != nil && (in.PublishedAt.Year() < 2000 || in.PublishedAt.Year() > 2200) {
		return conflict("invalid_content", "发布时间无效")
	}
	if in.CategoryIDs == nil {
		in.CategoryIDs = []int64{}
		if in.CategoryID != nil {
			in.CategoryIDs = append(in.CategoryIDs, *in.CategoryID)
		}
	} else if len(in.CategoryIDs) > 0 && in.CategoryID == nil {
		in.CategoryID = &in.CategoryIDs[0]
	}
	if len(in.CategoryIDs) == 0 && in.CategoryID != nil {
		return conflict("invalid_category", "主分类必须属于文章分类")
	}
	if err := uniquePositiveContentIDs(in.CategoryIDs); err != nil {
		return err
	}
	if err := uniquePositiveContentIDs(in.TagIDs); err != nil {
		return err
	}
	if in.CategoryID != nil {
		found := false
		for _, id := range in.CategoryIDs {
			found = found || id == *in.CategoryID
		}
		if !found {
			return conflict("invalid_category", "主分类必须属于文章分类")
		}
	}
	return nil
}

func validCanonical(value string) bool {
	if value == "" {
		return true
	}
	return safeContentLink(value) && strings.HasPrefix(value, "/") && !strings.ContainsAny(value, "?#") && contentTextValid(value, 500, false)
}

func uniquePositiveContentIDs(ids []int64) error {
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id < 1 || seen[id] {
			return conflict("invalid_content", "分类或标签编号无效或重复")
		}
		seen[id] = true
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
	categoriesProvided, tagsProvided := in.CategoryIDs != nil, in.TagIDs != nil
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
	var oldCategoryID *int64
	var oldPublished *time.Time
	sortOrder := 0
	if in.SortOrder != nil {
		sortOrder = *in.SortOrder
	}
	if id != 0 {
		var oldSortOrder int
		if err = tx.QueryRow(ctx, "SELECT content_type,version,category_id,published_at,sort_order FROM content_articles WHERE id=$1 FOR UPDATE", id).Scan(&oldType, &oldVersion, &oldCategoryID, &oldPublished, &oldSortOrder); err != nil {
			return 0, err
		}
		if oldVersion != in.ExpectedVersion {
			return 0, conflict("version_conflict", "文章已变更，请重新加载")
		}
		if oldType != in.ContentType {
			return 0, conflict("content_type_immutable", "文章类型不能改变")
		}
		if in.SortOrder == nil {
			sortOrder = oldSortOrder
		}
		if !categoriesProvided && in.CategoryID != nil {
			var secondary []int64
			if err = tx.QueryRow(ctx, `SELECT coalesce(array_agg(category_id ORDER BY category_id),'{}'::bigint[]) FROM content_article_categories
 WHERE article_id=$1 AND ($2::bigint IS NULL OR category_id<>$2) AND category_id<>$3`, id, oldCategoryID, in.CategoryID).Scan(&secondary); err != nil {
				return 0, err
			}
			in.CategoryIDs = append(in.CategoryIDs, secondary...)
			if len(in.CategoryIDs) > 30 {
				return 0, conflict("invalid_category", "文章分类不能超过 30 个")
			}
		}
		if !tagsProvided {
			if err = tx.QueryRow(ctx, `SELECT coalesce(array_agg(tag_id ORDER BY tag_id),'{}'::bigint[]) FROM content_article_tags WHERE article_id=$1`, id).Scan(&in.TagIDs); err != nil {
				return 0, err
			}
		}
		action = "content.updated"
	}
	imageRefs := regexp.MustCompile(`src="/api/v1/content-images/([1-9][0-9]*)"`).FindAllStringSubmatch(in.BodyHTML, -1)
	for _, ref := range imageRefs {
		var belongs bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM content_images WHERE id=$1::bigint AND article_id=$2)", ref[1], id).Scan(&belongs); err != nil {
			return 0, err
		}
		if !belongs {
			return 0, conflict("invalid_content_image", "正文图片不属于当前文章，请先保存草稿并上传图片")
		}
	}
	for _, categoryID := range in.CategoryIDs {
		var categoryType string
		var active bool
		if err = tx.QueryRow(ctx, "SELECT content_type,active FROM content_categories WHERE id=$1 FOR SHARE", categoryID).Scan(&categoryType, &active); err != nil {
			return 0, err
		}
		if categoryType != in.ContentType {
			return 0, conflict("invalid_category", "文章与分类类型不一致")
		}
		if in.Status == "published" && !active {
			return 0, conflict("category_hidden", "不能发布到隐藏分类")
		}
	}
	for _, tagID := range in.TagIDs {
		var tagType string
		if err = tx.QueryRow(ctx, "SELECT content_type FROM content_tags WHERE id=$1 FOR SHARE", tagID).Scan(&tagType); err != nil {
			return 0, err
		}
		if tagType != in.ContentType {
			return 0, conflict("invalid_tag", "文章与标签类型不一致")
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
		err = tx.QueryRow(ctx, `INSERT INTO content_articles(content_type,category_id,title,slug,excerpt,body,body_html,seo_title,seo_description,seo_canonical,status,access_level,published_at,author_id,sort_order) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING id`, in.ContentType, in.CategoryID, in.Title, in.Slug, in.Excerpt, in.Body, in.BodyHTML, in.SEO.Title, in.SEO.Description, in.SEO.Canonical, in.Status, in.AccessLevel, published, actorID, sortOrder).Scan(&id)
	} else {
		_, err = tx.Exec(ctx, `UPDATE content_articles SET category_id=$2,title=$3,slug=$4,excerpt=$5,body=$6,body_html=$7,seo_title=$8,seo_description=$9,seo_canonical=$10,status=$11,access_level=$12,published_at=$13,sort_order=$14,version=version+1,updated_at=now() WHERE id=$1`, id, in.CategoryID, in.Title, in.Slug, in.Excerpt, in.Body, in.BodyHTML, in.SEO.Title, in.SEO.Description, in.SEO.Canonical, in.Status, in.AccessLevel, published, sortOrder)
	}
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM content_article_categories WHERE article_id=$1", id); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM content_article_tags WHERE article_id=$1", id); err != nil {
		return 0, err
	}
	for _, categoryID := range in.CategoryIDs {
		if _, err = tx.Exec(ctx, "INSERT INTO content_article_categories(article_id,category_id) VALUES($1,$2)", id, categoryID); err != nil {
			return 0, err
		}
	}
	for _, tagID := range in.TagIDs {
		if _, err = tx.Exec(ctx, "INSERT INTO content_article_tags(article_id,tag_id) VALUES($1,$2)", id, tagID); err != nil {
			return 0, err
		}
	}
	if err = Audit(ctx, tx, actorID, action, "content_article", id, map[string]any{"content_type": in.ContentType, "status": in.Status, "access_level": in.AccessLevel, "published_at": published, "category_ids": in.CategoryIDs, "tag_ids": in.TagIDs, "sort_order": sortOrder, "previous_version": oldVersion, "version": oldVersion + 1}); err != nil {
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
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM content_articles WHERE category_id=$1) OR EXISTS(SELECT 1 FROM content_article_categories WHERE category_id=$1)", id).Scan(&used); err != nil {
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
