package httpapi

import (
	"fmt"
	"testing"
)

func TestPublicIndexHidesSecondaryCategoryAfterTaxonomyEdit(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "native-index-root", "super_admin")
	primary := contentCategoryID(t, h, root, categoryInput("news", "index-primary"))
	secondary := contentCategoryID(t, h, root, categoryInput("news", "index-secondary"))
	w := request(h, "POST", "/api/v1/admin/content", map[string]any{
		"content_type": "news", "title": "多分类索引", "slug": "taxonomy-index",
		"body": "公开内容", "status": "published", "access_level": "public",
		"category_id": primary, "category_ids": []int64{primary, secondary},
	}, root, "")
	expect(t, w, 201)
	id := int64(data(t, w)["id"].(float64))
	preview := fmt.Sprintf("/api/v1/public-content/news/%d", id)
	expect(t, request(h, "GET", preview, nil, "", ""), 200)
	if data(t, request(h, "GET", "/api/v1/public-index", nil, "", ""))["total"] != float64(1) {
		t.Fatal("public index missed article")
	}
	category := categoryInput("news", "index-secondary")
	category.Active, category.ExpectedVersion = false, 1
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/content-categories/%d", secondary), category, root, ""), 200)
	for _, cookie := range []string{"", root} {
		expect(t, request(h, "GET", preview, nil, cookie, ""), 404)
		if data(t, request(h, "GET", "/api/v1/public-index", nil, cookie, ""))["total"] != float64(0) {
			t.Fatal("hidden secondary category leaked through index")
		}
	}
}

func TestContentTaxonomyMigrationPreservesExistingArticle(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "native-migration-root", "super_admin")
	category := contentCategoryID(t, h, root, categoryInput("knowledge", "existing-guide"))
	article := articleInput("knowledge", "existing-article", "published")
	article.CategoryID, article.AccessLevel = &category, "login"
	id := contentID(t, h, root, article)
	if _, err := db.Pool.Exec(t.Context(), `
 DELETE FROM schema_migrations WHERE version='074_content_taxonomy.sql';
 DROP TABLE content_article_tags,content_tags,content_article_categories;
 ALTER TABLE content_articles DROP COLUMN sort_order;`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE content_articles SET version=7,views=11 WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var categoryID, version, views, relations int64
	var level, body string
	if err := db.Pool.QueryRow(t.Context(), `SELECT category_id,version,views,access_level,body,(SELECT count(*) FROM content_article_categories ac WHERE ac.article_id=a.id AND ac.category_id=a.category_id) FROM content_articles a WHERE id=$1`, id).Scan(&categoryID, &version, &views, &level, &body, &relations); err != nil || categoryID != category || version != 7 || views != 11 || relations != 1 || level != "login" || body != article.Body {
		t.Fatalf("migration changed existing article: category=%d version=%d views=%d relations=%d level=%s err=%v", categoryID, version, views, relations, level, err)
	}
}
