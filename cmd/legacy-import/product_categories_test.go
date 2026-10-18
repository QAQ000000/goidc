package main

import (
	"testing"

	"github.com/codex/zjmfgo/internal/legacy"
)

func TestProductCategoryImportSeparateFromNavigationAndReplay(t *testing.T) {
	db := importTestDB(t)
	snapshot := navigationImportFixture()
	snapshot["product_categories"] = []legacy.Record{{"id": "71", "name": "独立商品分类", "order": "4", "hidden": "1", "gid": "0"}}
	snapshot["products"][0]["gid"] = "71"
	im := newImporter(snapshot)
	if err := im.apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	category := importedID(t, db, "product_categories", 71)
	navigation := importedID(t, db, "service_navigation_groups", 71)
	pid := importedID(t, db, "products", 167)
	var categoryID, navigationID int64
	var name string
	var active bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT p.category_id,p.service_group_id,c.name,c.active FROM products p JOIN product_categories c ON c.id=p.category_id WHERE p.id=$1`, pid).Scan(&categoryID, &navigationID, &name, &active); err != nil {
		t.Fatal(err)
	}
	if categoryID != category || navigationID != navigation || name != "独立商品分类" || active {
		t.Fatal("classification and navigation were confused")
	}
	// A repeat import does not overwrite later native management changes.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE product_categories SET name='人工分类名称',version=version+1 WHERE id=$1", category); err != nil {
		t.Fatal(err)
	}
	repeat := newImporter(snapshot)
	if err := repeat.apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	if repeat.report.Replayed["product_categories"] != 1 {
		t.Fatal("category import was not idempotent")
	}
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM product_categories WHERE legacy_source='integration' AND legacy_id=71").Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate mapped category: %d %v", count, err)
	}
}

func TestProductCategoryImportRejectsMissingMapping(t *testing.T) {
	snapshot := importFixture()
	snapshot["products"][0]["gid"] = "999"
	im := newImporter(snapshot)
	if err := im.validate(); err == nil {
		t.Fatal("missing product category silently accepted")
	}
}
