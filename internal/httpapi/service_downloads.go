package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The service contextual route never lends another customer's download rights.
// Lock the owned service and recheck credentials after any row-lock wait.
func (s *Server) ownedDownloadService(w http.ResponseWriter, r *http.Request, tx pgx.Tx, id, uid int64) (int64, bool) {
	if uid == 0 || uid != identityFrom(r).ID {
		writeError(w, 401, "unauthorized", "登录已失效")
		return 0, false
	}
	var productID int64
	if err := tx.QueryRow(r.Context(), "SELECT product_id FROM services WHERE id=$1 AND user_id=$2 FOR SHARE", id, uid).Scan(&productID); err != nil {
		s.fail(w, r, err)
		return 0, false
	}
	fresh, err := s.publicDownloadIdentity(r, tx)
	if err != nil {
		s.fail(w, r, err)
		return 0, false
	}
	if fresh != uid {
		writeError(w, 401, "unauthorized", "登录已失效")
		return 0, false
	}
	return productID, true
}

func (s *Server) serviceDownloads(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, err := s.db.DownloadTx(r.Context(), 0, false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	uid, err := s.publicDownloadIdentity(r, tx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	productID, ok := s.ownedDownloadService(w, r, tx, id, uid)
	if !ok {
		return
	}
	var enabled bool
	if err = tx.QueryRow(r.Context(), "SELECT enabled FROM download_settings WHERE id=1").Scan(&enabled); err != nil {
		s.fail(w, r, err)
		return
	}
	limit, offset := pagination(r)
	items := []map[string]any{}
	var total int64
	if enabled {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		where := ` FROM downloads d LEFT JOIN download_categories c ON c.id=d.category_id
 WHERE ` + downloadVisible + ` AND EXISTS(SELECT 1 FROM download_products p WHERE p.download_id=d.id AND p.product_id=$1)
 AND ($2='' OR d.title ILIKE '%'||$2||'%' OR d.description ILIKE '%'||$2||'%')`
		if err = tx.QueryRow(r.Context(), "SELECT count(*)"+where, productID, query).Scan(&total); err != nil {
			s.fail(w, r, err)
			return
		}
		rows, err := tx.Query(r.Context(), "SELECT "+downloadColumns+",download_allowed(d.id,$5) AS can_download"+where+" ORDER BY d.id DESC LIMIT $3 OFFSET $4", productID, query, limit, offset, uid)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		items, err = pgx.CollectRows(rows, pgx.RowToMap)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if items == nil {
			items = []map[string]any{}
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"enabled": enabled, "items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) serviceDownloadFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	downloadID, err := strconv.ParseInt(r.PathValue("download_id"), 10, 64)
	if err != nil || downloadID < 1 {
		writeError(w, 400, "invalid_id", "文件编号无效")
		return
	}
	s.serveDownloadFile(w, r, downloadID, id)
}
