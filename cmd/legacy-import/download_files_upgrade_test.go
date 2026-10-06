package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"testing"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/codex/zjmfgo/internal/store"
)

func TestLegacyDownloadMigrationPreservesNonemptyCatalog(t *testing.T) {
	db := emptyImportTestDB(t)
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "044_" {
			break
		}
		sql, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Pool.Exec(t.Context(), string(sql)); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(sql)
		if _, err = db.Pool.Exec(t.Context(), "INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)", name, hex.EncodeToString(hash[:])); err != nil {
			t.Fatal(err)
		}
	}
	admin := navigationAdmin(t, db)
	var parent, child, product int64
	if err = db.Pool.QueryRow(t.Context(), "INSERT INTO download_categories(name,active,version) VALUES('原目录',false,7) RETURNING id").Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), "INSERT INTO download_categories(name,parent_id) VALUES('原子目录',$1) RETURNING id", parent).Scan(&child); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), "INSERT INTO products(name,slug,price_cents) VALUES('原商品','upgrade-file',10) RETURNING id").Scan(&product); err != nil {
		t.Fatal(err)
	}
	if _, err = db.SaveDownload(t.Context(), admin, 0, store.DownloadInput{CategoryID: &child, Title: "原文件", SourceType: "local", AccessLevel: "product", ProductIDs: []int64{product}, Active: true}, &store.DownloadFile{Filename: "原文档.txt", MIME: "text/plain; charset=utf-8", Content: []byte("完整内容")}); err != nil {
		t.Fatal(err)
	}
	payload := []byte("私有原内容")
	if _, err = db.Pool.Exec(t.Context(), `INSERT INTO customer_files(customer_id,title,remarks,filename,mime_type,size_bytes,sha256,content,uploader_id) VALUES($1,'原专属文件','原备注','合同.txt','text/plain; charset=utf-8',$2,$3,$4,$1)`, admin, len(payload), checksum(payload), payload); err != nil {
		t.Fatal(err)
	}
	query := func(new bool) string {
		suffix := ""
		link := ""
		if new {
			suffix = "-'legacy_source'-'legacy_id'"
			link = "-'id'-'legacy_source'-'legacy_id'"
		}
		return `SELECT jsonb_build_object('categories',(SELECT jsonb_agg(to_jsonb(c)` + suffix + ` ORDER BY id) FROM download_categories c),'files',(SELECT jsonb_agg(to_jsonb(d)` + suffix + ` ORDER BY id) FROM downloads d),'private',(SELECT jsonb_agg(to_jsonb(c)` + suffix + ` ORDER BY id) FROM customer_files c),'links',(SELECT jsonb_agg(to_jsonb(p)` + link + ` ORDER BY download_id,product_id) FROM download_products p),'settings',(SELECT to_jsonb(s) FROM download_settings s))::text`
	}
	var before, after string
	if err = db.Pool.QueryRow(t.Context(), query(false)).Scan(&before); err != nil {
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
	if err = db.Pool.QueryRow(t.Context(), query(true)).Scan(&after); err != nil || before != after {
		t.Fatal("upgrade changed tree, bytes, links or settings", err)
	}
	var positive bool
	if err = db.Pool.QueryRow(t.Context(), "SELECT bool_and(id>0) FROM download_products").Scan(&positive); err != nil || !positive {
		t.Fatal("stable link IDs", err)
	}
}
