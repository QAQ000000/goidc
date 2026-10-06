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

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	_ "golang.org/x/image/webp"
)

func (s *Server) uploadContentImage(w http.ResponseWriter, r *http.Request) {
	articleID, ok := pathID(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, store.MaxContentImageBytes+1<<20)
	if err := r.ParseMultipartForm(32 << 10); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, 413, "image_too_large", "图片不能超过 5 MiB")
		} else {
			writeError(w, 400, "invalid_multipart", "图片上传格式无效")
		}
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
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 10000 || config.Height > 10000 {
		writeError(w, 415, "unsupported_image", "图片格式或尺寸不受支持")
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
	imageID, err := s.db.SaveContentImage(r.Context(), identityFrom(r).ID, articleID, store.ContentImage{Filename: name, MIME: mimeType, Content: content})
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
	level, err := s.contentAccess(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var name, mimeType string
	var content []byte
	err = s.db.Pool.QueryRow(r.Context(), `SELECT i.filename,i.mime_type,i.content FROM content_images i JOIN content_articles a ON a.id=i.article_id LEFT JOIN content_categories c ON c.id=a.category_id WHERE i.id=$1 AND `+publicArticleWhere, id, level).Scan(&name, &mimeType, &content)
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
