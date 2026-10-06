package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func rangedFileRequest(h http.Handler, method, path, cookie, byteRange, ifRange string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	if byteRange != "" {
		r.Header.Set("Range", byteRange)
	}
	if ifRange != "" {
		r.Header.Set("If-Range", ifRange)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func checkFileRanges(t *testing.T, h http.Handler, path, cookie, full string) string {
	t.Helper()
	got := rangedFileRequest(h, "GET", path, cookie, "", "")
	expect(t, got, 200)
	if got.Body.String() != full || got.Header().Get("Accept-Ranges") != "bytes" || got.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("full response headers or bytes: %d %q %v", got.Code, got.Body.String(), got.Header())
	}
	etag := got.Header().Get("ETag")
	if len(etag) != 66 || !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("invalid strong ETag %q", etag)
	}
	first := rangedFileRequest(h, "GET", path, cookie, "bytes=0-2", etag)
	expect(t, first, 206)
	if first.Body.String() != full[:3] || first.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-2/%d", len(full)) || first.Header().Get("Content-Length") != "3" {
		t.Fatal("closed range response", first.Body.String(), first.Header())
	}
	open := rangedFileRequest(h, "GET", path, cookie, "bytes=3-", etag)
	expect(t, open, 206)
	if open.Body.String() != full[3:] {
		t.Fatal("open range response")
	}
	suffix := rangedFileRequest(h, "GET", path, cookie, "bytes=-2", etag)
	expect(t, suffix, 206)
	if suffix.Body.String() != full[len(full)-2:] {
		t.Fatal("suffix range response")
	}
	multi := rangedFileRequest(h, "GET", path, cookie, "bytes=0-1,3-4", etag)
	expect(t, multi, 206)
	if !strings.HasPrefix(multi.Header().Get("Content-Type"), "multipart/byteranges;") || !strings.Contains(multi.Body.String(), full[:2]) || !strings.Contains(multi.Body.String(), full[3:5]) {
		t.Fatal("multipart range response")
	}
	invalid := rangedFileRequest(h, "GET", path, cookie, "bytes=9999-", etag)
	expect(t, invalid, 416)
	if invalid.Header().Get("Content-Range") != fmt.Sprintf("bytes */%d", len(full)) {
		t.Fatal("unsatisfied range header")
	}
	bounded := rangedFileRequest(h, "GET", path, cookie, "bytes="+strings.Repeat("0-0,", 16)+"0-0", etag)
	expect(t, bounded, 416)
	if bounded.Header().Get("Content-Range") != fmt.Sprintf("bytes */%d", len(full)) {
		t.Fatal("bounded range header")
	}
	stale := rangedFileRequest(h, "GET", path, cookie, "bytes=0-2", `"stale"`)
	expect(t, stale, 200)
	if stale.Body.String() != full {
		t.Fatal("stale If-Range should return full file")
	}
	head := rangedFileRequest(h, "HEAD", path, cookie, "bytes=0-2", etag)
	expect(t, head, 206)
	if head.Body.Len() != 0 || head.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-2/%d", len(full)) || head.Header().Get("ETag") != etag {
		t.Fatal("HEAD range response", head.Header())
	}
	return etag
}

func TestDownloadResumeAuthorizationAndReplacement(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "resume-root", "super_admin")
	uid, customer := user(t, db, h, "resume-customer", "customer")
	in := downloadInput()
	in.AccessLevel = "login"
	id := downloadID(t, h, root, in)
	path := fmt.Sprintf("/api/v1/downloads/%d/file", id)
	if denied := rangedFileRequest(h, "GET", path, "", "bytes=0-2", ""); denied.Code != 401 || strings.Contains(denied.Body.String(), "下载内容") {
		t.Fatal("anonymous range exposed bytes")
	}
	etag := checkFileRanges(t, h, path, customer, "下载内容 UTF-8\n")
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT downloads FROM downloads WHERE id=$1", id).Scan(&count); err != nil || count != 8 {
		t.Fatal("GET counter or HEAD semantics", count, err)
	}
	in.ExpectedVersion = 1
	expect(t, downloadUpload(h, "PUT", fmt.Sprintf("/api/v1/admin/downloads/%d", id), root, "replacement.txt", []byte("replacement"), in), 200)
	replaced := rangedFileRequest(h, "GET", path, customer, "bytes=0-2", etag)
	expect(t, replaced, 200)
	if replaced.Body.String() != "replacement" || replaced.Header().Get("ETag") == etag {
		t.Fatal("replacement did not invalidate If-Range")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	if denied := rangedFileRequest(h, "GET", path, customer, "bytes=0-2", replaced.Header().Get("ETag")); denied.Code != 401 || strings.Contains(denied.Body.String(), "replacement") {
		t.Fatal("revoked session exposed bytes")
	}
	conditional := httptest.NewRequest("GET", path, nil)
	conditional.Header.Set("Cookie", customer)
	conditional.Header.Set("If-None-Match", replaced.Header().Get("ETag"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, conditional)
	if w.Code != 401 {
		t.Fatal("conditional request bypassed revoked session", w.Code)
	}
}

func TestCustomerFileResumeOwnershipAndAudit(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "resume-file-root", "super_admin")
	owner, customer := user(t, db, h, "resume-file-owner", "customer")
	_, other := user(t, db, h, "resume-file-other", "customer")
	in := store.CustomerFileInput{CustomerID: owner, Title: "Resume"}
	id := customerFileID(t, h, root, in)
	path := ownFilePath(id)
	if denied := rangedFileRequest(h, "GET", path, other, "bytes=0-2", ""); denied.Code != 404 || strings.Contains(denied.Body.String(), "客户专属") {
		t.Fatal("other customer exposed bytes")
	}
	etag := checkFileRanges(t, h, path, customer, "客户专属文件内容\n")
	var count, audit int
	if err := db.Pool.QueryRow(t.Context(), "SELECT downloads FROM customer_files WHERE id=$1", id).Scan(&count); err != nil || count != 8 {
		t.Fatal("customer GET counter or HEAD semantics", count, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE entity_type='customer_file' AND entity_id=$1 AND action='customer_file.downloaded'", fmt.Sprint(id)).Scan(&audit); err != nil || audit != count {
		t.Fatal("customer download audit", audit, err)
	}
	in.ExpectedVersion = 1
	expect(t, customerFileUpload(h, "PUT", customerFilePath(id), root, "replacement.txt", []byte("replacement"), in), 200)
	replaced := rangedFileRequest(h, "GET", path, customer, "bytes=0-2", etag)
	expect(t, replaced, 200)
	if replaced.Body.String() != "replacement" || replaced.Header().Get("ETag") == etag {
		t.Fatal("customer replacement did not invalidate If-Range")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", owner); err != nil {
		t.Fatal(err)
	}
	if denied := rangedFileRequest(h, "GET", path, customer, "bytes=0-2", replaced.Header().Get("ETag")); denied.Code != 401 || strings.Contains(denied.Body.String(), "replacement") {
		t.Fatal("revoked customer exposed bytes")
	}
}
