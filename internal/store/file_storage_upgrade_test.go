package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestFileStorageUpgrade166PreservesInlinePayloads(t *testing.T) {
	db := notificationSchema160(t)
	for _, name := range []string{notificationUpgrade161, notificationUpgrade162, "163_notification_lifecycle.sql", "164_certification_provider_runtime.sql", "165_payment_runtime_contract.sql", "166_supplier_configuration_contract.sql"} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	var boundary string
	var objectTable bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT max(version),to_regclass('file_storage_objects') IS NOT NULL FROM schema_migrations`).Scan(&boundary, &objectTable); err != nil || boundary != "166_supplier_configuration_contract.sql" || objectTable {
		t.Fatal("fixture must originate at actual schema 166", boundary, objectTable, err)
	}
	actor, customer := topUpFixture(t, db)
	ticket, article, application := fileStorageOwners(t, db, actor, customer)
	data := []byte("legacy inline payload remains private and unchanged")
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO downloads(title,access_level,source_type,filename,mime_type,size_bytes,sha256,content,author_id,legacy_source,legacy_id) VALUES('Legacy download','public','local','legacy.txt','text/plain',$1,$2,$3,$4,'legacy-fixture',101)`, []any{len(data), hash, data, actor}},
		{`INSERT INTO downloads(title,access_level,source_type,remote_url,author_id) VALUES('Legacy remote','public','remote','https://download.example.test/file',$1)`, []any{actor}},
		{`INSERT INTO customer_files(customer_id,title,filename,mime_type,size_bytes,sha256,content,uploader_id,legacy_source,legacy_id) VALUES($1,'Legacy private file','legacy.txt','text/plain',$2,$3,$4,$5,'legacy-fixture',102)`, []any{customer, len(data), hash, data, actor}},
		{`INSERT INTO ticket_attachments(ticket_id,uploader_id,filename,mime_type,size_bytes,sha256,content) VALUES($1,$2,'legacy.txt','text/plain',$3,$4,$5)`, []any{ticket, actor, len(data), hash, data}},
		{`INSERT INTO content_images(article_id,uploader_id,filename,mime_type,size_bytes,content) VALUES($1,$2,'legacy.png','image/png',$3,$4)`, []any{article, actor, len(data), data}},
		{`INSERT INTO invoice_application_documents(application_id,filename,mime_type,size_bytes,sha256,content,uploader_id) VALUES($1,'legacy.pdf','application/pdf',$2,$3,$4,$5)`, []any{application, len(data), hash, data, actor}},
	} {
		if _, err := db.Pool.Exec(t.Context(), seed.query, seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() string {
		var raw string
		if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'downloads',(SELECT jsonb_agg(to_jsonb(f)-'storage_object_id' ORDER BY id) FROM downloads f),
 'customer_files',(SELECT jsonb_agg(to_jsonb(f)-'storage_object_id' ORDER BY id) FROM customer_files f),
 'ticket_attachments',(SELECT jsonb_agg(to_jsonb(f)-'storage_object_id' ORDER BY id) FROM ticket_attachments f),
 'content_images',(SELECT jsonb_agg(to_jsonb(f)-'storage_object_id' ORDER BY id) FROM content_images f),
 'invoice_documents',(SELECT jsonb_agg(to_jsonb(f)-'storage_object_id' ORDER BY id) FROM invoice_application_documents f),
 'tickets',(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM tickets f),
 'articles',(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM content_articles f),
 'invoice_applications',(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM invoice_applications f)
 )::text`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	if err := db.CheckSchema(t.Context()); err == nil {
		t.Fatal("new file runtime accepted schema 166 before migration")
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if snapshot() != before {
		t.Fatal("file storage migration changed old inline bytes, metadata or legacy import identity")
	}
	if err := db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"downloads", "customer_files", "ticket_attachments", "content_images", "invoice_application_documents"} {
		var inline []byte
		var objectID *string
		if err := db.Pool.QueryRow(t.Context(), "SELECT content,storage_object_id FROM "+kind+" WHERE content IS NOT NULL ORDER BY id LIMIT 1").Scan(&inline, &objectID); err != nil || objectID != nil || !bytes.Equal(inline, data) {
			t.Fatal("legacy payload was moved or changed", kind, err)
		}
		if content, err := db.ReadFileContent(t.Context(), nil, kind, inline, objectID); err != nil || !bytes.Equal(content, data) {
			t.Fatal("legacy inline read no longer works", kind, err)
		}
	}
	var count int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM file_storage_objects`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration unexpectedly copied inline payloads to external storage", count, err)
	}
	if err := db.Migrate(t.Context()); err != nil || snapshot() != before {
		t.Fatal("repeat migration changed inline facts", err)
	}
	assertNotificationUpgradeChecksums(t, db)
}
