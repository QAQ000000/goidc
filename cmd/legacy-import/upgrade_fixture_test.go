package main

import (
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// Upgrade fixtures must write the historical schema directly. Current account
// and storage methods deliberately require tables and columns added later.
func historicalImportAdministrator(t *testing.T, db *store.Store) int64 {
	t.Helper()
	var id int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role)
 VALUES('nav-import-root','nav-import-root@example.test','unused','导航测试管理员','super_admin') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func historicalImportDownload(t *testing.T, db *store.Store, admin, category int64, access string, content []byte) int64 {
	t.Helper()
	var id int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO downloads(category_id,title,active,access_level,source_type,filename,mime_type,size_bytes,sha256,content,author_id)
 VALUES($1,'原文件',true,$2,'local','原文档.txt','text/plain; charset=utf-8',$3,$4,$5,$6) RETURNING id`, category, access, len(content), checksum(content), content, admin).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
