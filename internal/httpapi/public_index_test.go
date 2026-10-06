package httpapi

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPublicIndexAndPreviewRespectVisibility(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "seo-root", "super_admin")
	pid := product(t, db, 2)
	hiddenProduct := product(t, db, 2)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET active=false WHERE id=$1", hiddenProduct); err != nil {
		t.Fatal(err)
	}
	visible := contentID(t, h, root, articleInput("news", "public-news", "published"))
	knowledge := contentID(t, h, root, articleInput("knowledge", "public-knowledge", "published"))
	for _, status := range []string{"draft", "archived"} {
		contentID(t, h, root, articleInput("news", status+"-news", status))
	}
	future := articleInput("news", "future-news", "published")
	at := time.Now().Add(time.Hour)
	future.PublishedAt = &at
	futureID := contentID(t, h, root, future)
	private := articleInput("knowledge", "private-knowledge", "published")
	private.AccessLevel = "login"
	privateID := contentID(t, h, root, private)
	category := contentCategoryID(t, h, root, categoryInput("news", "hidden-category"))
	in := articleInput("news", "hidden-news", "published")
	in.CategoryID = &category
	hiddenID := contentID(t, h, root, in)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE content_categories SET active=false WHERE id=$1", category); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range []string{"", root} {
		w := request(h, "GET", "/api/v1/public-index", nil, cookie, "")
		expect(t, w, 200)
		d := data(t, w)
		if d["total"].(float64) != 3 || len(d["items"].([]any)) != 3 {
			t.Fatal("index included nonpublic resources", w.Body.String())
		}
		for _, path := range []string{fmt.Sprintf("/products/%d", pid), fmt.Sprintf("/news/%d", visible), fmt.Sprintf("/knowledgebase/%d", knowledge)} {
			if !strings.Contains(w.Body.String(), path) {
				t.Fatal("missing", path)
			}
		}
		for _, entry := range []struct {
			kind string
			id   int64
		}{{"news", futureID}, {"news", hiddenID}, {"knowledge", privateID}} {
			expect(t, request(h, "GET", fmt.Sprintf("/api/v1/public-content/%s/%d", entry.kind, entry.id), nil, cookie, ""), 404)
		}
		expect(t, request(h, "GET", fmt.Sprintf("/api/v1/public-content/news/%d", visible), nil, cookie, ""), 200)
	}
	var views int
	if err := db.Pool.QueryRow(t.Context(), "SELECT views FROM content_articles WHERE id=$1", visible).Scan(&views); err != nil || views != 0 {
		t.Fatal("SEO read advanced views", views, err)
	}
	first := request(h, "GET", "/api/v1/public-index?limit=1&offset=0", nil, "", "")
	second := request(h, "GET", "/api/v1/public-index?limit=1&offset=1", nil, "", "")
	expect(t, first, 200)
	expect(t, second, 200)
	if data(t, first)["items"].([]any)[0].(map[string]any)["path"] == data(t, second)["items"].([]any)[0].(map[string]any)["path"] {
		t.Fatal("duplicate index page")
	}
	for _, q := range []string{"limit=1001", "limit=-1", "offset=-1", "offset=garbage"} {
		expect(t, request(h, "GET", "/api/v1/public-index?"+q, nil, "", ""), 400)
	}
}
