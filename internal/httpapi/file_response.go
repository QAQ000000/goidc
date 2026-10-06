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
	w = storedFileWriter{ResponseWriter: w}
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

// ServeContent may clear cache headers on precondition/range errors. Private
// files must retain their cache policy for every response, including errors.
type storedFileWriter struct{ http.ResponseWriter }

func (w storedFileWriter) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.ResponseWriter.WriteHeader(status)
}

type storedFileProbe struct {
	header http.Header
	status int
}

func (p *storedFileProbe) Header() http.Header         { return p.header }
func (p *storedFileProbe) WriteHeader(status int)      { p.status = status }
func (p *storedFileProbe) Write(b []byte) (int, error) { return len(b), nil }

// Resolve range and conditional status without sending headers or bytes before
// the authorization/counter/audit transaction commits.
func storedFileTransfersBytes(r *http.Request, name, mimeType, hash string, content []byte) bool {
	probe := &storedFileProbe{header: make(http.Header)}
	head := r.Clone(r.Context())
	head.Method = http.MethodHead
	serveStoredFile(probe, head, name, mimeType, hash, content)
	return probe.status == http.StatusOK || probe.status == http.StatusPartialContent
}
