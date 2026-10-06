package httpapi

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func contentTagID(t *testing.T, h http.Handler, cookie, kind, slug string) int64 {
	t.Helper()
	w := request(h, "POST", "/api/v1/admin/content-tags", store.ContentTagInput{ContentType: kind, Name: slug, Slug: slug, Active: true}, cookie, "")
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}

func TestContentTaxonomyVisibilityValidationAndCounts(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "taxonomy-root", "super_admin")
	_, customer := user(t, db, h, "taxonomy-customer", "customer")
	a := contentCategoryID(t, h, root, categoryInput("knowledge", "taxonomy-a"))
	b := contentCategoryID(t, h, root, categoryInput("knowledge", "taxonomy-b"))
	other := contentCategoryID(t, h, root, categoryInput("news", "taxonomy-news"))
	tag := contentTagID(t, h, root, "knowledge", "taxonomy-tag")
	wrongTag := contentTagID(t, h, root, "news", "taxonomy-news-tag")
	in := articleInput("knowledge", "taxonomy-article", "published")
	sortOrder := 12
	in.CategoryID, in.CategoryIDs, in.TagIDs, in.SortOrder = &a, []int64{a, b}, []int64{tag}, &sortOrder
	for _, bad := range [][]int64{{a, a}, {a, other}} {
		invalid := in
		invalid.CategoryIDs = bad
		expect(t, request(h, "POST", "/api/v1/admin/content", invalid, root, ""), 409)
	}
	invalid := in
	invalid.CategoryIDs = []int64{a, 999999}
	expect(t, request(h, "POST", "/api/v1/admin/content", invalid, root, ""), 404)
	invalid = in
	invalid.TagIDs = []int64{tag, wrongTag}
	expect(t, request(h, "POST", "/api/v1/admin/content", invalid, root, ""), 409)
	invalid.TagIDs = []int64{tag, tag}
	expect(t, request(h, "POST", "/api/v1/admin/content", invalid, root, ""), 409)
	invalid.TagIDs = []int64{999999}
	expect(t, request(h, "POST", "/api/v1/admin/content", invalid, root, ""), 404)
	id := contentID(t, h, root, in)
	adminPath := fmt.Sprintf("/api/v1/admin/content/%d", id)
	publicPath := fmt.Sprintf("/api/v1/knowledgebase/%d", id)
	legacy := in
	legacy.CategoryIDs, legacy.TagIDs, legacy.SortOrder, legacy.ExpectedVersion = nil, nil, nil, 1
	expect(t, request(h, "PUT", adminPath, legacy, root, ""), 200)
	stored := data(t, request(h, "GET", adminPath, nil, root, ""))
	if len(stored["categories"].([]any)) != 2 || len(stored["tags"].([]any)) != 1 || stored["sort_order"] != float64(12) {
		t.Fatal("legacy update removed taxonomy", stored)
	}
	article := data(t, request(h, "GET", publicPath, nil, "", ""))
	if article["sort_order"] != float64(12) || len(article["categories"].([]any)) != 2 || len(article["tags"].([]any)) != 1 {
		t.Fatal("taxonomy detail", article)
	}
	if data(t, request(h, "GET", fmt.Sprintf("/api/v1/knowledgebase?category_id=%d&tag_id=%d", b, tag), nil, "", ""))["total"] != float64(1) {
		t.Fatal("secondary category filter")
	}
	if listData(t, request(h, "GET", "/api/v1/admin/content-categories?content_type=knowledge", nil, root, ""))[1]["article_count"] != float64(1) {
		t.Fatal("secondary category count")
	}
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/content-categories/%d", b), map[string]int{"expected_version": 1}, root, ""), 409)
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/content-tags/%d", tag), map[string]int{"expected_version": 1}, root, ""), 409)
	if len(listData(t, request(h, "GET", "/api/v1/content-tags?content_type=knowledge", nil, "", ""))) != 1 {
		t.Fatal("visible tag missing")
	}
	// A hidden secondary category hides the whole article, including tags and counts.
	c := categoryInput("knowledge", "taxonomy-b")
	c.Active, c.ExpectedVersion = false, 1
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/content-categories/%d", b), c, root, ""), 200)
	expect(t, request(h, "GET", publicPath, nil, "", ""), 404)
	if data(t, request(h, "GET", "/api/v1/knowledgebase", nil, "", ""))["total"] != float64(0) || len(listData(t, request(h, "GET", "/api/v1/content-tags?content_type=knowledge", nil, "", ""))) != 0 {
		t.Fatal("hidden category leaked article or tag")
	}
	if len(listData(t, request(h, "GET", "/api/v1/content-categories?content_type=knowledge", nil, "", ""))) != 0 {
		t.Fatal("hidden article leaked category")
	}
	in.ExpectedVersion = 2
	expect(t, request(h, "PUT", adminPath, in, root, ""), 409)
	c.Active, c.ExpectedVersion = true, 2
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/content-categories/%d", b), c, root, ""), 200)
	// Login-only articles never affect anonymous tag counts.
	login := articleInput("knowledge", "taxonomy-login", "published")
	login.AccessLevel, login.TagIDs = "login", []int64{tag}
	contentID(t, h, root, login)
	future := articleInput("knowledge", "taxonomy-future", "published")
	future.TagIDs = []int64{tag}
	stamp := time.Now().Add(time.Hour)
	future.PublishedAt = &stamp
	contentID(t, h, root, future)
	draft := articleInput("knowledge", "taxonomy-draft", "draft")
	draft.TagIDs = []int64{tag}
	contentID(t, h, root, draft)
	archived := articleInput("knowledge", "taxonomy-archived", "archived")
	archived.TagIDs = []int64{tag}
	contentID(t, h, root, archived)
	publicTags := listData(t, request(h, "GET", "/api/v1/content-tags?content_type=knowledge", nil, "", ""))
	loginTags := listData(t, request(h, "GET", "/api/v1/content-tags?content_type=knowledge", nil, customer, ""))
	if publicTags[0]["article_count"] != float64(1) || loginTags[0]["article_count"] != float64(2) {
		t.Fatal("tag counts leaked inaccessible articles", publicTags, loginTags)
	}
	// Hidden tags stay assigned for editors but disappear from public metadata.
	tagInput := store.ContentTagInput{ContentType: "knowledge", Name: "taxonomy-tag", Slug: "taxonomy-tag", Active: false, ExpectedVersion: 1}
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/content-tags/%d", tag), tagInput, root, ""), 200)
	if len(listData(t, request(h, "GET", "/api/v1/content-tags?content_type=knowledge", nil, "", ""))) != 0 || len(data(t, request(h, "GET", publicPath, nil, "", ""))["tags"].([]any)) != 0 {
		t.Fatal("hidden tag visible")
	}
}

func TestContentTagPermissionConcurrencyAndAudit(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "tag-root", "super_admin")
	viewerID, viewer := user(t, db, h, "tag-viewer", "admin")
	contentRole(t, db, viewerID, "content.view")
	id := contentTagID(t, h, root, "news", "tag-race")
	path := fmt.Sprintf("/api/v1/admin/content-tags/%d", id)
	expect(t, request(h, "GET", "/api/v1/admin/content-tags", nil, viewer, ""), 200)
	expect(t, request(h, "PUT", path, store.ContentTagInput{ContentType: "news", Name: "denied", Slug: "tag-race", Active: true, ExpectedVersion: 1}, viewer, ""), 403)
	input := store.ContentTagInput{ContentType: "news", Name: "updated", Slug: "tag-race", Active: true, ExpectedVersion: 1}
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { results <- request(h, "PUT", path, input, root, "").Code })
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for status := range results {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("tag update race", counts)
	}
	var auditCount int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action IN ('content_tag.created','content_tag.updated') AND entity_id=$1", fmt.Sprint(id)).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatal("tag audit", auditCount, err)
	}
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_tag_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='content_tag.updated' THEN RAISE EXCEPTION 'audit rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_tag_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_tag_audit();`); err != nil {
		t.Fatal(err)
	}
	input.ExpectedVersion, input.Name = 2, "must rollback"
	expect(t, request(h, "PUT", path, input, root, ""), 500)
	tags := listData(t, request(h, "GET", "/api/v1/admin/content-tags?content_type=news", nil, root, ""))
	if tags[0]["name"] != "updated" || tags[0]["version"] != float64(2) {
		t.Fatal("audit failure changed tag", tags)
	}
}
