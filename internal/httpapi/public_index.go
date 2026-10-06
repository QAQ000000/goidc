package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Crawlers and social previews receive only anonymous-public resources. This
// deliberately ignores session cookies and does not increment article views.
func (s *Server) publicContentPreview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	if kind != "news" && kind != "knowledge" {
		writeError(w, 400, "invalid_content_type", "内容类型无效")
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), "SELECT "+articleColumns+` FROM content_articles a LEFT JOIN content_categories c ON c.id=a.category_id WHERE a.content_type=$1 AND `+publicArticleWhere+" AND a.id=$3", kind, 0, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	item, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, item)
}

func (s *Server) publicIndex(w http.ResponseWriter, r *http.Request) {
	limit, offset := 1000, 0
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
	}
	if err != nil || limit < 1 || limit > 1000 {
		writeError(w, 400, "invalid_pagination", "分页参数无效")
		return
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
	}
	if err != nil || offset < 0 {
		writeError(w, 400, "invalid_pagination", "分页参数无效")
		return
	}
	const prefix = `WITH public_urls AS (
 SELECT '/products/'||id::text AS path,updated_at FROM products WHERE active AND catalog_category_visible(category_id)
 UNION ALL
 SELECT (CASE WHEN a.content_type='news' THEN '/news/' ELSE '/knowledgebase/' END)||a.id::text AS path,a.updated_at
 FROM content_articles a LEFT JOIN content_categories c ON c.id=a.category_id
 WHERE ($1='' OR a.content_type=$1) AND ` + publicArticleWhere + `) `
	// One statement keeps page count and rows on the same database snapshot.
	var payload []byte
	err = s.db.Pool.QueryRow(r.Context(), prefix+`SELECT jsonb_build_object('total',(SELECT count(*) FROM public_urls),'items',
 coalesce((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.path) FROM (SELECT path,updated_at FROM public_urls ORDER BY path LIMIT $3 OFFSET $4) p),'[]'::jsonb),'limit',$3,'offset',$4)`, "", 0, limit, offset).Scan(&payload)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, json.RawMessage(payload))
}
