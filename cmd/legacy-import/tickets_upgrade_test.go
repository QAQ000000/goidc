package main

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/codex/zjmfgo/db/migrations"
	"io/fs"
	"testing"
)

func TestTicketMigrationPreservesExistingThreadsAndAttachments(t *testing.T) {
	db := emptyImportTestDB(t)
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "045_" {
			break
		}
		raw, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Pool.Exec(t.Context(), string(raw)); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(raw)
		if _, err = db.Pool.Exec(t.Context(), "INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)", name, hex.EncodeToString(hash[:])); err != nil {
			t.Fatal(err)
		}
	}
	admin := navigationAdmin(t, db)
	var dept, ticket, reply int64
	if err = db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug,active,version) VALUES('原部门','original-dept',false,8) RETURNING id").Scan(&dept); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), "INSERT INTO tickets(ticket_no,user_id,department_id,subject,body,status,version) VALUES('ORIGINAL',$1,$2,'原主题','原正文','pending',9) RETURNING id", admin, dept).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_replies(ticket_id,author_id,body) VALUES($1,$2,'原回复') RETURNING id", ticket, admin).Scan(&reply); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), "INSERT INTO ticket_notes(ticket_id,author_id,body) VALUES($1,$2,'原备注')", ticket, admin); err != nil {
		t.Fatal(err)
	}
	payload := []byte("原附件字节\n")
	if _, err = db.Pool.Exec(t.Context(), "INSERT INTO ticket_attachments(ticket_id,uploader_id,filename,mime_type,size_bytes,sha256,content) VALUES($1,$2,'原附件.txt','text/plain; charset=utf-8',$3,$4,$5)", ticket, admin, len(payload), checksum(payload), payload); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), "INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)", admin, dept); err != nil {
		t.Fatal(err)
	}
	query := func(new bool) string {
		common, departments, tickets, attachments := "", "", "", ""
		if new {
			common = "-'legacy_source'-'legacy_id'"
			departments = common + "-'customer_visible'"
			tickets = common + "-'service_id'-'legacy_status_label'-'legacy_ticket_no'"
			attachments = common + "-'reply_id'-'note_id'"
		}
		return `SELECT jsonb_build_object('departments',(SELECT jsonb_agg(to_jsonb(d)` + departments + ` ORDER BY id) FROM ticket_departments d),'tickets',(SELECT jsonb_agg(to_jsonb(t)` + tickets + ` ORDER BY id) FROM tickets t),'replies',(SELECT jsonb_agg(to_jsonb(r)` + common + ` ORDER BY id) FROM ticket_replies r),'notes',(SELECT jsonb_agg(to_jsonb(n)` + common + ` ORDER BY id) FROM ticket_notes n),'files',(SELECT jsonb_agg(to_jsonb(a)` + attachments + ` ORDER BY id) FROM ticket_attachments a),'scope',(SELECT jsonb_agg(to_jsonb(s) ORDER BY user_id,department_id) FROM admin_ticket_departments s))::text`
	}
	var before, after string
	if err = db.Pool.QueryRow(t.Context(), query(false)).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), query(true)).Scan(&after); err != nil || before != after {
		t.Fatal("upgrade altered threads, files or scope", err)
	}
	var visible bool
	if err = db.Pool.QueryRow(t.Context(), "SELECT customer_visible FROM ticket_departments WHERE id=$1", dept).Scan(&visible); err != nil || !visible {
		t.Fatal("deactivated department lost historical visibility", err)
	}
}
