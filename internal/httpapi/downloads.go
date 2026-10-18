package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/fileformat"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

const downloadColumns = `d.id,d.category_id,c.name AS category_name,(SELECT path FROM download_category_tree WHERE id=d.category_id) AS category_path,d.title,d.description,d.access_level,d.source_type,d.filename,d.size_bytes,d.sha256,d.downloads,d.created_at,d.updated_at`
const downloadAdminColumns = downloadColumns + `,d.active,d.version,d.remote_url,d.mime_type,ARRAY(SELECT product_id FROM download_products WHERE download_id=d.id ORDER BY product_id) AS product_ids`
const downloadVisible = `d.active AND download_category_visible(d.category_id)`

func (s *Server) downloadProducts(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, "SELECT id,name,active FROM products ORDER BY id")
}

// Re-read credentials after waiting for catalog locks; revoked sessions never authorize bytes.
func (s *Server) publicDownloadIdentity(r *http.Request, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (int64, error) {
	var id int64
	var err error
	if bearer := bearerCredential(r); bearer != "" {
		err = q.QueryRow(r.Context(), `SELECT u.id FROM api_keys k JOIN users u ON u.id=k.user_id WHERE k.secret_hash=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp()) AND u.status='active'`, tokenHash(bearer)).Scan(&id)
	} else if cookie, e := r.Cookie("zjmfgo_session"); e == nil {
		err = q.QueryRow(r.Context(), `SELECT u.id FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>clock_timestamp() AND u.status='active'`, tokenHash(cookie.Value)).Scan(&id)
	} else {
		return 0, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func (s *Server) downloadsList(w http.ResponseWriter, r *http.Request) {
	admin := strings.HasPrefix(r.URL.Path, "/api/v1/admin/")
	category, ok := categoryFilter(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	enabled := true
	if !admin {
		if err := s.db.Pool.QueryRow(r.Context(), "SELECT enabled FROM download_settings WHERE id=1").Scan(&enabled); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if !enabled {
		write(w, 200, map[string]any{"enabled": false, "items": []any{}, "total": 0, "limit": limit, "offset": offset})
		return
	}
	uid, err := s.publicDownloadIdentity(r, s.db.Pool)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	columns := downloadColumns + `,download_allowed(d.id,$5) AS can_download`
	visibility := downloadVisible
	if admin {
		columns = downloadAdminColumns + `,$5::bigint AS viewer_id`
		visibility = "true"
	}
	where := ` FROM downloads d LEFT JOIN download_categories c ON c.id=d.category_id WHERE ` + visibility + `
  AND ($1='' OR d.title ILIKE '%'||$1||'%' OR d.description ILIKE '%'||$1||'%') AND ($2::bigint IS NULL OR ($2=0 AND d.category_id IS NULL) OR download_category_contains($2,d.category_id))`
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	var total int64
	if err = s.db.Pool.QueryRow(r.Context(), "SELECT count(*)"+where, query, category).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), "SELECT "+columns+where+" ORDER BY d.id DESC LIMIT $3 OFFSET $4", query, category, limit, offset, uid)
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
	write(w, 200, map[string]any{"enabled": enabled, "items": items, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) adminDownloadDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), "SELECT "+downloadAdminColumns+" FROM downloads d LEFT JOIN download_categories c ON c.id=d.category_id WHERE d.id=$1", id)
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
func (s *Server) downloadCategories(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/v1/admin/") {
		s.list(w, r, `SELECT c.id,c.parent_id,c.name,c.description,c.sort_order,c.active,c.version,c.created_at,c.updated_at,c.path,c.depth,c.effective_active,
 (SELECT count(*) FROM downloads d WHERE download_category_contains(c.id,d.category_id)) AS file_count,
 (SELECT count(*) FROM downloads d WHERE d.category_id=c.id) AS direct_file_count,
 (SELECT count(*) FROM download_categories child WHERE child.parent_id=c.id) AS child_count
 FROM download_category_tree c ORDER BY c.sort_path`)
		return
	}
	s.list(w, r, `SELECT c.id,c.parent_id,c.name,c.description,c.path,c.depth,
 (SELECT count(*) FROM downloads d WHERE download_category_contains(c.id,d.category_id) AND d.active AND download_category_visible(d.category_id)) AS file_count
 FROM download_category_tree c WHERE c.effective_active AND (SELECT enabled FROM download_settings WHERE id=1) ORDER BY c.sort_path`)
}
func downloadMIME(name string, content []byte) (string, bool) {
	return fileformat.DownloadMIME(name, content)
}
func (s *Server) saveDownload(w http.ResponseWriter, r *http.Request) {
	var id int64
	var ok bool
	if r.Method == "PUT" {
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var in store.DownloadInput
	file, valid := s.readDownloadUpload(w, r, &in)
	if !valid {
		return
	}
	saved, err := s.db.SaveDownload(requestFileContext(r), identityFrom(r).ID, id, in, file)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if id == 0 {
		status = 201
	}
	write(w, status, map[string]int64{"id": saved})
}
func (s *Server) readDownloadUpload(w http.ResponseWriter, r *http.Request, in any) (*store.DownloadFile, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, (50<<20)+(256<<10))
	if err := r.ParseMultipartForm(32 << 10); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, 413, "download_too_large", "文件不能超过 50 MiB")
		} else {
			writeError(w, 400, "invalid_multipart", "文件上传格式无效")
		}
		return nil, false
	}
	defer r.MultipartForm.RemoveAll()
	metadata := r.MultipartForm.Value["metadata"]
	if len(metadata) != 1 || len(metadata[0]) > 128<<10 {
		writeError(w, 400, "invalid_download", "文件信息无效")
		return nil, false
	}
	decoder := json.NewDecoder(strings.NewReader(metadata[0]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(in); err != nil {
		writeError(w, 400, "invalid_download", "文件信息无效")
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(w, 400, "invalid_download", "文件信息无效")
		return nil, false
	}
	var file *store.DownloadFile
	files := r.MultipartForm.File["file"]
	if len(files) > 1 {
		writeError(w, 400, "invalid_download_file", "每次只能上传一个文件")
		return nil, false
	}
	if len(files) == 1 {
		header := files[0]
		name := header.Filename
		// multipart strips forward-slash directory components; reject the raw disposition as well.
		_, params, e := mime.ParseMediaType(header.Header.Get("Content-Disposition"))
		if e != nil || params["filename"] != name || name == "" || strings.ContainsAny(name, "/\\\r\n\x00") {
			writeError(w, 400, "invalid_download_file", "文件名无效")
			return nil, false
		}
		stream, err := header.Open()
		if err != nil {
			s.fail(w, r, err)
			return nil, false
		}
		payload, err := io.ReadAll(io.LimitReader(stream, (50<<20)+1))
		stream.Close()
		if err != nil {
			s.fail(w, r, err)
			return nil, false
		}
		if len(payload) > 50<<20 {
			writeError(w, 413, "download_too_large", "文件不能超过 50 MiB")
			return nil, false
		}
		mt, valid := downloadMIME(name, payload)
		if !valid || len(payload) == 0 {
			writeError(w, 400, "invalid_download_file", "文件扩展名与内容不匹配，支持文档、图片与压缩包")
			return nil, false
		}
		file = &store.DownloadFile{Filename: name, MIME: mt, Content: payload}
	}
	return file, true
}
func (s *Server) deleteDownload(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.db.DeleteDownload(r.Context(), identityFrom(r).ID, id, in.ExpectedVersion); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
func (s *Server) saveDownloadCategory(w http.ResponseWriter, r *http.Request) {
	var id int64
	var ok bool
	if r.Method == "PUT" {
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var in store.DownloadCategoryInput
	if !decode(w, r, &in) {
		return
	}
	saved, err := s.db.SaveDownloadCategory(r.Context(), identityFrom(r).ID, id, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if id == 0 {
		status = 201
	}
	write(w, status, map[string]int64{"id": saved})
}
func (s *Server) deleteDownloadCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.db.DeleteDownloadCategory(r.Context(), identityFrom(r).ID, id, in.ExpectedVersion); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
func (s *Server) downloadSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		rows, err := s.db.Pool.Query(r.Context(), "SELECT enabled,version,updated_at FROM download_settings WHERE id=1")
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
		return
	}
	var in struct {
		Enabled         bool  `json:"enabled"`
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	tx, err := s.db.DownloadTx(r.Context(), identityFrom(r).ID, true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), "UPDATE download_settings SET enabled=$1,version=version+1,updated_at=now() WHERE id=1 AND version=$2", in.Enabled, in.ExpectedVersion)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if tag.RowsAffected() != 1 {
		writeError(w, 409, "version_conflict", "下载设置已变更，请重新加载")
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "download.settings_updated", "download_settings", 1, map[string]any{"enabled": in.Enabled, "version": in.ExpectedVersion + 1}); err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"saved": true})
}
func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.serveDownloadFile(w, r, id, 0)
}
func (s *Server) serveDownloadFile(w http.ResponseWriter, r *http.Request, id, serviceID int64) {
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
	if serviceID > 0 {
		productID, valid := s.ownedDownloadService(w, r, tx, serviceID, uid)
		if !valid {
			return
		}
		var bound bool
		if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM download_products WHERE download_id=$1 AND product_id=$2)", id, productID).Scan(&bound); err != nil {
			s.fail(w, r, err)
			return
		}
		if !bound {
			writeError(w, 404, "not_found", "文件不存在")
			return
		}
	}
	var enabled bool
	if err = tx.QueryRow(r.Context(), "SELECT enabled FROM download_settings WHERE id=1").Scan(&enabled); err != nil {
		s.fail(w, r, err)
		return
	}
	if !enabled {
		writeError(w, 403, "downloads_disabled", "下载中心已关闭")
		return
	}
	var name, mt, source, link, access, hash string
	var content []byte
	var allowed bool
	err = tx.QueryRow(r.Context(), `SELECT d.filename,d.mime_type,d.source_type,d.remote_url,d.access_level,d.sha256,download_allowed(d.id,$2)
 FROM downloads d LEFT JOIN download_categories c ON c.id=d.category_id WHERE d.id=$1 AND `+downloadVisible, id, uid).Scan(&name, &mt, &source, &link, &access, &hash, &allowed)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !allowed {
		code, message := "download_product_required", "需要持有指定产品的有效服务"
		status := 403
		if uid == 0 {
			code, message, status = "login_required", "请先登录再下载", 401
		}
		writeError(w, status, code, message)
		return
	}
	if source == "local" {
		var objectID *string
		if err = tx.QueryRow(r.Context(), "SELECT content,storage_object_id FROM downloads WHERE id=$1", id).Scan(&content, &objectID); err != nil {
			s.fail(w, r, err)
			return
		}
		content, err = s.db.ReadFileContent(r.Context(), tx, "downloads", content, objectID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		fresh, authErr := s.publicDownloadIdentity(r, tx)
		if authErr != nil {
			s.fail(w, r, authErr)
			return
		}
		if fresh != uid {
			writeError(w, 401, "unauthorized", "登录已失效")
			return
		}
		// Service eligibility can change while an external object read waits.
		// Catalog locks serialize file changes, but do not lock service status.
		if err = tx.QueryRow(r.Context(), "SELECT download_allowed($1,$2)", id, uid).Scan(&allowed); err != nil {
			s.fail(w, r, err)
			return
		}
		if !allowed {
			writeError(w, 403, "download_product_required", "需要持有指定产品的有效服务")
			return
		}
	}
	if r.Method != "HEAD" && (source == "remote" || storedFileTransfersBytes(r, name, mt, hash, content)) {
		if _, err = tx.Exec(r.Context(), "UPDATE downloads SET downloads=downloads+1 WHERE id=$1", id); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if source == "remote" {
		if r.Method == "HEAD" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(200)
			return
		}
		write(w, 200, map[string]string{"url": link})
		return
	}
	serveStoredFile(w, r, name, mt, hash, content)
}
