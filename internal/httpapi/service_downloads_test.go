package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func downloadService(t *testing.T, db *store.Store, h http.Handler, uid int64, cookie string, productID int64, key string) int64 {
	t.Helper()
	navOrder(t, h, cookie, productID, key)
	var id int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE user_id=$1 AND product_id=$2 ORDER BY id DESC LIMIT 1", uid, productID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestServiceDownloadsOwnershipBindingsAndCurrentRights(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "service-download-root", "super_admin")
	uid, first := user(t, db, h, "service-download-first", "customer")
	_, second := user(t, db, h, "service-download-second", "customer")
	p1, p2 := product(t, db, 10), product(t, db, 10)
	sid := downloadService(t, db, h, uid, first, p1, "sd-first")
	secondService := downloadService(t, db, h, uid, first, p2, "sd-second")
	in := downloadInput()
	in.ProductIDs = []int64{p1}
	publicID := downloadID(t, h, root, in)
	in.AccessLevel = "login"
	loginID := downloadID(t, h, root, in)
	in.AccessLevel = "product"
	in.ProductIDs = []int64{p1, p2}
	lockedID := downloadID(t, h, root, in)
	in.ProductIDs = []int64{p2}
	unrelated := downloadID(t, h, root, in)
	path := fmt.Sprintf("/api/v1/services/%d/downloads", sid)
	filePath := func(id int64) string { return fmt.Sprintf("%s/%d/file", path, id) }
	expect(t, request(h, "GET", path, nil, "", ""), 401)
	expect(t, request(h, "GET", path, nil, second, ""), 404)
	expect(t, request(h, "GET", filePath(publicID), nil, second, ""), 404)
	result := data(t, request(h, "GET", path, nil, first, ""))
	if result["total"] != float64(3) || len(result["items"].([]any)) != 3 {
		t.Fatal("bindings or totals", result)
	}
	for _, raw := range result["items"].([]any) {
		item := raw.(map[string]any)
		for _, field := range []string{"content", "remote_url", "version", "author_id", "product_ids", "mime_type"} {
			if _, leaked := item[field]; leaked {
				t.Fatal("private download field", field)
			}
		}
		if item["id"] == float64(lockedID) && item["can_download"] != false {
			t.Fatal("pending service granted product access")
		}
	}
	expect(t, request(h, "GET", filePath(unrelated), nil, first, ""), 404)
	for _, id := range []int64{publicID, loginID} {
		got := request(h, "GET", filePath(id), nil, first, "")
		expect(t, got, 200)
		if got.Body.String() != "下载内容 UTF-8\n" {
			t.Fatal("wrong bytes")
		}
	}
	expect(t, request(h, "GET", filePath(lockedID), nil, first, ""), 403)
	// Central rights allow any associated active service, even if this context's service is pending.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET status='active' WHERE id=$1", secondService); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", filePath(lockedID), nil, first, ""), 200)
	head := request(h, "HEAD", filePath(lockedID), nil, first, "")
	expect(t, head, 200)
	if head.Body.Len() != 0 {
		t.Fatal("HEAD bytes")
	}
	key := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "service-files"}, first, "")
	expect(t, key, 201)
	secret := data(t, key)["secret"].(string)
	expect(t, requestBearer(h, "GET", filePath(loginID), nil, secret), 200)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET import_review_required=true WHERE id=$1", secondService); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", filePath(lockedID), nil, first, ""), 403)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET import_review_required=false,status='suspended' WHERE id=$1", secondService); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", filePath(lockedID), nil, first, ""), 403)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE downloads SET active=false WHERE id=$1", publicID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", filePath(publicID), nil, first, ""), 404)
	if data(t, request(h, "GET", path, nil, first, ""))["total"] != float64(2) {
		t.Fatal("hidden download visible")
	}
	var cid int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO download_categories(name,active) VALUES('隐藏分类',false) RETURNING id").Scan(&cid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE downloads SET category_id=$1 WHERE id=$2", cid, loginID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", filePath(loginID), nil, first, ""), 404)
	if data(t, request(h, "GET", path, nil, first, ""))["total"] != float64(1) {
		t.Fatal("hidden category visible")
	}
	expect(t, request(h, "PUT", "/api/v1/admin/download-settings", map[string]any{"enabled": false, "expected_version": 1}, root, ""), 200)
	result = data(t, request(h, "GET", path, nil, first, ""))
	if result["enabled"] != false || result["total"] != float64(0) || len(result["items"].([]any)) != 0 {
		t.Fatal("disabled leak")
	}
	expect(t, request(h, "GET", path, nil, second, ""), 404)
	expect(t, request(h, "GET", filePath(lockedID), nil, first, ""), 403)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/services/%d/downloads/bad/file", sid), nil, first, ""), 400)
	var count int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT downloads FROM downloads WHERE id=$1", lockedID).Scan(&count); err != nil || count != 1 {
		t.Fatal("denial or HEAD counted", count, err)
	}
}

func TestServiceDownloadsPaginationReplacementAndUnbinding(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "sd-page-root", "super_admin")
	uid, owner := user(t, db, h, "sd-page-owner", "customer")
	pid := product(t, db, 10)
	sid := downloadService(t, db, h, uid, owner, pid, "sd-page-order")
	in := downloadInput()
	in.ProductIDs = []int64{pid}
	var last int64
	for i := range 23 {
		in.Title = fmt.Sprintf("产品指南 %02d", i)
		last = downloadID(t, h, root, in)
	}
	path := fmt.Sprintf("/api/v1/services/%d/downloads", sid)
	for _, test := range []struct{ offset, size int }{{0, 20}, {20, 3}, {40, 0}} {
		page := data(t, request(h, "GET", fmt.Sprintf("%s?limit=20&offset=%d", path, test.offset), nil, owner, ""))
		if page["total"] != float64(23) || len(page["items"].([]any)) != test.size {
			t.Fatal("pagination", test, page)
		}
	}
	if data(t, request(h, "GET", path+"?q=22", nil, owner, ""))["total"] != float64(1) {
		t.Fatal("search")
	}
	filePath := fmt.Sprintf("%s/%d/file", path, last)
	in.ExpectedVersion = 1
	expect(t, downloadUpload(h, "PUT", fmt.Sprintf("/api/v1/admin/downloads/%d", last), root, "新指南.txt", []byte("新的产品资料"), in), 200)
	got := request(h, "GET", filePath, nil, owner, "")
	expect(t, got, 200)
	if got.Body.String() != "新的产品资料" {
		t.Fatal("stale bytes")
	}
	in.ExpectedVersion = 2
	in.ProductIDs = nil
	expect(t, downloadUpload(h, "PUT", fmt.Sprintf("/api/v1/admin/downloads/%d", last), root, "", nil, in), 200)
	expect(t, request(h, "GET", filePath, nil, owner, ""), 404)
	if data(t, request(h, "GET", path, nil, owner, ""))["total"] != float64(22) {
		t.Fatal("unbinding did not remove")
	}
}

func TestServiceDownloadsWaitingRechecksSession(t *testing.T) {
	for _, lock := range []string{"catalog", "service"} {
		for _, file := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/file=%v", lock, file), func(t *testing.T) {
				db, h := testServer(t)
				_, root := user(t, db, h, "sd-wait-root", "super_admin")
				uid, owner := user(t, db, h, "sd-wait-owner", "customer")
				pid := product(t, db, 10)
				sid := downloadService(t, db, h, uid, owner, pid, "sd-wait-order")
				in := downloadInput()
				in.ProductIDs = []int64{pid}
				fid := downloadID(t, h, root, in)
				path := fmt.Sprintf("/api/v1/services/%d/downloads", sid)
				if file {
					path += fmt.Sprintf("/%d/file", fid)
				}
				block, err := db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer block.Rollback(t.Context())
				if lock == "catalog" {
					_, err = block.Exec(t.Context(), "SELECT pg_advisory_xact_lock(70108198)")
				} else {
					_, err = block.Exec(t.Context(), "SELECT id FROM services WHERE id=$1 FOR UPDATE", sid)
				}
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan int, 1)
				go func() { done <- request(h, "GET", path, nil, owner, "").Code }()
				deadline := time.Now().Add(3 * time.Second)
				for {
					var waiting bool
					if err := db.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1)", map[bool]string{true: "SELECT product_id FROM services%", false: "SELECT pg_advisory_xact_lock(70108198)%"}[lock == "service"]).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("request did not wait")
					}
					time.Sleep(10 * time.Millisecond)
				}
				if _, err := db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", uid); err != nil {
					t.Fatal(err)
				}
				if err := block.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				select {
				case status := <-done:
					if status != 401 {
						t.Fatal("revoked session", status)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("request stalled")
				}
				var count int64
				if err := db.Pool.QueryRow(t.Context(), "SELECT downloads FROM downloads WHERE id=$1", fid).Scan(&count); err != nil || count != 0 {
					t.Fatal("revoked download counted", err, count)
				}
			})
		}
	}
}

func TestServiceDownloadBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "service-download-super", func(db *store.Store, h http.Handler) {
		_, root := user(t, db, h, "sd-browser-root", "super_admin")
		uid, owner := user(t, db, h, "sd-browser-owner", "customer")
		user(t, db, h, "sd-browser-other", "customer")
		pid := product(t, db, 10)
		downloadService(t, db, h, uid, owner, pid, "sd-browser")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET status='active' WHERE user_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		in := downloadInput()
		in.Title = "产品关联安装指南"
		in.ProductIDs = []int64{pid}
		downloadID(t, h, root, in)
	})
}

func TestServiceDownloadRemoteAndChangedServiceOwner(t *testing.T) {
	f := newServiceTransferFixture(t, false)
	db, h, root := f.db, f.h, f.root
	owner, other, pid, sid := f.customer, f.targetCookie, f.pid, f.sid
	in := downloadInput()
	in.ProductIDs = []int64{pid}
	in.SourceType = "remote"
	in.RemoteURL = "https://example.invalid/private-guide.zip"
	fid := downloadID(t, h, root, in)
	path := fmt.Sprintf("/api/v1/services/%d/downloads", sid)
	filePath := fmt.Sprintf("%s/%d/file", path, fid)
	expect(t, request(h, "GET", filePath, nil, other, ""), 404)
	got := request(h, "GET", filePath, nil, owner, "")
	expect(t, got, 200)
	if data(t, got)["url"] != in.RemoteURL {
		t.Fatal("remote URL changed")
	}
	head := request(h, "HEAD", filePath, nil, owner, "")
	expect(t, head, 200)
	if head.Body.Len() != 0 {
		t.Fatal("remote HEAD bytes")
	}
	body := serviceTransferQuote(t, f, serviceTransferInput(t, f, f.uid, f.target, 0))
	block, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer block.Rollback(t.Context())
	if _, err = block.Exec(t.Context(), "SELECT pg_advisory_xact_lock(71910322)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), `CREATE FUNCTION sd_transfer_audit_gate() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.action='service.ownership.transferred' THEN PERFORM pg_advisory_xact_lock(71910322); END IF; RETURN NEW; END $$;
 CREATE TRIGGER sd_transfer_audit_gate BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION sd_transfer_audit_gate()`); err != nil {
		t.Fatal(err)
	}
	transferred := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		transferred <- request(h, "POST", adminRenewalPath(sid)+"/transfers", body, f.staff, "sd-remote-ownership-transfer")
	}()
	// The legitimate transfer owns the service row while its audit waits. The
	// download must wait behind that same row and recheck ownership on release.
	waitUploadBlockedBy(t, db, block.Conn().PgConn().PID())
	done := make(chan int, 1)
	go func() { done <- request(h, "GET", filePath, nil, owner, "").Code }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err := db.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT product_id FROM services%')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("service download did not wait")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = block.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	expect(t, awaitBalanceResponse(t, transferred), 201)
	select {
	case status := <-done:
		if status != 404 {
			t.Fatal("changed owner retained access", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owner wait stalled")
	}
	expect(t, request(h, "GET", path, nil, owner, ""), 404)
	expect(t, request(h, "GET", path, nil, other, ""), 200)
	var count int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT downloads FROM downloads WHERE id=$1", fid).Scan(&count); err != nil || count != 1 {
		t.Fatal("denial or HEAD counted", err, count)
	}
}
