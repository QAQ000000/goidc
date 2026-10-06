package httpapi

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	_ "golang.org/x/image/webp"
)

func (s *Server) uploadContentImage(w http.ResponseWriter, r *http.Request) {
	articleID, ok := pathID(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, store.MaxContentImageBytes+(1<<20))
	parseErr := r.ParseMultipartForm(32 << 10)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err := parseErr; err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, 413, "image_too_large", "图片不能超过 5 MiB")
		} else {
			writeError(w, 400, "invalid_multipart", "图片上传格式无效")
		}
		return
	}
	if len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["file"]) != 1 || len(r.MultipartForm.Value) != 0 {
		writeError(w, 400, "invalid_multipart", "请选择一张图片")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, "image_required", "请选择图片")
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, store.MaxContentImageBytes+1))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(content) == 0 || len(content) > store.MaxContentImageBytes {
		writeError(w, 413, "image_too_large", "图片不能超过 5 MiB")
		return
	}
	name := filepath.Base(header.Filename)
	if name == "." || name == "" || strings.ContainsAny(name, "\r\n\\/") {
		writeError(w, 400, "invalid_image", "图片文件名无效")
		return
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 10000 || config.Height > 10000 || int64(config.Width)*int64(config.Height) > 25_000_000 {
		writeError(w, 415, "unsupported_image", "图片格式或尺寸不受支持")
		return
	}
	// Decode the complete image so truncated payloads and decompression bombs are rejected.
	if _, _, err = image.Decode(bytes.NewReader(content)); err != nil || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 255 {
		writeError(w, 415, "unsupported_image", "图片内容无效")
		return
	}
	mimeType := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp"}[format]
	if mimeType == "" {
		writeError(w, 415, "unsupported_image", "仅支持 PNG、JPEG、GIF 或 WebP 图片")
		return
	}
	if declared, _, e := mime.ParseMediaType(header.Header.Get("Content-Type")); e == nil && declared != "" && declared != mimeType {
		writeError(w, 415, "unsupported_image", "图片 MIME 类型无效")
		return
	}
	imageID, err := s.db.SaveContentImage(requestFileContext(r), identityFrom(r).ID, articleID, store.ContentImage{Filename: name, MIME: mimeType, Content: content})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 201, map[string]any{"id": imageID, "url": "/api/v1/content-images/" + strconv.FormatInt(imageID, 10), "mime_type": mimeType, "size_bytes": len(content)})
}

func (s *Server) contentImage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	// Authorization and publication visibility share the same database statement.
	// No previously read session or permission is reused to authorize image bytes.
	hash, credential := "", "none"
	if bearer := bearerCredential(r); bearer != "" {
		hash, credential = tokenHash(bearer), "key"
	} else if cookie, e := r.Cookie("zjmfgo_session"); e == nil {
		hash, credential = tokenHash(cookie.Value), "session"
	}
	var name, mimeType string
	var content []byte
	visible := strings.ReplaceAll(publicArticleWhere, "$2", "coalesce(v.access,0)")
	err := s.db.Pool.QueryRow(r.Context(), `WITH viewer AS (
 SELECT u.id,u.role,r.active AS role_active,r.permissions,
 CASE WHEN EXISTS(SELECT 1 FROM services WHERE user_id=u.id AND status='active' AND NOT import_review_required) THEN 2 ELSE 1 END AS access
 FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.status='active' AND (
 ($3='session' AND EXISTS(SELECT 1 FROM sessions s WHERE s.user_id=u.id AND s.token_hash=$2 AND s.expires_at>statement_timestamp())) OR
 ($3='key' AND EXISTS(SELECT 1 FROM api_keys k WHERE k.user_id=u.id AND k.secret_hash=$2 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>statement_timestamp()))))
 ) SELECT i.filename,i.mime_type,i.content FROM content_images i JOIN content_articles a ON a.id=i.article_id
 LEFT JOIN content_categories c ON c.id=a.category_id LEFT JOIN viewer v ON true WHERE i.id=$1 AND ((`+visible+`) OR
 v.role='super_admin' OR (v.role='admin' AND v.role_active AND 'content.view'=ANY(v.permissions)))`, id, hash, credential).Scan(&name, &mimeType, &content)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "not_found", "图片不存在或当前账户无法访问")
			return
		}
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `inline; filename*=UTF-8''`+url.PathEscape(name))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}
