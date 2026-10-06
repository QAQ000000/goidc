package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func articleInput(kind, slug, status string) store.ContentInput {
	return store.ContentInput{ContentType: kind, Title: "指南 " + slug, Slug: slug, Excerpt: "共同搜索关键词", Body: "正文内容", Status: status, AccessLevel: "public"}
}
func categoryInput(kind, slug string) store.ContentCategoryInput {
	return store.ContentCategoryInput{ContentType: kind, Name: "分类 " + slug, Slug: slug, Description: "说明", Active: true}
}
func contentID(t *testing.T, h http.Handler, root string, input store.ContentInput) int64 {
	t.Helper()
	w := request(h, "POST", "/api/v1/admin/content", input, root, "")
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}
func contentCategoryID(t *testing.T, h http.Handler, root string, input store.ContentCategoryInput) int64 {
	t.Helper()
	w := request(h, "POST", "/api/v1/admin/content-categories", input, root, "")
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}
func contentRole(t *testing.T, db *store.Store, id int64, grants ...string) {
	t.Helper()
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=$2 WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", id, grants); err != nil {
		t.Fatal(err)
	}
}

func TestContentPermissionsAndMigration(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "content-root", "super_admin")
	viewerID, viewer := user(t, db, h, "content-viewer", "admin")
	writerID, writer := user(t, db, h, "content-writer", "admin")
	_, customer := user(t, db, h, "content-customer", "customer")
	contentRole(t, db, viewerID, "content.view")
	contentRole(t, db, writerID, "content.view", "content.manage")
	id := contentID(t, h, root, articleInput("news", "permission-news", "draft"))
	for _, cookie := range []string{viewer, writer} {
		expect(t, request(h, "GET", "/api/v1/admin/content", nil, cookie, ""), 200)
		expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/content/%d", id), nil, cookie, ""), 200)
		expect(t, request(h, "GET", "/api/v1/admin/content-categories", nil, cookie, ""), 200)
	}
	for _, cookie := range []string{viewer, customer} {
		expect(t, request(h, "POST", "/api/v1/admin/content", articleInput("news", "denied", "draft"), cookie, ""), 403)
		expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/content/%d", id), map[string]int{"expected_version": 1}, cookie, ""), 403)
		expect(t, request(h, "POST", "/api/v1/admin/content-categories", categoryInput("news", "denied"), cookie, ""), 403)
	}
	expect(t, request(h, "GET", "/api/v1/admin/content", nil, customer, ""), 403)
	expect(t, request(h, "GET", "/api/v1/admin/content", nil, "", ""), 401)
	w := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "content-editor"}, writer, "")
	expect(t, w, 201)
	secret := data(t, w)["secret"].(string)
	expect(t, requestBearer(h, "POST", "/api/v1/admin/content", articleInput("news", "key-news", "draft"), secret), 201)
	expect(t, request(h, "POST", "/api/v1/admin/roles", roleInput("缺少内容读取", "content.manage"), root, ""), 409)
	contentRole(t, db, writerID, "content.manage") // Malformed legacy grants must not bypass the store check.
	expect(t, requestBearer(h, "POST", "/api/v1/admin/content", articleInput("news", "denied-key", "draft"), secret), 403)
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM schema_migrations WHERE version='036_content_management.sql'; ALTER TABLE content_articles DROP CONSTRAINT news_public_access,DROP COLUMN version,DROP COLUMN access_level; ALTER TABLE content_categories DROP COLUMN description,DROP COLUMN version,DROP COLUMN created_at,DROP COLUMN updated_at;"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []int64{viewerID, writerID} {
		var grants []string
		var version int
		if err := db.Pool.QueryRow(t.Context(), "SELECT permissions,version FROM admin_roles WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", uid).Scan(&grants, &version); err != nil {
			t.Fatal(err)
		}
		wantLength, wantVersion := 1, 1
		if uid == writerID {
			wantLength, wantVersion = 2, 2
		}
		if len(grants) != wantLength || version != wantVersion {
			t.Fatalf("migration grants=%v version=%d", grants, version)
		}
	}
	expect(t, requestBearer(h, "POST", "/api/v1/admin/content", articleInput("news", "migrated-key", "draft"), secret), 201)
}

func TestContentLifecycleCategoriesAndValidation(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "lifecycle-root", "super_admin")
	c := categoryInput("news", "news-category")
	cid := contentCategoryID(t, h, root, c)
	other := contentCategoryID(t, h, root, categoryInput("knowledge", "knowledge-category"))
	in := articleInput("news", "lifecycle-news", "draft")
	in.CategoryID = &other
	expect(t, request(h, "POST", "/api/v1/admin/content", in, root, ""), 409)
	missing := int64(999999)
	in.CategoryID = &missing
	expect(t, request(h, "POST", "/api/v1/admin/content", in, root, ""), 404)
	in.CategoryID = &cid
	// Four-byte Unicode and JSON escaping fit the supported 20,000-character body.
	in.Body = strings.Repeat("🙂", 20000)
	id := contentID(t, h, root, in)
	adminPath, publicPath := fmt.Sprintf("/api/v1/admin/content/%d", id), fmt.Sprintf("/api/v1/news/%d", id)
	expect(t, request(h, "POST", "/api/v1/admin/content", in, root, ""), 409)
	expect(t, request(h, "GET", publicPath, nil, "", ""), 404)
	categoryPath := fmt.Sprintf("/api/v1/admin/content-categories/%d", cid)
	expect(t, request(h, "DELETE", categoryPath, map[string]int{"expected_version": 1}, root, ""), 409)
	in.ExpectedVersion, in.Status = 1, "published"
	expect(t, request(h, "PUT", adminPath, in, root, ""), 200)
	w := request(h, "GET", adminPath, nil, root, "")
	expect(t, w, 200)
	stamp := data(t, w)["published_at"]
	if stamp == nil || data(t, w)["views"] != float64(0) {
		t.Fatal("admin detail must not count views")
	}
	pub := request(h, "GET", publicPath, nil, "", "")
	expect(t, pub, 200)
	if data(t, pub)["views"] != float64(1) {
		t.Fatal("view count not advanced")
	}
	for _, field := range []string{"status", "access_level", "author_id", "version", "scheduled"} {
		if _, ok := data(t, pub)[field]; ok {
			t.Fatalf("public exposed %s", field)
		}
	}
	in.ExpectedVersion = 2
	expect(t, request(h, "PUT", adminPath, in, root, ""), 200)
	if data(t, request(h, "GET", adminPath, nil, root, ""))["published_at"] != stamp {
		t.Fatal("edit reset publication time")
	}
	c.Active, c.ExpectedVersion = false, 1
	expect(t, request(h, "PUT", categoryPath, c, root, ""), 200)
	expect(t, request(h, "GET", publicPath, nil, "", ""), 404)
	if len(listData(t, request(h, "GET", "/api/v1/content-categories?content_type=news", nil, "", ""))) != 0 {
		t.Fatal("hidden category visible")
	}
	in.ExpectedVersion = 3
	expect(t, request(h, "PUT", adminPath, in, root, ""), 409)
	c.Active, c.ExpectedVersion = true, 2
	expect(t, request(h, "PUT", categoryPath, c, root, ""), 200)
	expect(t, request(h, "GET", publicPath, nil, "", ""), 200)
	in.Status = "archived"
	expect(t, request(h, "PUT", adminPath, in, root, ""), 200)
	expect(t, request(h, "GET", publicPath, nil, "", ""), 404)
	if data(t, request(h, "GET", adminPath, nil, root, ""))["published_at"] != nil {
		t.Fatal("archive retained publication")
	}
	in.Status, in.ExpectedVersion = "published", 4
	future := time.Now().Add(time.Hour)
	in.PublishedAt = &future
	expect(t, request(h, "PUT", adminPath, in, root, ""), 200)
	expect(t, request(h, "GET", publicPath, nil, "", ""), 404)
	if data(t, request(h, "GET", adminPath, nil, root, ""))["scheduled"] != true {
		t.Fatal("missing scheduled status")
	}
	in.ExpectedVersion = 5
	past := time.Now().Add(-time.Minute)
	in.PublishedAt = &past
	expect(t, request(h, "PUT", adminPath, in, root, ""), 200)
	expect(t, request(h, "GET", publicPath, nil, "", ""), 200)
	in.ExpectedVersion = 6
	in.ContentType = "knowledge"
	expect(t, request(h, "PUT", adminPath, in, root, ""), 409)
	in.ContentType, in.AccessLevel = "news", "login"
	expect(t, request(h, "PUT", adminPath, in, root, ""), 409)
	in.AccessLevel, in.Body = "public", strings.Repeat("字", 20001)
	expect(t, request(h, "PUT", adminPath, in, root, ""), 409)
	c.ExpectedVersion, c.ContentType = 3, "knowledge"
	expect(t, request(h, "PUT", categoryPath, c, root, ""), 409)
	expect(t, request(h, "DELETE", adminPath, map[string]int{"expected_version": 5}, root, ""), 409)
	expect(t, request(h, "DELETE", adminPath, map[string]int{"expected_version": 6}, root, ""), 200)
	expect(t, request(h, "GET", publicPath, nil, "", ""), 404)
	expect(t, request(h, "DELETE", categoryPath, map[string]int{"expected_version": 3}, root, ""), 200)
}

func TestContentAccessSearchPaginationAndCredentials(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "access-root", "super_admin")
	uid, customer := user(t, db, h, "knowledge-customer", "customer")
	_, other := user(t, db, h, "knowledge-other", "customer")
	cid := contentCategoryID(t, h, root, categoryInput("knowledge", "access-category"))
	ids := []int64{}
	for _, level := range []string{"public", "login", "active_service"} {
		in := articleInput("knowledge", "access-"+strings.ReplaceAll(level, "_", "-"), "published")
		in.AccessLevel, in.CategoryID = level, &cid
		ids = append(ids, contentID(t, h, root, in))
	}
	check := func(cookie, bearer string, count int) {
		t.Helper()
		path := fmt.Sprintf("/api/v1/knowledgebase?q=共同搜索关键词&category_id=%d&limit=1&offset=1", cid)
		w := request(h, "GET", path, nil, cookie, "")
		if bearer != "" {
			w = requestBearer(h, "GET", path, nil, bearer)
		}
		expect(t, w, 200)
		d := data(t, w)
		wantItems := 1
		if count <= 1 {
			wantItems = 0
		}
		if d["total"] != float64(count) || len(d["items"].([]any)) != wantItems {
			t.Fatalf("visibility pagination: %s", w.Body.String())
		}
		for i, id := range ids {
			p := fmt.Sprintf("/api/v1/knowledgebase/%d", id)
			detail := request(h, "GET", p, nil, cookie, "")
			if bearer != "" {
				detail = requestBearer(h, "GET", p, nil, bearer)
			}
			want := 200
			if i >= count {
				want = 404
			}
			expect(t, detail, want)
			if want == 404 && strings.Contains(detail.Body.String(), "正文内容") {
				t.Fatal("protected body leaked")
			}
		}
	}
	check("", "", 1)
	check(customer, "", 2)
	check("zjmfgo_session=invalid", "", 1)
	key := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "knowledge"}, customer, "")
	expect(t, key, 201)
	secret := data(t, key)["secret"].(string)
	check("", secret, 2)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE api_keys SET expires_at=now()-interval '1 second' WHERE secret_hash=$1", tokenHash(secret)); err != nil {
		t.Fatal(err)
	}
	check("", secret, 1)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE api_keys SET expires_at=NULL WHERE secret_hash=$1", tokenHash(secret)); err != nil {
		t.Fatal(err)
	}
	var protectedViews int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT views FROM content_articles WHERE id=$1", ids[2]).Scan(&protectedViews); err != nil || protectedViews != 0 {
		t.Fatal("denied detail advanced views", err)
	}
	pid := product(t, db, 2)
	v, item := add(t, h, customer, pid, 1)
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "content-service"), 201)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET status='active' WHERE user_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	check(customer, "", 3)
	check("", secret, 3)
	check(other, "", 2)
	for _, sql := range []string{"UPDATE services SET status='suspended' WHERE user_id=$1", "UPDATE services SET status='active',import_review_required=true WHERE user_id=$1"} {
		if _, err := db.Pool.Exec(t.Context(), sql, uid); err != nil {
			t.Fatal(err)
		}
		check(customer, "", 2)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	check(customer, "", 1)
	check("", secret, 1)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET status='active' WHERE id=$1;", uid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/api-keys/%d", int64(data(t, key)["id"].(float64))), nil, customer, ""), 200)
	check("", secret, 1)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE sessions SET expires_at=now()-interval '1 second' WHERE user_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	check(customer, "", 1)
	expect(t, request(h, "GET", "/api/v1/news?category_id=invalid", nil, "", ""), 400)
	expect(t, request(h, "GET", "/api/v1/content-categories?content_type=invalid", nil, "", ""), 400)
}

func TestContentConcurrencyAndAuditRollback(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "concurrency-root", "super_admin")
	in := articleInput("news", "concurrent-news", "published")
	id := contentID(t, h, root, in)
	path := fmt.Sprintf("/api/v1/admin/content/%d", id)
	in.ExpectedVersion = 1
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { results <- request(h, "PUT", path, in, root, "").Code })
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for status := range results {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("update race", counts)
	}
	c := categoryInput("news", "existing-category")
	cid := contentCategoryID(t, h, root, c)
	cp := fmt.Sprintf("/api/v1/admin/content-categories/%d", cid)
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_content_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'content%' THEN RAISE EXCEPTION 'injected content audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_content_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_content_audit();`); err != nil {
		t.Fatal(err)
	}
	in.ExpectedVersion, in.Title = 2, "Must roll back"
	expect(t, request(h, "PUT", path, in, root, ""), 500)
	expect(t, request(h, "DELETE", path, map[string]int{"expected_version": 2}, root, ""), 500)
	expect(t, request(h, "POST", "/api/v1/admin/content-categories", categoryInput("news", "rollback-category"), root, ""), 500)
	c.ExpectedVersion, c.Name = 1, "Must roll back category"
	expect(t, request(h, "PUT", cp, c, root, ""), 500)
	expect(t, request(h, "DELETE", cp, map[string]int{"expected_version": 1}, root, ""), 500)
	after := data(t, request(h, "GET", path, nil, root, ""))
	if after["title"] == in.Title || after["version"] != float64(2) {
		t.Fatal("audit failure changed article")
	}
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM content_categories WHERE name<>'Must roll back category' AND version=1").Scan(&count); err != nil || count != 1 {
		t.Fatal("audit failure created category", err)
	}
}

func TestContentCategoryAssignmentDeleteRace(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "category-race-root", "super_admin")
	for i := range 5 {
		cid := contentCategoryID(t, h, root, categoryInput("news", fmt.Sprintf("race-category-%d", i)))
		in := articleInput("news", fmt.Sprintf("race-article-%d", i), "draft")
		in.CategoryID = &cid
		codes := make(chan int, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() { <-start; codes <- request(h, "POST", "/api/v1/admin/content", in, root, "").Code })
		wg.Go(func() {
			<-start
			codes <- request(h, "DELETE", fmt.Sprintf("/api/v1/admin/content-categories/%d", cid), map[string]int{"expected_version": 1}, root, "").Code
		})
		close(start)
		wg.Wait()
		close(codes)
		counts := map[int]int{}
		for code := range codes {
			counts[code]++
		}
		if !((counts[201] == 1 && counts[409] == 1) || (counts[200] == 1 && counts[404] == 1)) {
			t.Fatal("category race", counts)
		}
		var orphan int
		if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM content_articles WHERE slug=$1 AND category_id IS NULL", in.Slug).Scan(&orphan); err != nil || orphan != 0 {
			t.Fatal("race silently removed category", err)
		}
	}
}

func TestContentWaitingPermissionRevocation(t *testing.T) {
	db, h := testServer(t)
	id, writer := user(t, db, h, "waiting-writer", "admin")
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108196)"); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		done <- request(h, "POST", "/api/v1/admin/content", articleInput("news", "waiting-news", "draft"), writer, "").Code
	}()
	for {
		var waiting bool
		if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query='SELECT pg_advisory_xact_lock(70108196)')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("writer never reached content lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Direct SQL revocation verifies the fresh check after the content lock.
	// Real role management takes the exclusive 70108190 lock and serializes before/after mutations.
	if _, err = tx.Exec(ctx, "UPDATE admin_roles SET permissions=ARRAY['content.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", id); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-done:
		if status != 403 {
			t.Fatalf("waiting write status=%d", status)
		}
	case <-ctx.Done():
		t.Fatal("writer did not finish")
	}
	var count int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM content_articles").Scan(&count); err != nil || count != 0 {
		t.Fatal("revoked write persisted", err)
	}
}
