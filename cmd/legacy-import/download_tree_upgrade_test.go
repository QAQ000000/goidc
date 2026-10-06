package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"testing"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/codex/zjmfgo/internal/store"
)

func TestDownloadTreeMigrationPreservesExistingCatalog(t *testing.T) {
	db := emptyImportTestDB(t)
	files, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if name >= "043_" {
			break
		}
		sql, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Pool.Exec(t.Context(), string(sql)); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(sql)
		if _, err = db.Pool.Exec(t.Context(), "INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)", name, hex.EncodeToString(sum[:])); err != nil {
			t.Fatal(err)
		}
	}
	admin := navigationAdmin(t, db)
	var category int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO download_categories(name,description,sort_order,active,version) VALUES('原目录','保留说明',8,false,7) RETURNING id").Scan(&category); err != nil {
		t.Fatal(err)
	}
	_, err = db.SaveDownload(t.Context(), admin, 0, store.DownloadInput{CategoryID: &category, Title: "原文件", SourceType: "local", AccessLevel: "login", Active: true}, &store.DownloadFile{Filename: "原文档.txt", MIME: "text/plain; charset=utf-8", Content: []byte("不能修改原文件内容")})
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := db.Pool.QueryRow(t.Context(), "SELECT jsonb_build_object('categories',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM download_categories c),'files',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM downloads d),'settings',(SELECT to_jsonb(s) FROM download_settings s))::text").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal("repeat migration", err)
	}
	if err = db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT jsonb_build_object('categories',(SELECT jsonb_agg(to_jsonb(c)-'parent_id'-'legacy_source'-'legacy_id' ORDER BY id) FROM download_categories c),'files',(SELECT jsonb_agg(to_jsonb(d)-'legacy_source'-'legacy_id' ORDER BY id) FROM downloads d),'settings',(SELECT to_jsonb(s) FROM download_settings s))::text").Scan(&after); err != nil || before != after {
		t.Fatal("upgrade changed catalog", err)
	}
	var root, visible bool
	if err = db.Pool.QueryRow(t.Context(), "SELECT parent_id IS NULL,download_category_visible(id) FROM download_categories WHERE id=$1", category).Scan(&root, &visible); err != nil || !root || visible {
		t.Fatal("root or hidden state", err)
	}
}
