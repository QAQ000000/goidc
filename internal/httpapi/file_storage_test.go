package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/filestorage"
	"github.com/codex/zjmfgo/internal/store"
)

type storedHTTPFile struct {
	kind, path, cookie, objectID string
	id                           int64
	content                      []byte
	ranges                       bool
}

type fileStorageHTTPFixture struct {
	upgradeFixture
	profile                    filestorage.Profile
	files                      []storedHTTPFile
	ticketID, articleID, appID int64
}

func fileStorageManager(t *testing.T, defaultID string, profiles ...filestorage.Profile) *filestorage.Manager {
	t.Helper()
	m, err := filestorage.NewManager(defaultID, profiles)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func localFileStorageHTTPFixture(t *testing.T) fileStorageHTTPFixture {
	t.Helper()
	profile := filestorage.Profile{ID: "private_disk", Implementation: "local", Values: map[string]any{"root": t.TempDir()}}
	db, h := testServerConfig(t, func(cfg *config.Config) { cfg.FileStorage = fileStorageManager(t, profile.ID, profile) })
	f := fileStorageHTTPFixture{upgradeFixture: setupConfigurationUpgradeFixture(t, db, h, false), profile: profile}
	download := downloadInput()
	download.AccessLevel = "login"
	did := downloadID(t, h, f.root, download)
	cid := customerFileID(t, h, f.root, store.CustomerFileInput{CustomerID: f.uid, Title: "Private file"})
	ticket := request(h, "POST", "/api/v1/tickets", map[string]string{"subject": "Storage ticket", "body": "Private attachment", "priority": "normal"}, f.customer, "")
	expect(t, ticket, 201)
	f.ticketID = int64(data(t, ticket)["id"].(float64))
	ticketPath := fmt.Sprintf("/api/v1/tickets/%d/attachments", f.ticketID)
	ticketBytes := []byte("private ticket attachment\n")
	attachment := multipartRequest(h, ticketPath, f.customer, "private.txt", ticketBytes)
	expect(t, attachment, 201)
	tid := int64(data(t, attachment)["id"].(float64))
	article := articleInput("knowledge", "private-storage-image", "published")
	article.AccessLevel = "login"
	f.articleID = contentID(t, h, f.root, article)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	img := imageUpload(h, fmt.Sprintf("/api/v1/admin/content/%d/images", f.articleID), f.root, "private.png", "image/png", encoded.Bytes())
	expect(t, img, 201)
	iid := int64(data(t, img)["id"].(float64))
	f.appID = issuedApplication(t, f.upgradeFixture)
	pdf := []byte("%PDF-1.7\nprivate storage invoice\n")
	doc := multipartRequest(h, fmt.Sprintf("/api/v1/admin/invoice-applications/%d/document", f.appID), f.root, "invoice.pdf", pdf)
	expect(t, doc, 201)
	vid := int64(data(t, doc)["id"].(float64))
	f.files = []storedHTTPFile{
		{kind: "downloads", id: did, path: fmt.Sprintf("/api/v1/downloads/%d/file", did), cookie: f.customer, content: []byte("下载内容 UTF-8\n"), ranges: true},
		{kind: "customer_files", id: cid, path: ownFilePath(cid), cookie: f.customer, content: []byte("客户专属文件内容\n"), ranges: true},
		{kind: "ticket_attachments", id: tid, path: fmt.Sprintf("%s/%d", ticketPath, tid), cookie: f.customer, content: ticketBytes},
		{kind: "content_images", id: iid, path: fmt.Sprintf("/api/v1/content-images/%d", iid), cookie: f.customer, content: encoded.Bytes()},
		{kind: "invoice_application_documents", id: vid, path: fmt.Sprintf("/api/v1/invoice-applications/%d/documents/%d/file", f.appID, vid), cookie: f.customer, content: pdf, ranges: true},
	}
	for i := range f.files {
		file := &f.files[i]
		var inline bool
		if err := db.Pool.QueryRow(t.Context(), "SELECT storage_object_id,content IS NOT NULL FROM "+file.kind+" WHERE id=$1", file.id).Scan(&file.objectID, &inline); err != nil || inline || file.objectID == "" {
			t.Fatalf("%s was not stored externally: inline=%v err=%v", file.kind, inline, err)
		}
		var state, kind string
		if err := db.Pool.QueryRow(t.Context(), "SELECT state,kind FROM file_storage_objects WHERE id=$1", file.objectID).Scan(&state, &kind); err != nil || state != "attached" || kind != file.kind {
			t.Fatalf("%s object state=%q kind=%q err=%v", file.kind, state, kind, err)
		}
		actual, err := os.ReadFile(filepath.Join(profile.Values["root"].(string), file.objectID))
		if err != nil || !bytes.Equal(actual, file.content) {
			t.Fatalf("%s bytes were not persisted on local backend: %v", file.kind, err)
		}
	}
	return f
}

func TestFileStorageLocalAPIFiveDomainsAndCompatibility(t *testing.T) {
	f := localFileStorageHTTPFixture(t)
	for _, file := range f.files {
		t.Run(file.kind, func(t *testing.T) {
			if file.ranges {
				checkFileRanges(t, f.h, file.path, file.cookie, string(file.content))
			} else {
				out := request(f.h, "GET", file.path, nil, file.cookie, "")
				expect(t, out, 200)
				if !bytes.Equal(out.Body.Bytes(), file.content) {
					t.Fatal("external bytes differ")
				}
				wantDisposition := "attachment;"
				if file.kind == "content_images" {
					wantDisposition = "inline;"
				}
				if !strings.HasPrefix(out.Header().Get("Content-Disposition"), wantDisposition) {
					t.Fatal("download/inline compatibility changed")
				}
			}
		})
	}
	for _, file := range f.files {
		out := request(f.h, "GET", file.path, nil, "", "")
		if out.Code == 200 || bytes.Equal(out.Body.Bytes(), file.content) {
			t.Fatalf("anonymous access exposed %s", file.kind)
		}
		if file.kind != "downloads" && file.kind != "content_images" {
			expect(t, request(f.h, "GET", file.path, nil, f.other, ""), 404)
		}
	}
	restrictedID, restricted := user(t, f.db, f.h, "storage-restricted", "admin")
	contentRole(t, f.db, restrictedID, "products.view")
	expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/admin/customer-files/%d/file", f.files[1].id), nil, restricted, ""), 403)
	expect(t, request(f.h, "GET", f.files[2].path, nil, restricted, ""), 404)
	expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/admin/invoice-applications/%d/documents/%d/file", f.appID, f.files[4].id), nil, restricted, ""), 403)
	expect(t, request(f.h, "PUT", "/api/v1/admin/download-settings", map[string]any{"enabled": false, "expected_version": 1}, f.root, ""), 200)
	for _, file := range f.files[:2] {
		expect(t, request(f.h, "GET", file.path, nil, file.cookie, ""), 403)
	}
	expect(t, request(f.h, "PUT", "/api/v1/admin/download-settings", map[string]any{"enabled": true, "expected_version": 2}, f.root, ""), 200)
	// A closed ticket retains readable history and rejects new attachments.
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/tickets/%d", f.ticketID), map[string]any{"version": 1}, f.customer, ""), 200)
	expect(t, multipartRequest(f.h, fmt.Sprintf("/api/v1/tickets/%d/attachments", f.ticketID), f.customer, "late.txt", []byte("late")), 409)
	expect(t, request(f.h, "GET", f.files[2].path, nil, f.customer, ""), 200)
	storedImage, articleID, err := f.db.ContentImage(t.Context(), f.files[3].id)
	if err != nil || articleID != f.articleID || !bytes.Equal(storedImage.Content, f.files[3].content) {
		t.Fatal("store content image reader does not resolve external bytes", err)
	}
	// Changing only the default preserves every existing object's profile.
	f.db.FileStorage = fileStorageManager(t, "database", f.profile)
	for _, file := range f.files {
		out := request(f.h, "GET", file.path, nil, file.cookie, "")
		expect(t, out, 200)
		if !bytes.Equal(out.Body.Bytes(), file.content) {
			t.Fatalf("default change broke %s", file.kind)
		}
	}
	id := customerFileID(t, f.h, f.root, store.CustomerFileInput{CustomerID: f.uid, Title: "New inline"})
	var inline bool
	var objectID *string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT content IS NOT NULL,storage_object_id FROM customer_files WHERE id=$1", id).Scan(&inline, &objectID); err != nil || !inline || objectID != nil {
		t.Fatal("new database default did not preserve inline storage", err)
	}
	f.db.FileStorage = fileStorageManager(t, f.profile.ID, f.profile)
	inlineRead := request(f.h, "GET", ownFilePath(id), nil, f.customer, "")
	expect(t, inlineRead, 200)
	if inlineRead.Body.String() != "客户专属文件内容\n" {
		t.Fatal("changing to local storage broke existing inline bytes")
	}
	manager := f.db.FileStorage
	New(f.db, config.Config{}, slog.Default())
	if f.db.FileStorage != manager {
		t.Fatal("empty server config discarded an existing storage manager")
	}
}

func TestFileStorageAPIProfileLossAndLocationDriftFailClosed(t *testing.T) {
	f := localFileStorageHTTPFixture(t)
	for _, scenario := range []struct {
		name     string
		profiles []filestorage.Profile
	}{
		{name: "missing_profile"},
		{name: "changed_location", profiles: []filestorage.Profile{{ID: f.profile.ID, Implementation: "local", Values: map[string]any{"root": t.TempDir()}}}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f.db.FileStorage = fileStorageManager(t, "database", scenario.profiles...)
			for _, file := range f.files {
				out := request(f.h, "GET", file.path, nil, file.cookie, "")
				expect(t, out, 409)
				if !strings.Contains(out.Body.String(), "file_storage_unavailable") || bytes.Equal(out.Body.Bytes(), file.content) || strings.Contains(out.Body.String(), f.profile.Values["root"].(string)) {
					t.Fatalf("%s failed open or leaked deployment location", file.kind)
				}
			}
		})
	}
	f.db.FileStorage = fileStorageManager(t, f.profile.ID, f.profile)
	for _, file := range f.files {
		expect(t, request(f.h, "GET", file.path, nil, file.cookie, ""), 200)
		location := filepath.Join(f.profile.Values["root"].(string), file.objectID)
		if err := os.WriteFile(location, bytes.Repeat([]byte{'x'}, len(file.content)), 0600); err != nil {
			t.Fatal(err)
		}
		corrupt := request(f.h, "GET", file.path, nil, file.cookie, "")
		expect(t, corrupt, 409)
		if !strings.Contains(corrupt.Body.String(), "file_storage_read_failed") || bytes.Equal(corrupt.Body.Bytes(), file.content) {
			t.Fatalf("%s sent bytes before integrity verification", file.kind)
		}
		if err := os.WriteFile(location, file.content, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFileStorageAPIReplacementRollbackAndCascadeRelease(t *testing.T) {
	f := localFileStorageHTTPFixture(t)
	file := f.files[0]
	in := downloadInput()
	in.AccessLevel, in.ExpectedVersion = "login", 1
	adminPath := fmt.Sprintf("/api/v1/admin/downloads/%d", file.id)
	expect(t, downloadUpload(f.h, "PUT", adminPath, f.root, "replacement.txt", []byte("replacement\n"), in), 200)
	var replacementID, oldState string
	var oldDue bool
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT storage_object_id FROM downloads WHERE id=$1", file.id).Scan(&replacementID); err != nil || replacementID == file.objectID {
		t.Fatal("replacement reused an immutable object", err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT state,gc_after IS NOT NULL FROM file_storage_objects WHERE id=$1", file.objectID).Scan(&oldState, &oldDue); err != nil || oldState != "ready" || !oldDue {
		t.Fatal("replacement did not release the old object", oldState, err)
	}
	if old, err := os.ReadFile(filepath.Join(f.profile.Values["root"].(string), file.objectID)); err != nil || !bytes.Equal(old, file.content) {
		t.Fatal("replacement deleted old bytes before delayed collection", err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_storage_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='download.updated' THEN RAISE EXCEPTION 'injected storage audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_storage_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_storage_audit()`); err != nil {
		t.Fatal(err)
	}
	in.ExpectedVersion = 2
	expect(t, downloadUpload(f.h, "PUT", adminPath, f.root, "rollback.txt", []byte("must not publish\n"), in), 500)
	var current string
	var version int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT storage_object_id,version FROM downloads WHERE id=$1", file.id).Scan(&current, &version); err != nil || current != replacementID || version != 2 {
		t.Fatal("rolled-back replacement changed the business reference", err)
	}
	out := request(f.h, "GET", file.path, nil, f.customer, "")
	expect(t, out, 200)
	if out.Body.String() != "replacement\n" {
		t.Fatal("rolled-back replacement exposed new bytes")
	}
	var orphanCount int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM file_storage_objects WHERE kind='downloads' AND state='ready' AND gc_after IS NOT NULL").Scan(&orphanCount); err != nil || orphanCount != 2 {
		t.Fatal("rolled-back Put did not remain durably collectable", orphanCount, err)
	}
	// The database cascade must release the image even without a file-delete API.
	expect(t, request(f.h, "DELETE", fmt.Sprintf("/api/v1/admin/content/%d", f.articleID), map[string]int{"expected_version": 1}, f.root, ""), 200)
	expect(t, request(f.h, "GET", f.files[3].path, nil, f.customer, ""), 404)
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT state,gc_after IS NOT NULL FROM file_storage_objects WHERE id=$1", f.files[3].objectID).Scan(&oldState, &oldDue); err != nil || oldState != "ready" || !oldDue {
		t.Fatal("article cascade orphaned its external image", err)
	}
	// Deleting an internal note follows a different cascade than deleting a ticket.
	var noteID int64
	if err := f.db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_notes(ticket_id,author_id,body) VALUES($1,(SELECT id FROM administrators WHERE role='super_admin' LIMIT 1),'private') RETURNING id", f.ticketID).Scan(&noteID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE ticket_attachments SET note_id=$2 WHERE id=$1", f.files[2].id, noteID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", f.files[2].path, nil, f.customer, ""), 404)
	expect(t, request(f.h, "DELETE", fmt.Sprintf("/api/v1/admin/tickets/%d/notes/%d", f.ticketID, noteID), nil, f.root, ""), 200)
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT state,gc_after IS NOT NULL FROM file_storage_objects WHERE id=$1", f.files[2].objectID).Scan(&oldState, &oldDue); err != nil || oldState != "ready" || !oldDue {
		t.Fatal("internal-note cascade orphaned its external attachment", err)
	}
}

func TestFileStorageAPIWaitingReadRechecksCredentials(t *testing.T) {
	for _, kind := range []string{"ticket_attachments", "content_images"} {
		t.Run(kind, func(t *testing.T) {
			f := localFileStorageHTTPFixture(t)
			var file storedHTTPFile
			for _, candidate := range f.files {
				if candidate.kind == kind {
					file = candidate
				}
			}
			tx, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err = tx.Exec(t.Context(), "SELECT id FROM file_storage_objects WHERE id=$1 FOR UPDATE", file.objectID); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- request(f.h, "GET", file.path, nil, f.customer, "") }()
			waitQueryLock(t, f.db, `SELECT profile_id,location_sha256,kind,object_key,size_bytes,sha256,state FROM file_storage_objects WHERE id=$1 FOR SHARE`)
			if _, err = f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(f.customer))); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case out := <-done:
				want := 401
				if kind == "content_images" {
					want = 404
				}
				expect(t, out, want)
				if bytes.Equal(out.Body.Bytes(), file.content) {
					t.Fatal("revoked credential exposed external bytes")
				}
			case <-time.After(7 * time.Second):
				t.Fatal("file read did not finish after releasing the object lock")
			}
		})
	}
}

func TestFileStorageS3APIPrivateRoundTripAndCollection(t *testing.T) {
	type object struct {
		content []byte
		hash    string
	}
	var mu sync.Mutex
	objects := map[string]object{}
	counts := map[string]int{}
	failure := ""
	const accessKey = "TESTACCESSKEY"
	const securityToken = "isolated-session-token"
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		counts[r.Method]++
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential="+accessKey+"/") || !strings.Contains(auth, "/us-east-1/s3/aws4_request, SignedHeaders=") || !strings.Contains(auth, "x-amz-security-token") || r.Header.Get("X-Amz-Security-Token") != securityToken || r.Header.Get("X-Amz-Date") == "" || r.Header.Get("X-Amz-Content-Sha256") == "" {
			t.Error("object request omitted signed private credentials")
			w.WriteHeader(403)
			return
		}
		if r.URL.RawQuery != "" || !strings.HasPrefix(r.URL.Path, "/private-files/application/") || r.Header.Get("X-Amz-Acl") != "" {
			t.Error("object request changed the private path or used a public access mechanism")
			w.WriteHeader(403)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/private-files/application/")
		if len(key) != 64 || strings.Contains(key, "/") {
			t.Error("unexpected object key")
			w.WriteHeader(400)
			return
		}
		item, found := objects[key]
		etag := `"opaque-` + key + `"`
		switch r.Method {
		case http.MethodPut:
			if r.Header.Get("If-None-Match") != "*" || !strings.Contains(auth, "if-none-match") {
				t.Error("Put is not conditional")
				w.WriteHeader(400)
				return
			}
			if found {
				w.WriteHeader(412)
				return
			}
			content, err := io.ReadAll(r.Body)
			hash := fmt.Sprintf("%x", sha256.Sum256(content))
			if err != nil || r.Header.Get("X-Amz-Meta-Sha256") != hash || r.Header.Get("X-Amz-Content-Sha256") != hash {
				t.Error("Put payload digest differs from signed metadata")
				w.WriteHeader(400)
				return
			}
			objects[key] = object{content: content, hash: hash}
			w.WriteHeader(200)
		case http.MethodHead, http.MethodGet:
			if !found {
				w.WriteHeader(404)
				return
			}
			if failure == "get_error" && r.Method == http.MethodGet {
				w.WriteHeader(503)
				return
			}
			w.Header().Set("X-Amz-Meta-Sha256", item.hash)
			if failure == "bad_metadata" {
				w.Header().Set("X-Amz-Meta-Sha256", strings.Repeat("0", 64))
			}
			w.Header().Set("ETag", etag)
			if r.Method == http.MethodHead {
				w.Header().Set("Content-Length", strconv.Itoa(len(item.content)))
				w.WriteHeader(200)
				return
			}
			var start, end int
			if r.Header.Get("If-Match") != etag || !strings.Contains(auth, "if-match;range") {
				t.Error("Get did not fence the previously read object version")
				w.WriteHeader(412)
				return
			}
			if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || start < 0 || end != len(item.content)-1 || start > end {
				t.Error("unexpected S3 range")
				w.WriteHeader(416)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(item.content)))
			w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
			w.WriteHeader(206)
			_, _ = w.Write(item.content[start : end+1])
		case http.MethodDelete:
			delete(objects, key)
			w.WriteHeader(204)
		default:
			t.Error("unexpected object operation")
			w.WriteHeader(405)
		}
	}))
	defer s3.Close()
	profile := filestorage.Profile{ID: "private_s3", Implementation: "s3", Values: map[string]any{"endpoint": s3.URL, "region": "us-east-1", "bucket": "private-files", "prefix": "application"}, Secrets: map[string]string{"access_key": accessKey, "secret_key": "isolated-secret-key", "session_token": securityToken}}
	db, h := testServerConfig(t, func(cfg *config.Config) { cfg.FileStorage = fileStorageManager(t, profile.ID, profile) })
	_, root := user(t, db, h, "s3-file-root", "super_admin")
	_, customer := user(t, db, h, "s3-file-customer", "customer")
	in := downloadInput()
	in.AccessLevel = "login"
	content := []byte("S3 private application payload\n")
	upload := downloadUpload(h, "POST", "/api/v1/admin/downloads", root, "private.txt", content, in)
	expect(t, upload, 201)
	id := int64(data(t, upload)["id"].(float64))
	var objectID string
	var inline bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT storage_object_id,content IS NOT NULL FROM downloads WHERE id=$1", id).Scan(&objectID, &inline); err != nil || inline {
		t.Fatal("S3 upload did not attach an external object", err)
	}
	path := fmt.Sprintf("/api/v1/downloads/%d/file", id)
	checkFileRanges(t, h, path, customer, string(content))
	read := request(h, "GET", path, nil, customer, "")
	expect(t, read, 200)
	if !bytes.Equal(read.Body.Bytes(), content) || read.Header().Get("Location") != "" || strings.Contains(read.Header().Get("Content-Type"), "json") {
		t.Fatal("private S3 file was exposed as a redirect or URL response")
	}
	mu.Lock()
	beforeDenied := counts[http.MethodHead] + counts[http.MethodGet]
	mu.Unlock()
	expect(t, request(h, "GET", path, nil, "", ""), 401)
	mu.Lock()
	afterDenied := counts[http.MethodHead] + counts[http.MethodGet]
	mu.Unlock()
	if afterDenied != beforeDenied {
		t.Fatal("denied API request reached object storage")
	}
	for _, mode := range []string{"bad_metadata", "get_error"} {
		mu.Lock()
		failure = mode
		mu.Unlock()
		failed := request(h, "GET", path, nil, customer, "")
		expect(t, failed, 409)
		if !strings.Contains(failed.Body.String(), "file_storage_read_failed") || strings.Contains(failed.Body.String(), s3.URL) || strings.Contains(failed.Body.String(), accessKey) || failed.Header().Get("Location") != "" || bytes.Equal(failed.Body.Bytes(), content) {
			t.Fatal("S3 read failure exposed bytes or configuration")
		}
	}
	mu.Lock()
	failure = ""
	mu.Unlock()
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/downloads/%d", id), map[string]int{"expected_version": 1}, root, ""), 200)
	expect(t, request(h, "GET", path, nil, customer, ""), 404)
	if n, err := db.CollectFileObjects(t.Context(), 10); err != nil || n != 0 {
		t.Fatal("object was collected before the grace period", n, err)
	}
	mu.Lock()
	_, retained := objects[objectID]
	deletes := counts[http.MethodDelete]
	mu.Unlock()
	if !retained || deletes != 0 {
		t.Fatal("API deletion synchronously deleted the S3 payload")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE file_storage_objects SET gc_after=clock_timestamp()-interval '1 second' WHERE id=$1", objectID); err != nil {
		t.Fatal(err)
	}
	if n, err := db.CollectFileObjects(t.Context(), 10); err != nil || n != 1 {
		t.Fatal("released S3 object was not collected", n, err)
	}
	var state string
	if err := db.Pool.QueryRow(t.Context(), "SELECT state FROM file_storage_objects WHERE id=$1", objectID).Scan(&state); err != nil || state != "deleted" {
		t.Fatal("S3 collection did not commit its tombstone", state, err)
	}
	mu.Lock()
	_, retained = objects[objectID]
	deletes = counts[http.MethodDelete]
	puts := counts[http.MethodPut]
	mu.Unlock()
	if retained || deletes != 1 || puts != 1 {
		t.Fatal("S3 lifecycle repeated Put or failed to delete exactly once", retained, deletes, puts)
	}
}
