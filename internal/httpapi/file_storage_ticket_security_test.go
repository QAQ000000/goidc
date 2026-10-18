package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/filestorage"
	"github.com/codex/zjmfgo/internal/store"
)

func ticketStorageFixture(t *testing.T) (*store.Store, http.Handler, string, string, string, int64, int64, int64, int64, string) {
	t.Helper()
	dir := t.TempDir()
	profile := filestorage.Profile{ID: "ticket_disk", Implementation: "local", Values: map[string]any{"root": dir}}
	db, h := testServerConfig(t, func(cfg *config.Config) { cfg.FileStorage = fileStorageManager(t, profile.ID, profile) })
	_, root := user(t, db, h, "ticket-storage-root", "super_admin")
	owner, customer := user(t, db, h, "ticket-storage-owner", "customer")
	staffID, staff := user(t, db, h, "ticket-storage-staff", "admin")
	dept := request(h, "POST", "/api/v1/admin/ticket-departments", map[string]string{"name": "Storage support", "slug": "storage-support"}, root, "")
	expect(t, dept, 201)
	department := int64(data(t, dept)["id"].(float64))
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET ticket_scope='departments' WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)", staffID, department); err != nil {
		t.Fatal(err)
	}
	ticket := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "Storage authority", "body": "Private", "priority": "normal", "department_id": department}, customer, "")
	expect(t, ticket, 201)
	ticketID := int64(data(t, ticket)["id"].(float64))
	return db, h, root, customer, staff, owner, staffID, department, ticketID, dir
}

func TestFileStorageTicketUploadRechecksAuthorityAfterPut(t *testing.T) {
	for _, scenario := range []string{"disabled_customer", "disabled_admin", "manage_revoked", "role_revoked", "department_revoked", "customer_department_hidden", "session_revoked"} {
		t.Run(scenario, func(t *testing.T) {
			db, h, _, customer, staff, owner, staffID, department, ticketID, dir := ticketStorageFixture(t)
			actor, actorID := staff, staffID
			if scenario == "disabled_customer" || scenario == "customer_department_hidden" || scenario == "session_revoked" {
				actor, actorID = customer, owner
			}
			// Gate the independently committed ready transition after local Put.
			// This adds no production hook and proves bytes exist before revocation.
			if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION gate_ticket_storage_ready() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='ready' AND OLD.state='uncertain' THEN PERFORM pg_advisory_xact_lock(704811071); END IF; RETURN NEW; END $$; CREATE TRIGGER gate_ticket_storage_ready BEFORE UPDATE ON file_storage_objects FOR EACH ROW EXECUTE FUNCTION gate_ticket_storage_ready()`); err != nil {
				t.Fatal(err)
			}
			gate, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(704811071)"); err != nil {
				t.Fatal(err)
			}
			payload := []byte("uploaded before authority revocation\n")
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- multipartRequest(h, fmt.Sprintf("/api/v1/tickets/%d/attachments", ticketID), actor, "proof.txt", payload)
			}()
			waitQueryLock(t, db, `UPDATE file_storage_objects SET state='ready',updated_at=clock_timestamp() WHERE id=$1 AND state='uncertain'`)
			var objectID, state string
			if err = db.Pool.QueryRow(t.Context(), "SELECT id,state FROM file_storage_objects WHERE kind='ticket_attachments'").Scan(&objectID, &state); err != nil || state != "uncertain" {
				t.Fatal("request did not reach the durable post-Put boundary", state, err)
			}
			actual, err := os.ReadFile(filepath.Join(dir, objectID))
			if err != nil || !bytes.Equal(actual, payload) {
				t.Fatal("local Put had not completed before revocation", err)
			}
			want := 404
			switch scenario {
			case "disabled_customer", "disabled_admin":
				_, err = gate.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", actorID)
				want = 401
			case "manage_revoked":
				_, err = gate.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['tickets.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", actorID)
			case "role_revoked":
				_, err = gate.Exec(t.Context(), "UPDATE users SET role='customer',admin_role_id=NULL WHERE id=$1", actorID)
			case "department_revoked":
				_, err = gate.Exec(t.Context(), "DELETE FROM admin_ticket_departments WHERE user_id=$1 AND department_id=$2", actorID, department)
			case "customer_department_hidden":
				_, err = gate.Exec(t.Context(), "UPDATE ticket_departments SET customer_visible=false WHERE id=$1", department)
			case "session_revoked":
				_, err = gate.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(strings.TrimPrefix(actor, "zjmfgo_session=")))
				want = 401
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case out := <-done:
				expect(t, out, want)
			case <-time.After(7 * time.Second):
				t.Fatal("upload stalled after the authority gate was released")
			}
			var files, audits int
			if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM ticket_attachments WHERE ticket_id=$1", ticketID).Scan(&files); err != nil || files != 0 {
				t.Fatal("revoked upload attached a business file", files, err)
			}
			if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='ticket.attachment.uploaded'").Scan(&audits); err != nil || audits != 0 {
				t.Fatal("revoked upload committed an audit event", audits, err)
			}
			var collectable bool
			if err = db.Pool.QueryRow(t.Context(), "SELECT state='ready' AND gc_after IS NOT NULL AND NOT file_storage_object_referenced(id) FROM file_storage_objects WHERE id=$1", objectID).Scan(&collectable); err != nil || !collectable {
				t.Fatal("rejected upload lost its durable orphan journal", err)
			}
		})
	}
}

func waitTicketStorageLock(t *testing.T, db *store.Store, pattern string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err := db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1)", pattern).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("ticket operation did not reach its lock boundary", pattern)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestFileStorageTicketReadMergeUsesParentFirstLocks(t *testing.T) {
	db, h, root, customer, _, _, _, department, canonical, _ := ticketStorageFixture(t)
	created := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "Merge source", "body": "Private", "priority": "normal", "department_id": department}, customer, "")
	expect(t, created, 201)
	source := int64(data(t, created)["id"].(float64))
	upload := multipartRequest(h, fmt.Sprintf("/api/v1/tickets/%d/attachments", source), customer, "merge.txt", []byte("merge-preserved private bytes\n"))
	expect(t, upload, 201)
	attachment := int64(data(t, upload)["id"].(float64))
	parentGate, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer parentGate.Rollback(context.Background())
	if _, err = parentGate.Exec(t.Context(), "SELECT id FROM tickets WHERE id=$1 FOR UPDATE", source); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		readDone <- request(h, "GET", fmt.Sprintf("/api/v1/tickets/%d/attachments/%d", source, attachment), nil, customer, "")
	}()
	waitTicketStorageLock(t, db, "SELECT t.id FROM ticket_attachments%FOR SHARE OF t")
	attachmentGate, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer attachmentGate.Rollback(context.Background())
	if _, err = attachmentGate.Exec(t.Context(), "SET LOCAL lock_timeout='1s'"); err != nil {
		t.Fatal(err)
	}
	if _, err = attachmentGate.Exec(t.Context(), "SELECT id FROM ticket_attachments WHERE id=$1 FOR UPDATE", attachment); err != nil {
		t.Fatal("reader locked the attachment before its parent", err)
	}
	if err = parentGate.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitTicketStorageLock(t, db, "SELECT a.filename,a.mime_type,a.size_bytes,a.content,a.storage_object_id%FOR SHARE OF a")
	mergeDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		mergeDone <- request(h, "POST", "/api/v1/admin/tickets/merge", map[string]any{"ticket_ids": []int64{canonical, source}}, root, "")
	}()
	waitTicketStorageLock(t, db, "SELECT id,user_id,merged_ticket_id,department_id FROM tickets%FOR UPDATE")
	if err = attachmentGate.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, pending := range []struct {
		name   string
		result <-chan *httptest.ResponseRecorder
	}{{"read", readDone}, {"merge", mergeDone}} {
		select {
		case out := <-pending.result:
			expect(t, out, 200)
			if pending.name == "read" && out.Body.String() != "merge-preserved private bytes\n" {
				t.Fatal("concurrent merge changed the reader's bytes")
			}
		case <-time.After(7 * time.Second):
			t.Fatal("read/merge lock order stalled", pending.name)
		}
	}
	var parent int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT ticket_id FROM ticket_attachments WHERE id=$1", attachment).Scan(&parent); err != nil || parent != canonical {
		t.Fatal("merge did not preserve the attachment under the canonical ticket", parent, err)
	}
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/tickets/%d/attachments/%d", source, attachment), nil, customer, ""), 404)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/tickets/%d/attachments/%d", canonical, attachment), nil, customer, ""), 200)
}
