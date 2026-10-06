package httpapi

import (
	"bytes"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func serveStoredFile(w http.ResponseWriter, r *http.Request, name, mimeType, sha256 string, content []byte) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Disposition", `attachment; filename="download"; filename*=UTF-8''`+url.PathEscape(name))
	w.Header().Set("ETag", `"`+sha256+`"`)
	// Keep multipart responses bounded even when a client sends thousands of ranges.
	if ranges := r.Header.Get("Range"); (len(ranges) > 8192 || strings.Count(ranges, ",") >= 16) && (r.Header.Get("If-Range") == "" || r.Header.Get("If-Range") == w.Header().Get("ETag")) {
		w.Header().Set("Content-Range", "bytes */"+strconv.Itoa(len(content)))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(content))
}
