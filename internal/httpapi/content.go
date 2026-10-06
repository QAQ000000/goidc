package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

const articleColumns = `a.id,a.content_type,a.category_id,c.name AS category_name,a.title,a.slug,a.excerpt,a.body,a.published_at,a.views,a.created_at,a.updated_at`
const adminArticleColumns = articleColumns + `,a.status,a.access_level,a.version,a.author_id,(a.status='published' AND a.published_at>now()) AS scheduled`

func contentQueryType(w http.ResponseWriter, r *http.Request, required bool) (string, bool) {
	kind := r.URL.Query().Get("content_type")
	if (kind != "" || required) && kind != "news" && kind != "knowledge" {
		writeError(w, 400, "invalid_content_type", "请选择新闻或知识库")
		return "", false
	}
	return kind, true
}
func categoryFilter(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	raw := r.URL.Query().Get("category_id")
	if raw == "" {
		return nil, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		writeError(w, 400, "invalid_category", "分类筛选无效")
		return nil, false
	}
	return &id, true
}

// Invalid credentials on public content endpoints remain anonymous.
func (s *Server) contentAccess(r *http.Request) (int, error) {
	var id int64
	var err error
	if bearer := bearerCredential(r); bearer != "" {
		err = s.db.Pool.QueryRow(r.Context(), `SELECT u.id FROM api_keys k JOIN users u ON u.id=k.user_id WHERE k.secret_hash=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now()) AND u.status='active'`, tokenHash(bearer)).Scan(&id)
	} else if cookie, e := r.Cookie("zjmfgo_session"); e == nil {
		err = s.db.Pool.QueryRow(r.Context(), `SELECT u.id FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now() AND u.status='active'`, tokenHash(cookie.Value)).Scan(&id)
	} else {
		return 0, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var active bool
	err = s.db.Pool.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM services WHERE user_id=$1 AND status='active' AND NOT import_review_required)", id).Scan(&active)
	if active {
		return 2, err
	}
	return 1, err
}

const publicArticleWhere = `a.status='published' AND a.published_at IS NOT NULL AND a.published_at<=now()
 AND (a.category_id IS NULL OR c.active) AND (a.access_level='public' OR (a.access_level='login' AND $2>=1) OR (a.access_level='active_service' AND $2>=2))`

func (s *Server) contentList(w http.ResponseWriter, r *http.Request, kind string) {
	level, err := s.contentAccess(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	category, ok := categoryFilter(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	where := ` FROM content_articles a LEFT JOIN content_categories c ON c.id=a.category_id WHERE a.content_type=$1 AND ` + publicArticleWhere + ` AND ($3='' OR a.title ILIKE '%'||$3||'%' OR a.excerpt ILIKE '%'||$3||'%' OR a.body ILIKE '%'||$3||'%') AND ($4::bigint IS NULL OR coalesce(a.category_id,0)=$4)`
	var total int64
	if err = s.db.Pool.QueryRow(r.Context(), "SELECT count(*)"+where, kind, level, q, category).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), "SELECT "+articleColumns+where+" ORDER BY a.published_at DESC,a.id DESC LIMIT $5 OFFSET $6", kind, level, q, category, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	write(w, 200, map[string]any{"items": items, "content_type": kind, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) news(w http.ResponseWriter, r *http.Request)      { s.contentList(w, r, "news") }
func (s *Server) knowledge(w http.ResponseWriter, r *http.Request) { s.contentList(w, r, "knowledge") }
func (s *Server) contentDetail(w http.ResponseWriter, r *http.Request, kind string) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	level, err := s.contentAccess(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	rows, err := tx.Query(r.Context(), "SELECT "+articleColumns+` FROM content_articles a LEFT JOIN content_categories c ON c.id=a.category_id WHERE a.id=$3 AND a.content_type=$1 AND `+publicArticleWhere+" FOR UPDATE OF a", kind, level, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	article, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE content_articles SET views=views+1 WHERE id=$1", id); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	article["views"] = article["views"].(int64) + 1
	write(w, 200, article)
}
func (s *Server) newsDetail(w http.ResponseWriter, r *http.Request) { s.contentDetail(w, r, "news") }
func (s *Server) knowledgeDetail(w http.ResponseWriter, r *http.Request) {
	s.contentDetail(w, r, "knowledge")
}
func (s *Server) publicContentCategories(w http.ResponseWriter, r *http.Request) {
	kind, ok := contentQueryType(w, r, true)
	if !ok {
		return
	}
	s.list(w, r, "SELECT id,content_type,name,slug,description,sort_order FROM content_categories WHERE content_type=$1 AND active ORDER BY sort_order,id", kind)
}
func (s *Server) adminContentList(w http.ResponseWriter, r *http.Request) {
	kind, ok := contentQueryType(w, r, false)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "draft" && status != "published" && status != "archived" {
		writeError(w, 400, "invalid_status", "文章状态无效")
		return
	}
	category, ok := categoryFilter(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	where := ` FROM content_articles a LEFT JOIN content_categories c ON c.id=a.category_id WHERE ($1='' OR a.content_type=$1) AND ($2='' OR a.status=$2) AND ($3='' OR a.title ILIKE '%'||$3||'%' OR a.slug ILIKE '%'||$3||'%') AND ($4::bigint IS NULL OR coalesce(a.category_id,0)=$4)`
	var total int64
	err := s.db.Pool.QueryRow(r.Context(), "SELECT count(*)"+where, kind, status, q, category).Scan(&total)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `SELECT a.id,a.content_type,a.category_id,c.name AS category_name,a.title,a.slug,a.excerpt,a.published_at,a.views,a.status,a.access_level,a.version,a.author_id,a.created_at,a.updated_at,(a.status='published' AND a.published_at>now()) AS scheduled`+where+" ORDER BY a.updated_at DESC,a.id DESC LIMIT $5 OFFSET $6", kind, status, q, category, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	write(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) adminContentDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), "SELECT "+adminArticleColumns+" FROM content_articles a LEFT JOIN content_categories c ON c.id=a.category_id WHERE a.id=$1", id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	article, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, article)
}
func (s *Server) adminCreateContent(w http.ResponseWriter, r *http.Request) {
	var id int64
	var ok bool
	if r.Method == "PUT" {
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var input store.ContentInput
	if !decodeLimit(w, r, &input, 256<<10) {
		return
	}
	saved, err := s.db.SaveContent(r.Context(), identityFrom(r).ID, id, input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if id == 0 {
		status = 201
	}
	state := input.Status
	if state == "" {
		state = "draft"
	}
	write(w, status, map[string]any{"id": saved, "status": state})
}
func (s *Server) adminDeleteContent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := s.db.DeleteContent(r.Context(), identityFrom(r).ID, id, input.ExpectedVersion); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"deleted": true})
}
func (s *Server) adminContentCategories(w http.ResponseWriter, r *http.Request) {
	kind, ok := contentQueryType(w, r, false)
	if !ok {
		return
	}
	s.list(w, r, `SELECT c.*,(SELECT count(*) FROM content_articles a WHERE a.category_id=c.id) AS article_count FROM content_categories c WHERE ($1='' OR c.content_type=$1) ORDER BY c.sort_order,c.id`, kind)
}
func (s *Server) adminSaveContentCategory(w http.ResponseWriter, r *http.Request) {
	var id int64
	var ok bool
	if r.Method == "PUT" {
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var input store.ContentCategoryInput
	if !decode(w, r, &input) {
		return
	}
	saved, err := s.db.SaveContentCategory(r.Context(), identityFrom(r).ID, id, input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if id == 0 {
		status = 201
	}
	write(w, status, map[string]any{"id": saved})
}
func (s *Server) adminDeleteContentCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := s.db.DeleteContentCategory(r.Context(), identityFrom(r).ID, id, input.ExpectedVersion); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"deleted": true})
}
