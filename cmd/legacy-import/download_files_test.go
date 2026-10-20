package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/httpapi"
	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/codex/zjmfgo/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func downloadImportFixture(t *testing.T) (legacy.Snapshot, string) {
	t.Helper()
	s := importFixture()
	root := t.TempDir()
	for _, dir := range []string{"support", "clients/10"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for path, body := range map[string]string{"support/stored.txt": "公共文件字节\n", "clients/10/uuid^合同.txt": "客户私有合同\n"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cat := func(id, parent, hidden string) legacy.Record {
		return legacy.Record{"id": id, "parentid": parent, "name": "目录" + id, "description": "原分类说明", "sort": "8", "hidden": hidden, "create_time": "1700000000", "update_time": "1700000100"}
	}
	s["download_categories"] = []legacy.Record{cat("72", "71", "0"), cat("71", "0", "0"), cat("73", "71", "1")}
	file := func(id, cat, clients, restricted string) legacy.Record {
		return legacy.Record{"id": id, "category": cat, "type": "资料", "title": "旧资料" + id, "description": "资料说明", "downloads": "42", "location": "stored.txt", "locationname": "指南.txt", "filetype": "upload", "clientsonly": clients, "productdownload": restricted, "hidden": "0", "create_time": "1700000000", "update_time": "1700000100"}
	}
	s["downloads"] = []legacy.Record{file("81", "72", "0", "0"), file("82", "72", "1", "0"), file("83", "72", "1", "1"), file("84", "73", "0", "0"), file("85", "0", "0", "0")}
	s["downloads"][4]["filetype"] = "remote"
	s["downloads"][4]["location"] = "https://wrong.example.test/ignored"
	s["downloads"][4]["locationname"] = "https://downloads.example.test/guide.zip"
	s["download_products"] = []legacy.Record{{"id": "91", "product_id": "167", "download_id": "83", "create_time": "1700000000", "update_time": "0"}}
	s["customer_files"] = []legacy.Record{{"id": "101", "uid": "10", "name": "客户合同", "remarks": "客户可见备注", "url": "uuid^合同.txt", "downame": "", "adminid": "999", "create_time": "1700000000"}}
	return s, root
}
func fileImporter(s legacy.Snapshot, root string, admin int64) *importer {
	im := newImporter(s)
	im.uploadRoot = root
	im.fileAuthorID = admin
	return im
}

func TestDownloadFileImportIntegrityReplayAndLiveEdits(t *testing.T) {
	db := importTestDB(t)
	admin := navigationAdmin(t, db)
	s, root := downloadImportFixture(t)
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	var oldParent, owner, author, count int64
	var body, hash, name, access string
	if err := db.Pool.QueryRow(t.Context(), `SELECT p.legacy_id FROM download_categories c JOIN download_categories p ON p.id=c.parent_id WHERE c.legacy_id=72`).Scan(&oldParent); err != nil || oldParent != 71 {
		t.Fatal("tree mapping", err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT convert_from(content,'UTF8'),sha256,filename,access_level,downloads,author_id FROM downloads WHERE legacy_id=83`).Scan(&body, &hash, &name, &access, &count, &author); err != nil || body != "公共文件字节\n" || hash != checksum([]byte(body)) || name != "指南.txt" || access != "product" || count != 42 || author != admin {
		t.Fatal("file metadata or bytes", err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT u.legacy_id,c.uploader_id,convert_from(c.content,'UTF8'),c.filename,c.sha256 FROM customer_files c JOIN users u ON u.id=c.customer_id WHERE c.legacy_id=101`).Scan(&owner, &author, &body, &name, &hash); err != nil || owner != 10 || author != admin || body != "客户私有合同\n" || name != "合同.txt" || hash != checksum([]byte(body)) {
		t.Fatal("private owner/bytes", err)
	}
	var timestamps bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT bool_and(extract(epoch FROM created_at)=1700000000 AND extract(epoch FROM updated_at)=1700000100) FROM downloads`).Scan(&timestamps); err != nil || !timestamps {
		t.Fatal("timestamps", err)
	}
	var archive string
	if err := db.Pool.QueryRow(t.Context(), `SELECT payload::text FROM legacy_source_records WHERE entity='customer_files' AND legacy_id=101`).Scan(&archive); err != nil || strings.Contains(archive, root) || !strings.Contains(archive, "_file_sha256") || !strings.Contains(archive, `"adminid": "999"`) {
		t.Fatal("archive", err)
	}
	id := importedID(t, db, "downloads", 83)
	product := importedID(t, db, "products", 167)
	if _, err := db.SaveDownload(t.Context(), admin, id, store.DownloadInput{Title: "迁移后标题", Active: true, SourceType: "local", AccessLevel: "product", ProductIDs: []int64{product}, ExpectedVersion: 1}, &store.DownloadFile{Filename: "新版.txt", MIME: "text/plain; charset=utf-8", Content: []byte("新版字节")}); err != nil {
		t.Fatal(err)
	}
	private := importedID(t, db, "customer_files", 101)
	other := importedID(t, db, "users", 11)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE customer_files SET customer_id=$2,title='已转交',version=version+1 WHERE id=$1", private, other); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE download_categories SET parent_id=NULL,name='已移动' WHERE legacy_id=72"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM legacy_id_map WHERE entity=ANY($1)", []string{"download_categories", "downloads", "download_products", "customer_files"}); err != nil {
		t.Fatal(err)
	}
	replay := fileImporter(s, root, admin)
	if err := replay.apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	if replay.report.Replayed["downloads"] != 5 || replay.report.Replayed["customer_files"] != 1 || replay.report.Replayed["download_products"] != 1 {
		t.Fatal("replay counters")
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT title,convert_from(content,'UTF8') FROM downloads WHERE id=$1", id).Scan(&name, &body); err != nil || name != "迁移后标题" || body != "新版字节" {
		t.Fatal("live file overwritten", err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT customer_id FROM customer_files WHERE id=$1", private).Scan(&owner); err != nil || owner != other {
		t.Fatal("owner transfer overwritten", err)
	}
	var moved bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT parent_id IS NULL AND name='已移动' FROM download_categories WHERE legacy_id=72").Scan(&moved); err != nil || !moved {
		t.Fatal("category edit overwritten", err)
	}
	// Removing bindings is a deliberate access change. Replay fails rather than regranting.
	if _, err := db.SaveDownload(t.Context(), admin, id, store.DownloadInput{Title: "解除关联", Active: true, SourceType: "local", AccessLevel: "public", ExpectedVersion: 2}, nil); err != nil {
		t.Fatal(err)
	}
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err == nil || !strings.Contains(err.Error(), "stale target mapping") {
		t.Fatal("removed relation recreated", err)
	}
	var n int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM download_products WHERE download_id=$1", id).Scan(&n); err != nil || n != 0 {
		t.Fatal("nil bindings did not remove", err)
	}
}

func TestDownloadImportValidationAndTransactionFailures(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(legacy.Snapshot)
	}{
		{"cycle", func(s legacy.Snapshot) { s["download_categories"][1]["parentid"] = "72" }},
		{"missing-parent", func(s legacy.Snapshot) { s["download_categories"][1]["parentid"] = "999" }},
		{"owner", func(s legacy.Snapshot) { s["customer_files"][0]["uid"] = "999" }},
		{"product", func(s legacy.Snapshot) { s["download_products"][0]["product_id"] = "999" }},
		{"duplicate-pair", func(s legacy.Snapshot) {
			r := legacy.Record{}
			for k, v := range s["download_products"][0] {
				r[k] = v
			}
			r["id"] = "92"
			s["download_products"] = append(s["download_products"], r)
		}},
		{"unbound", func(s legacy.Snapshot) { s["download_products"] = nil }},
		{"timestamp", func(s legacy.Snapshot) { s["downloads"][0]["create_time"] = "garbage" }},
		{"remote-auth", func(s legacy.Snapshot) { s["downloads"][4]["locationname"] = "https://user:pass@example.test/a" }},
		{"filename", func(s legacy.Snapshot) { s["downloads"][0]["location"] = "../secret.txt" }},
		{"entity-set", func(s legacy.Snapshot) { delete(s, "customer_files") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, root := downloadImportFixture(t)
			c.mutate(s)
			im := fileImporter(s, root, 1)
			err := im.validate()
			if err == nil {
				err = im.prepareDownloadFiles()
			}
			if err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
	for _, table := range []string{"downloads", "customer_files", "audit_events"} {
		t.Run("rollback-"+table, func(t *testing.T) {
			db := importTestDB(t)
			admin := navigationAdmin(t, db)
			s, root := downloadImportFixture(t)
			if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION fail_file_import() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected file import failure'; END $$; CREATE TRIGGER fail_file BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION fail_file_import()`); err != nil {
				t.Fatal(err)
			}
			if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err == nil {
				t.Fatal("injected failure ignored")
			}
			var n int
			for _, target := range []string{"download_categories", "downloads", "customer_files", "download_products", "products", "orders", "legacy_id_map", "legacy_source_records", "legacy_snapshot_manifests"} {
				if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM "+target).Scan(&n); err != nil || n != 0 {
					t.Fatal("partial import", target, n, err)
				}
			}
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM account_identities").Scan(&n); err != nil || n != 1 {
				t.Fatal("customer rollback", err)
			}
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM legacy_import_runs WHERE status='failed'").Scan(&n); err != nil || n != 1 {
				t.Fatal("failed run lost", err)
			}
		})
	}
}

func TestDownloadImportSnapshotAndAuthorGuards(t *testing.T) {
	db := importTestDB(t)
	admin := navigationAdmin(t, db)
	s, root := downloadImportFixture(t)
	if err := fileImporter(s, root, 0).apply(t.Context(), db.Pool); err == nil {
		t.Fatal("missing author accepted")
	}
	var customer int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO users(username,email,password_hash,display_name) VALUES('invalid-import-author','invalid-import-author@example.test','unused','Customer') RETURNING id").Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err := fileImporter(s, root, customer).apply(t.Context(), db.Pool); err == nil {
		t.Fatal("customer author accepted")
	}
	for _, change := range []string{"status='disabled'"} {
		if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET "+change+" WHERE id=$1", admin); err != nil {
			t.Fatal(err)
		}
		if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err == nil {
			t.Fatal("invalid author accepted")
		}
		if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET role='super_admin',status='active' WHERE id=$1", admin); err != nil {
			t.Fatal(err)
		}
	}
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "support/stored.txt"), []byte("源文件被改动"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatal("changed bytes accepted", err)
	}
	if err := os.WriteFile(filepath.Join(root, "support/stored.txt"), []byte("公共文件字节\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s["downloads"] = s["downloads"][:4]
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatal("deleted source accepted", err)
	}
	s, root = downloadImportFixture(t)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE legacy_snapshot_manifests SET entities=array_append(entities,'unexpected_empty')"); err != nil {
		t.Fatal(err)
	}
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err == nil || !strings.Contains(err.Error(), "snapshot contract") {
		t.Fatal("empty entity contract ignored", err)
	}
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM legacy_snapshot_manifests"); err != nil {
		t.Fatal(err)
	}
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err == nil || !strings.Contains(err.Error(), "snapshot contract") {
		t.Fatal("old contract accepted", err)
	}
}

func TestImportedFilesThroughCustomerAPI(t *testing.T) {
	db := importTestDB(t)
	admin := navigationAdmin(t, db)
	s, root := downloadImportFixture(t)
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	uid := importedID(t, db, "users", 10)
	other := importedID(t, db, "users", 11)
	hash, err := bcrypt.GenerateFromPassword([]byte("file-api-fixture"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE users SET password_hash=$1,password_reset_required=false,status='active' WHERE id=ANY($2)", string(hash), []int64{uid, other}); err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(db, config.Config{PublicOrigin: "http://localhost:3088"}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	call := func(method, path string, body any, cookie string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://localhost:3088/api/v1"+path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost:3088")
		r.Header.Set("X-Requested-With", "zjmfgo")
		r.Header.Set("Cookie", cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	login := func(name string) string {
		t.Helper()
		var email string
		if err := db.Pool.QueryRow(t.Context(), "SELECT email FROM users WHERE username=$1", name).Scan(&email); err != nil {
			t.Fatal(err)
		}
		w := call("POST", "/auth/login", map[string]string{"identifier": email, "password": "file-api-fixture"}, "")
		if w.Code != 200 {
			t.Fatal("login", w.Code)
		}
		c := w.Result().Cookies()[0]
		return c.Name + "=" + c.Value
	}
	cookie, foreign := login("legacy-10"), login("legacy-11")
	check := func(path, c string, code int, body string) {
		t.Helper()
		w := call("GET", path, nil, c)
		if w.Code != code {
			t.Fatal("API", path, w.Code, code)
		}
		if body != "" && w.Body.String() != body {
			t.Fatal("API bytes", path)
		}
	}
	path := func(old int64) string { return fmt.Sprintf("/downloads/%d/file", importedID(t, db, "downloads", old)) }
	check(path(81), "", 200, "公共文件字节\n")
	check(path(82), "", 401, "")
	check(path(82), cookie, 200, "公共文件字节\n")
	check(path(83), cookie, 403, "")
	check(path(84), cookie, 404, "")
	private := fmt.Sprintf("/customer-files/%d/file", importedID(t, db, "customer_files", 101))
	check(private, cookie, 200, "客户私有合同\n")
	check(private, foreign, 404, "")
	check(private, "", 401, "")
	remote := call("GET", path(85), nil, "")
	if remote.Code != 200 || !strings.Contains(remote.Body.String(), "https://downloads.example.test/guide.zip") || strings.Contains(remote.Body.String(), "wrong.example") {
		t.Fatal("remote contract", remote.Code)
	}
	sid := importedID(t, db, "services", 40)
	contextPath := fmt.Sprintf("/services/%d/downloads/%d/file", sid, importedID(t, db, "downloads", 83))
	check(contextPath, cookie, 403, "")
	check(contextPath, foreign, 404, "")
	if _, err = db.Pool.Exec(t.Context(), "UPDATE services SET status='active' WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	check(path(83), cookie, 403, "") // Reviewed ownership is required, even with active status.
	if _, err = db.Pool.Exec(t.Context(), "UPDATE services SET import_review_required=false WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	check(contextPath, cookie, 200, "公共文件字节\n")
	check(path(83), foreign, 403, "")
	if _, err = db.Pool.Exec(t.Context(), "UPDATE download_categories SET active=false WHERE legacy_id=71"); err != nil {
		t.Fatal(err)
	}
	check(path(81), cookie, 404, "")
	check(contextPath, cookie, 404, "")
}
