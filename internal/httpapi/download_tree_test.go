package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func downloadCategoryID(t *testing.T, h http.Handler, root, name string, parent *int64) int64 {
	t.Helper()
	w := request(h, "POST", "/api/v1/admin/download-categories", store.DownloadCategoryInput{Name: name, ParentID: parent, Active: true}, root, "")
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}

func TestDownloadTreeVisibilityFilteringAndMoves(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "tree-root", "super_admin")
	uid, customer := user(t, db, h, "tree-customer", "customer")
	parent := downloadCategoryID(t, h, root, "工具", nil)
	child := downloadCategoryID(t, h, root, "Windows", &parent)
	leaf := downloadCategoryID(t, h, root, "安装说明", &child)
	other := downloadCategoryID(t, h, root, "其他目录", nil)
	pid := product(t, db, 5)
	sid := downloadService(t, db, h, uid, customer, pid, "tree-service-order")
	in := downloadInput()
	in.CategoryID = &leaf
	in.ProductIDs = []int64{pid}
	fid := downloadID(t, h, root, in)
	in.CategoryID = &parent
	in.Active = false
	downloadID(t, h, root, in)
	in.CategoryID = &other
	in.Active = true
	downloadID(t, h, root, in)
	listPath := fmt.Sprintf("/api/v1/downloads?category_id=%d", parent)
	adminListPath := fmt.Sprintf("/api/v1/admin/downloads?category_id=%d", parent)
	filePath := fmt.Sprintf("/api/v1/downloads/%d/file", fid)
	serviceList := fmt.Sprintf("/api/v1/services/%d/downloads", sid)
	serviceFile := fmt.Sprintf("%s/%d/file", serviceList, fid)
	if got := data(t, request(h, "GET", listPath, nil, "", "")); got["total"] != float64(1) || len(got["items"].([]any)) != 1 {
		t.Fatal("subtree filter", got)
	}
	if data(t, request(h, "GET", adminListPath, nil, root, ""))["total"] != float64(2) {
		t.Fatal("admin subtree filter")
	}
	cats := listData(t, request(h, "GET", "/api/v1/download-categories", nil, "", ""))
	var found bool
	for _, c := range cats {
		if c["id"] == float64(parent) && c["file_count"] != float64(1) {
			t.Fatal("parent count")
		}
		if c["id"] == float64(leaf) {
			found = true
			if c["depth"] != float64(2) || len(c["path"].([]any)) != 3 {
				t.Fatal("hierarchy metadata", c)
			}
		}
	}
	if !found {
		t.Fatal("leaf missing")
	}
	path := fmt.Sprintf("/api/v1/admin/download-categories/%d", parent)
	hidden := store.DownloadCategoryInput{Name: "工具", Active: false, ExpectedVersion: 1}
	expect(t, request(h, "PUT", path, hidden, root, ""), 200)
	if data(t, request(h, "GET", listPath, nil, "", ""))["total"] != float64(0) {
		t.Fatal("ancestor hidden list")
	}
	if data(t, request(h, "GET", serviceList, nil, customer, ""))["total"] != float64(1) {
		t.Fatal("service hidden subtree")
	}
	// Only the unrelated visible category's file remains in the service list.
	expect(t, request(h, "GET", filePath, nil, customer, ""), 404)
	expect(t, request(h, "GET", serviceFile, nil, customer, ""), 404)
	cats = listData(t, request(h, "GET", "/api/v1/download-categories", nil, "", ""))
	if len(cats) != 1 || cats[0]["id"] != float64(other) {
		t.Fatal("ancestor hidden categories", cats)
	}
	adminCats := listData(t, request(h, "GET", "/api/v1/admin/download-categories", nil, root, ""))
	for _, c := range adminCats {
		if c["id"] == float64(leaf) && c["effective_active"] != false {
			t.Fatal("inherited state")
		}
	}
	// Moving a branch out from a hidden ancestor restores its own enabled visibility.
	move := store.DownloadCategoryInput{Name: "Windows", ParentID: &other, Active: true, ExpectedVersion: 1}
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/download-categories/%d", child), move, root, ""), 200)
	expect(t, request(h, "GET", filePath, nil, customer, ""), 200)
	if data(t, request(h, "GET", fmt.Sprintf("/api/v1/downloads?category_id=%d", other), nil, "", ""))["total"] != float64(2) {
		t.Fatal("move subtree count")
	}
	got := data(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/downloads/%d", fid), nil, root, ""))
	if strings.Join([]string{got["category_path"].([]any)[0].(string), got["category_path"].([]any)[1].(string)}, "/") != "其他目录/Windows" {
		t.Fatal("stale path")
	}
	var count int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT downloads FROM downloads WHERE id=$1", fid).Scan(&count); err != nil || count != 1 {
		t.Fatal("denials counted", err, count)
	}
}

func TestDownloadTreeCycleDeletionAndAuditRollback(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "tree-cycle-root", "super_admin")
	a := downloadCategoryID(t, h, root, "A", nil)
	b := downloadCategoryID(t, h, root, "B", &a)
	c := downloadCategoryID(t, h, root, "C", &b)
	path := fmt.Sprintf("/api/v1/admin/download-categories/%d", a)
	for _, parent := range []int64{a, b, c} {
		expect(t, request(h, "PUT", path, store.DownloadCategoryInput{Name: "A", ParentID: &parent, Active: true, ExpectedVersion: 1}, root, ""), 409)
	}
	bad := int64(99999)
	expect(t, request(h, "PUT", path, store.DownloadCategoryInput{Name: "A", ParentID: &bad, Active: true, ExpectedVersion: 1}, root, ""), 404)
	bad = 0
	expect(t, request(h, "PUT", path, store.DownloadCategoryInput{Name: "A", ParentID: &bad, Active: true, ExpectedVersion: 1}, root, ""), 409)
	expect(t, request(h, "DELETE", path, map[string]int{"expected_version": 1}, root, ""), 409)
	// Database guard also rejects SQL writers, including self-reference insertion.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE download_categories SET parent_id=$2 WHERE id=$1", a, c); err == nil {
		t.Fatal("SQL cycle allowed")
	}
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO download_categories(id,name,parent_id) OVERRIDING SYSTEM VALUE VALUES(999,'self',999)"); err == nil {
		t.Fatal("SQL self cycle allowed")
	}
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_tree_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'download_category%' THEN RAISE EXCEPTION 'injected tree audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_tree_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_tree_audit()`); err != nil {
		t.Fatal(err)
	}
	move := store.DownloadCategoryInput{Name: "C", Active: true, ExpectedVersion: 1}
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/download-categories/%d", c), move, root, ""), 500)
	var parent, version int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT parent_id,version FROM download_categories WHERE id=$1", c).Scan(&parent, &version); err != nil || parent != b || version != 1 {
		t.Fatal("audit failure moved category", err, parent, version)
	}
}

func TestDownloadTreeConcurrentOppositeMoves(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "tree-concurrent-root", "super_admin")
	a := downloadCategoryID(t, h, root, "A", nil)
	b := downloadCategoryID(t, h, root, "B", nil)
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, pair := range [][2]int64{{a, b}, {b, a}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parent := pair[1]
			statuses <- request(h, "PUT", fmt.Sprintf("/api/v1/admin/download-categories/%d", pair[0]), store.DownloadCategoryInput{Name: "移动", ParentID: &parent, Active: true, ExpectedVersion: 1}, root, "").Code
		}()
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("opposite moves", counts)
	}
	var total int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM download_category_tree").Scan(&total); err != nil || total != 2 {
		t.Fatal("orphan cycle", err, total)
	}
}

func TestDownloadTreeWaitingAncestorHide(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "tree-wait-root", "super_admin")
	a := downloadCategoryID(t, h, root, "A", nil)
	b := downloadCategoryID(t, h, root, "B", &a)
	in := downloadInput()
	in.CategoryID = &b
	fid := downloadID(t, h, root, in)
	block, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer block.Rollback(t.Context())
	if _, err = block.Exec(t.Context(), "UPDATE download_categories SET active=false WHERE id=$1", a); err != nil {
		t.Fatal(err)
	}
	// active alone does not run the parent trigger; hold the official catalog lock as a writer does.
	if _, err = block.Exec(t.Context(), "SELECT pg_advisory_xact_lock(70108198)"); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- request(h, "GET", fmt.Sprintf("/api/v1/downloads/%d/file", fid), nil, "", "").Code }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err := db.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query='SELECT pg_advisory_xact_lock(70108198)')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no catalog wait")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := block.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-done:
		if status != 404 {
			t.Fatal("waited hidden file", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("wait stalled")
	}
}

func TestDownloadTreeBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "tree-super", func(db *store.Store, h http.Handler) {
		_, root := user(t, db, h, "tree-browser-root", "super_admin")
		viewerID, _ := user(t, db, h, "tree-reader", "admin")
		contentRole(t, db, viewerID, "downloads.view")
		a := downloadCategoryID(t, h, root, "产品工具", nil)
		b := downloadCategoryID(t, h, root, "Windows", &a)
		c := downloadCategoryID(t, h, root, "安装指南", &b)
		in := downloadInput()
		in.CategoryID = &c
		in.Title = "三层目录文档"
		downloadID(t, h, root, in)
	})
}
