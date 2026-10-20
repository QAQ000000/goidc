package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func downloadUpload(h http.Handler, method, path, cookie, name string, payload []byte, in store.DownloadInput) *httptest.ResponseRecorder {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadata, _ := json.Marshal(in)
	writer.WriteField("metadata", string(metadata))
	if name != "" {
		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			panic(err)
		}
		part.Write(payload)
	}
	writer.Close()
	req := httptest.NewRequest(method, path, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-Requested-With", "zjmfgo")
	req.Header.Set("Origin", "http://localhost:3088")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
func downloadInput() store.DownloadInput {
	return store.DownloadInput{Title: "安装说明", Description: "产品使用说明", Active: true, AccessLevel: "public", SourceType: "local"}
}
func downloadID(t *testing.T, h http.Handler, cookie string, in store.DownloadInput) int64 {
	t.Helper()
	name := "指南.txt"
	var payload = []byte("下载内容 UTF-8\n")
	if in.SourceType == "remote" {
		name = ""
		payload = nil
	}
	w := downloadUpload(h, "POST", "/api/v1/admin/downloads", cookie, name, payload, in)
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}
func downloadItems(t *testing.T, h http.Handler, cookie string) []any {
	t.Helper()
	w := request(h, "GET", "/api/v1/downloads", nil, cookie, "")
	expect(t, w, 200)
	return data(t, w)["items"].([]any)
}
func TestDownloadsLifecycleBytesCategoriesAndSources(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "download-root", "super_admin")
	category := store.DownloadCategoryInput{Name: "工具文档", Active: true, SortOrder: 5}
	w := request(h, "POST", "/api/v1/admin/download-categories", category, root, "")
	expect(t, w, 201)
	cid := int64(data(t, w)["id"].(float64))
	in := downloadInput()
	in.CategoryID = &cid
	id := downloadID(t, h, root, in)
	path := fmt.Sprintf("/api/v1/admin/downloads/%d", id)
	filePath := fmt.Sprintf("/api/v1/downloads/%d/file", id)
	cp := fmt.Sprintf("/api/v1/admin/download-categories/%d", cid)
	items := downloadItems(t, h, "")
	if len(items) != 1 {
		t.Fatal("public file missing")
	}
	item := items[0].(map[string]any)
	for _, key := range []string{"content", "remote_url", "version", "author_id", "product_ids", "mime_type"} {
		if _, ok := item[key]; ok {
			t.Fatalf("public leaked %s", key)
		}
	}
	expect(t, request(h, "DELETE", cp, map[string]int{"expected_version": 1}, root, ""), 409)
	got := request(h, "GET", filePath, nil, "", "")
	expect(t, got, 200)
	head := request(h, "HEAD", filePath, nil, "", "")
	expect(t, head, 200)
	if head.Body.Len() != 0 {
		t.Fatal("HEAD returned bytes")
	}
	if got.Body.String() != "下载内容 UTF-8\n" || !strings.Contains(got.Header().Get("Content-Disposition"), "filename*=UTF-8''") || got.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("bytes or headers incorrect")
	}
	digest := sha256.Sum256(got.Body.Bytes())
	if item["sha256"] != hex.EncodeToString(digest[:]) {
		t.Fatal("hash differs")
	}
	if len(downloadItems(t, h, "")) != 1 {
		t.Fatal("list read affected file")
	}
	after := data(t, request(h, "GET", path, nil, root, ""))
	if after["downloads"] != float64(1) || after["version"] != float64(1) {
		t.Fatal("count or version incorrect")
	}
	in.ExpectedVersion = 1
	in.Title = "更新名称"
	expect(t, downloadUpload(h, "PUT", path, root, "", nil, in), 200)
	expect(t, downloadUpload(h, "PUT", path, root, "指南.txt", []byte("替换文档"), in), 409)
	in.ExpectedVersion = 2
	expect(t, downloadUpload(h, "PUT", path, root, "新版.txt", []byte("替换文档"), in), 200)
	got = request(h, "GET", filePath, nil, "", "")
	expect(t, got, 200)
	if got.Body.String() != "替换文档" {
		t.Fatal("replacement missing")
	}
	category.ExpectedVersion = 1
	category.Active = false
	expect(t, request(h, "PUT", cp, category, root, ""), 200)
	if len(downloadItems(t, h, "")) != 0 || len(listData(t, request(h, "GET", "/api/v1/download-categories", nil, "", ""))) != 0 {
		t.Fatal("hidden category exposed")
	}
	expect(t, request(h, "GET", filePath, nil, "", ""), 404)
	in.ExpectedVersion = 3
	in.CategoryID = nil
	in.SourceType = "remote"
	in.RemoteURL = "https://example.com/download.zip"
	expect(t, downloadUpload(h, "PUT", path, root, "", nil, in), 200)
	got = request(h, "GET", filePath, nil, "", "")
	expect(t, got, 200)
	if data(t, got)["url"] != in.RemoteURL {
		t.Fatal("remote target changed")
	}
	after = data(t, request(h, "GET", path, nil, root, ""))
	if after["size_bytes"] != float64(0) || after["sha256"] != "" || after["filename"] != "" || after["downloads"] != float64(3) {
		t.Fatal("remote retained local bytes metadata")
	}
	in.ExpectedVersion = 4
	in.SourceType = "local"
	in.RemoteURL = ""
	expect(t, downloadUpload(h, "PUT", path, root, "", nil, in), 409)
	expect(t, downloadUpload(h, "PUT", path, root, "返回.txt", []byte("本地文件"), in), 200)
	in.ExpectedVersion = 5
	in.Active = false
	expect(t, downloadUpload(h, "PUT", path, root, "", nil, in), 200)
	expect(t, request(h, "GET", filePath, nil, "", ""), 404)
	expect(t, request(h, "DELETE", path, map[string]int{"expected_version": 5}, root, ""), 409)
	expect(t, request(h, "DELETE", path, map[string]int{"expected_version": 6}, root, ""), 200)
	expect(t, request(h, "DELETE", cp, map[string]int{"expected_version": 2}, root, ""), 200)
}
func TestDownloadsLoginProductAndSettingsAuthorization(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "download-auth-root", "super_admin")
	uid, customer := user(t, db, h, "download-customer", "customer")
	_, other := user(t, db, h, "download-other", "customer")
	in := downloadInput()
	in.AccessLevel = "login"
	id := downloadID(t, h, root, in)
	path := fmt.Sprintf("/api/v1/downloads/%d/file", id)
	if downloadItems(t, h, "")[0].(map[string]any)["can_download"] != false {
		t.Fatal("anonymous allowed")
	}
	expect(t, request(h, "GET", path, nil, "", ""), 401)
	expect(t, request(h, "GET", path, nil, "zjmfgo_session=invalid", ""), 401)
	expect(t, request(h, "GET", path, nil, customer, ""), 200)
	key := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "downloads"}, customer, "")
	expect(t, key, 201)
	secret := data(t, key)["secret"].(string)
	expect(t, requestBearer(h, "GET", path, nil, secret), 200)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=now() WHERE secret_hash=$1", tokenHash(secret)); err != nil {
		t.Fatal(err)
	}
	expect(t, requestBearer(h, "GET", path, nil, secret), 401)
	first, second := product(t, db, 1), product(t, db, 1)
	in.AccessLevel = "product"
	in.ProductIDs = []int64{first, second}
	locked := downloadID(t, h, root, in)
	lockedPath := fmt.Sprintf("/api/v1/downloads/%d/file", locked)
	expect(t, request(h, "GET", lockedPath, nil, customer, ""), 403)
	expect(t, request(h, "GET", lockedPath, nil, other, ""), 403)
	version, item := add(t, h, customer, second, 1)
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, customer, "download-service"), 201)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET status='active' WHERE user_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	// Any associated product qualifies, including the second binding.
	expect(t, request(h, "GET", lockedPath, nil, customer, ""), 200)
	expect(t, request(h, "GET", lockedPath, nil, other, ""), 403)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET import_review_required=true WHERE user_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", lockedPath, nil, customer, ""), 403)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET import_review_required=false,status='suspended' WHERE user_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", lockedPath, nil, customer, ""), 403)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", path, nil, customer, ""), 401)
	settings := map[string]any{"enabled": false, "expected_version": 1}
	expect(t, request(h, "PUT", "/api/v1/admin/download-settings", settings, root, ""), 200)
	disabled := data(t, request(h, "GET", "/api/v1/downloads", nil, "", ""))
	if disabled["enabled"] != false || len(disabled["items"].([]any)) != 0 {
		t.Fatal("disabled list exposed")
	}
	expect(t, request(h, "GET", path, nil, other, ""), 403)
	expect(t, request(h, "PUT", "/api/v1/admin/download-settings", settings, root, ""), 409)
	settings["enabled"] = true
	settings["expected_version"] = 2
	expect(t, request(h, "PUT", "/api/v1/admin/download-settings", settings, root, ""), 200)
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT downloads FROM downloads WHERE id=$1", locked).Scan(&count); err != nil || count != 1 {
		t.Fatal("denials changed counter", err, count)
	}
}
func TestDownloadsPermissionsValidationAndLimits(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "download-validation-root", "super_admin")
	viewerID, viewer := user(t, db, h, "download-viewer", "admin")
	contentRole(t, db, viewerID, "downloads.view")
	_, customer := user(t, db, h, "download-validation-customer", "customer")
	in := downloadInput()
	expect(t, downloadUpload(h, "POST", "/api/v1/admin/downloads", viewer, "x.txt", []byte("safe text"), in), 403)
	expect(t, request(h, "GET", "/api/v1/admin/downloads", nil, customer, ""), 401)
	for _, p := range []string{"downloads", "download-categories", "download-settings", "download-products"} {
		expect(t, request(h, "GET", "/api/v1/admin/"+p, nil, viewer, ""), 200)
	}
	expect(t, request(h, "POST", "/api/v1/admin/roles", roleInput("不完整下载授权", "downloads.manage"), root, ""), 409)
	cases := []struct {
		name    string
		payload []byte
	}{{"x.php", []byte("<?php")}, {"../x.txt", []byte("text")}, {"x\\x.txt", []byte("text")}, {"x.txt", []byte("<html>script</html>")}, {"x.pdf", []byte("plain text")}, {"x.png", []byte("plain text")}, {"x.zip", []byte("plain text")}, {"empty.txt", nil}}
	for _, c := range cases {
		expect(t, downloadUpload(h, "POST", "/api/v1/admin/downloads", root, c.name, c.payload, in), 400)
	}
	expect(t, downloadUpload(h, "POST", "/api/v1/admin/downloads", root, "", nil, in), 409)
	in.AccessLevel = "product"
	expect(t, downloadUpload(h, "POST", "/api/v1/admin/downloads", root, "x.txt", []byte("text"), in), 409)
	in.ProductIDs = []int64{999999}
	expect(t, downloadUpload(h, "POST", "/api/v1/admin/downloads", root, "x.txt", []byte("text"), in), 404)
	in = downloadInput()
	in.SourceType = "remote"
	for _, link := range []string{"javascript:alert(1)", "//example.com/x", "file:///etc/passwd", "https://user:pass@example.com/x", "https://example.com/\\x", "https://example.com/\nLocation:x"} {
		in.RemoteURL = link
		expect(t, downloadUpload(h, "POST", "/api/v1/admin/downloads", root, "", nil, in), 409)
	}
	in = downloadInput()
	expect(t, downloadUpload(h, "POST", "/api/v1/admin/downloads", root, "huge.txt", bytes.Repeat([]byte("x"), (50<<20)+1), in), 413)
	if _, ok := downloadMIME("text.txt", []byte("valid text")); !ok {
		t.Fatal("text rejected")
	}
}
func TestDownloadsConcurrentUpdatesAuditRollbackAndCounters(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "download-race-root", "super_admin")
	in := downloadInput()
	id := downloadID(t, h, root, in)
	path := fmt.Sprintf("/api/v1/admin/downloads/%d", id)
	public := fmt.Sprintf("/api/v1/downloads/%d/file", id)
	in.ExpectedVersion = 1
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() { codes <- downloadUpload(h, "PUT", path, root, "race.txt", []byte("replaced"), in).Code })
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("update race", counts)
	}
	for range 8 {
		wg.Go(func() {
			w := request(h, "GET", public, nil, "", "")
			if w.Code != 200 || w.Body.String() != "replaced" {
				t.Error("concurrent bytes", w.Code)
			}
		})
	}
	wg.Wait()
	before := data(t, request(h, "GET", path, nil, root, ""))
	if before["downloads"] != float64(8) {
		t.Fatal("lost counter increment")
	}
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_download_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'download%' THEN RAISE EXCEPTION 'injected download audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_download_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_download_audit();`); err != nil {
		t.Fatal(err)
	}
	in.ExpectedVersion = 2
	in.Title = "must rollback"
	expect(t, downloadUpload(h, "PUT", path, root, "rollback.txt", []byte("bad replacement"), in), 500)
	expect(t, request(h, "DELETE", path, map[string]int{"expected_version": 2}, root, ""), 500)
	expect(t, request(h, "POST", "/api/v1/admin/download-categories", store.DownloadCategoryInput{Name: "rollback"}, root, ""), 500)
	expect(t, request(h, "PUT", "/api/v1/admin/download-settings", map[string]any{"enabled": false, "expected_version": 1}, root, ""), 500)
	after := data(t, request(h, "GET", path, nil, root, ""))
	if after["title"] != before["title"] || after["sha256"] != before["sha256"] || after["version"] != before["version"] {
		t.Fatal("audit failure changed file")
	}
	expect(t, request(h, "GET", public, nil, "", ""), 200)
	if data(t, request(h, "GET", "/api/v1/admin/download-settings", nil, root, ""))["version"] != float64(1) {
		t.Fatal("settings rollback failed")
	}
}
func TestDownloadsWaitingCredentialAndPermissionRevocation(t *testing.T) {
	for _, kind := range []string{"session", "api_key", "permission"} {
		t.Run(kind, func(t *testing.T) {
			db, h := testServer(t)
			_, root := user(t, db, h, "download-wait-root", "super_admin")
			uid, customer := user(t, db, h, "download-wait-customer", "customer")
			writerID, writer := user(t, db, h, "download-wait-writer", "admin")
			contentRole(t, db, writerID, "downloads.view", "downloads.manage")
			in := downloadInput()
			in.AccessLevel = "login"
			id := downloadID(t, h, root, in)
			var secret string
			if kind == "api_key" {
				w := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "waiting"}, customer, "")
				expect(t, w, 201)
				secret = data(t, w)["secret"].(string)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
			defer cancel()
			tx, err := db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108198)"); err != nil {
				t.Fatal(err)
			}
			done := make(chan int, 1)
			go func() {
				if kind == "permission" {
					done <- downloadUpload(h, "POST", "/api/v1/admin/downloads", writer, "wait.txt", []byte("text"), in).Code
				} else if kind == "api_key" {
					done <- requestBearer(h, "GET", fmt.Sprintf("/api/v1/downloads/%d/file", id), nil, secret).Code
				} else {
					done <- request(h, "GET", fmt.Sprintf("/api/v1/downloads/%d/file", id), nil, customer, "").Code
				}
			}()
			for {
				var waiting bool
				if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query='SELECT pg_advisory_xact_lock(70108198)')").Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("request never waited")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if kind == "permission" {
				_, err = tx.Exec(ctx, "UPDATE admin_roles SET permissions=ARRAY['downloads.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", writerID)
			} else if kind == "api_key" {
				_, err = tx.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE secret_hash=$1", tokenHash(secret))
			} else {
				_, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", uid)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			want := 401
			if kind == "permission" {
				want = 403
			}
			select {
			case code := <-done:
				if code != want {
					t.Fatal("revoked request", code)
				}
			case <-ctx.Done():
				t.Fatal("request stuck")
			}
			var count, downloads int
			if err = db.Pool.QueryRow(ctx, "SELECT count(*),sum(downloads) FROM downloads").Scan(&count, &downloads); err != nil || count != 1 || downloads != 0 {
				t.Fatal("revocation persisted mutation", err)
			}
		})
	}
}
func TestDownloadBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "download-super", func(db *store.Store, h http.Handler) {
		_, root := user(t, db, h, "download-fixture-root", "super_admin")
		viewerID, _ := user(t, db, h, "download-reader", "admin")
		contentRole(t, db, viewerID, "downloads.view")
		user(t, db, h, "download-browser-customer", "customer")
		product(t, db, 3)
		in := downloadInput()
		in.Title = "登录后下载的说明"
		in.AccessLevel = "login"
		downloadID(t, h, root, in)
	})
}

func TestDownloadsSearchPaginationAndVisibleCounts(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "download-page-root", "super_admin")
	w := request(h, "POST", "/api/v1/admin/download-categories", store.DownloadCategoryInput{Name: "分页文档", Active: true}, root, "")
	expect(t, w, 201)
	cid := int64(data(t, w)["id"].(float64))
	in := downloadInput()
	in.SourceType = "remote"
	in.RemoteURL = "https://example.com/resource.zip"
	in.CategoryID = &cid
	for i := range 23 {
		in.Title = fmt.Sprintf("分页指南 %02d", i)
		downloadID(t, h, root, in)
	}
	in.Title = "隐藏文件"
	in.Active = false
	downloadID(t, h, root, in)
	for offset, want := range map[int]int{0: 20, 20: 3, 40: 0} {
		page := request(h, "GET", fmt.Sprintf("/api/v1/downloads?category_id=%d&limit=20&offset=%d", cid, offset), nil, "", "")
		expect(t, page, 200)
		body := data(t, page)
		if body["total"] != float64(23) || len(body["items"].([]any)) != want {
			t.Fatal("page count", offset, body)
		}
	}
	page := data(t, request(h, "GET", "/api/v1/downloads?q=00", nil, "", ""))
	if page["total"] != float64(1) {
		t.Fatal("title search count")
	}
	for _, path := range []string{"/api/v1/downloads?q=not-found", "/api/v1/downloads?category_id=0", "/api/v1/downloads?category_id=99999"} {
		if data(t, request(h, "GET", path, nil, "", ""))["total"] != float64(0) {
			t.Fatal("empty filter", path)
		}
	}
	expect(t, request(h, "GET", "/api/v1/downloads?category_id=-1", nil, "", ""), 400)
	expect(t, request(h, "GET", "/api/v1/downloads?category_id=abc", nil, "", ""), 400)
	categories := listData(t, request(h, "GET", "/api/v1/download-categories", nil, "", ""))
	if categories[0]["file_count"] != float64(23) {
		t.Fatal("public category included hidden file")
	}
	categories = listData(t, request(h, "GET", "/api/v1/admin/download-categories", nil, root, ""))
	if categories[0]["file_count"] != float64(24) {
		t.Fatal("admin category omitted hidden file")
	}
}
