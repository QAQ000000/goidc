package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/filestorage"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

func keyedTicketUpload(h http.Handler, path, cookie, filename string, content []byte, keys ...string) *httptest.ResponseRecorder {
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		panic(err)
	}
	if _, err = part.Write(content); err != nil {
		panic(err)
	}
	if err = writer.Close(); err != nil {
		panic(err)
	}
	r := httptest.NewRequest("POST", path+"/attachments", &payload)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("X-Requested-With", "zjmfgo")
	r.Header.Set("Origin", "http://localhost:3088")
	r.Header.Set("Cookie", cookie)
	if strings.Contains(cookie, adminSessionCookie+"=") && !strings.Contains(cookie, customerSessionCookie+"=") {
		r.Header.Set(authPortalHeader, "admin")
	}
	for _, key := range keys {
		r.Header.Add("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func ticketUploadStatus(h http.Handler, path, cookie, key string) *httptest.ResponseRecorder {
	return requestFrom(h, "GET", path+"/attachment-upload", nil, "192.0.2.99:1234", cookie, map[string]string{"Idempotency-Key": key})
}

func ticketUploadAPIClient(t *testing.T, h http.Handler, customer string) (http.Handler, string) {
	t.Helper()
	issued := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "ticket-upload-key"}, customer, "")
	expect(t, issued, 201)
	secret := data(t, issued)["secret"].(string)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+secret)
		h.ServeHTTP(w, r)
	}), secret
}

func makeUploadTicket(t *testing.T, h http.Handler, cookie, subject string) (int64, string) {
	t.Helper()
	created := request(h, "POST", "/api/v1/tickets", map[string]string{"subject": subject, "body": "private upload", "priority": "normal"}, cookie, "")
	expect(t, created, 201)
	id := int64(data(t, created)["id"].(float64))
	return id, fmt.Sprintf("/api/v1/tickets/%d", id)
}

func expectUploadUnconfirmed(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	expect(t, w, 409)
	if errorData(t, w)["code"] != "attachment_upload_unconfirmed" {
		t.Fatalf("upload did not return its stable uncertainty marker: %s", w.Body.String())
	}
}

func waitUploadBlockedBy(t *testing.T, db *store.Store, blocker uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err := db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity a WHERE datname=current_database() AND $1::integer=ANY(pg_blocking_pids(a.pid)))", blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("upload did not wait on its own fixture's transaction")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestTicketAttachmentUploadIdempotencyReplayAndScope(t *testing.T) {
	db, h, _, customer, _, owner, _, _, ticket, dir := ticketStorageFixture(t)
	_, other := user(t, db, h, "keyed-upload-other", "customer")
	_, otherPath := makeUploadTicket(t, h, other, "Other owner's upload")
	_, secondPath := makeUploadTicket(t, h, customer, "Another own ticket")
	path := fmt.Sprintf("/api/v1/tickets/%d", ticket)
	payload := []byte("one immutable attachment\n")
	key := "attachment-replay-001"
	first := keyedTicketUpload(h, path, customer, "proof.txt", payload, key)
	expect(t, first, 201)
	original := data(t, first)
	if original["state"] != "confirmed" || original["replayed"] != false {
		t.Fatal("first upload omitted confirmation metadata")
	}
	replay := keyedTicketUpload(h, path, customer, "proof.txt", payload, key)
	expect(t, replay, 200)
	if data(t, replay)["id"] != original["id"] || data(t, replay)["replayed"] != true {
		t.Fatal("confirmed replay created or returned another attachment")
	}
	status := ticketUploadStatus(h, path, customer, key)
	expect(t, status, 200)
	if status.Header().Get("Cache-Control") != "private, no-store" || data(t, status)["state"] != "confirmed" || data(t, status)["attachment"].(map[string]any)["id"] != original["id"] {
		t.Fatal("result query did not return the immutable private receipt")
	}
	var objectID string
	var files, audits, intents, results int
	if err := db.Pool.QueryRow(t.Context(), `SELECT storage_object_id FROM ticket_attachments WHERE ticket_id=$1`, ticket).Scan(&objectID); err != nil {
		t.Fatal(err)
	}
	for _, out := range []*httptest.ResponseRecorder{first, replay, status} {
		for _, private := range []string{objectID, dir, "object_id", "object_key", "storage_object_id", "profile_id"} {
			if strings.Contains(out.Body.String(), private) {
				t.Fatal("upload result exposed private storage metadata", private)
			}
		}
	}
	for _, changed := range []struct {
		path, name string
		payload    []byte
	}{{path, "renamed.txt", payload}, {path, "proof.txt", []byte("different bytes")}, {secondPath, "proof.txt", payload}} {
		out := keyedTicketUpload(h, changed.path, customer, changed.name, changed.payload, key)
		expect(t, out, 409)
		if errorData(t, out)["code"] != "idempotency_conflict" {
			t.Fatal("same key accepted a different normalized payload", out.Body.String())
		}
	}
	expect(t, ticketUploadStatus(h, path, other, key), 404)
	expect(t, ticketUploadStatus(h, secondPath, customer, key), 404)
	expect(t, ticketUploadStatus(h, otherPath, other, key), 404)
	if err := db.Pool.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM ticket_attachments WHERE ticket_id=$1),
 (SELECT count(*) FROM audit_events WHERE action='ticket.attachment.uploaded'),
 (SELECT count(*) FROM mutation_operations WHERE actor_id=$2 AND scope='ticket:attachment-upload-intent'),
 (SELECT count(*) FROM mutation_operations WHERE actor_id=$2 AND scope='ticket:attachment-upload')`, ticket, owner).Scan(&files, &audits, &intents, &results); err != nil || files != 1 || audits != 1 || intents != 1 || results != 1 {
		t.Fatal("upload replay changed atomic business facts", files, audits, intents, results, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("upload replay wrote duplicate external objects", len(entries), err)
	}
	// The same opaque key belongs to each actor's independent key domain.
	expect(t, keyedTicketUpload(h, otherPath, other, "proof.txt", payload, key), 201)
}

func TestTicketAttachmentUploadConcurrentSameKey(t *testing.T) {
	db, h, _, customer, _, _, _, _, ticket, dir := ticketStorageFixture(t)
	path := fmt.Sprintf("/api/v1/tickets/%d", ticket)
	const callers = 8
	start := make(chan struct{})
	out := make(chan *httptest.ResponseRecorder, callers)
	for range callers {
		go func() {
			<-start
			out <- keyedTicketUpload(h, path, customer, "concurrent.txt", []byte("single keyed write\n"), "concurrent-upload-001")
		}()
	}
	close(start)
	created := 0
	for range callers {
		select {
		case response := <-out:
			switch response.Code {
			case 201:
				created++
			case 200:
				if data(t, response)["replayed"] != true {
					t.Fatal("concurrent 200 did not replay the committed receipt")
				}
			case 409:
				expectUploadUnconfirmed(t, response)
			default:
				t.Fatalf("unexpected concurrent response: %d %s", response.Code, response.Body.String())
			}
		case <-time.After(10 * time.Second):
			t.Fatal("same-key uploads deadlocked")
		}
	}
	var files, audits, objects int
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ticket_attachments WHERE ticket_id=$1),(SELECT count(*) FROM audit_events WHERE action='ticket.attachment.uploaded'),(SELECT count(*) FROM file_storage_objects WHERE kind='ticket_attachments')`, ticket).Scan(&files, &audits, &objects); err != nil || created != 1 || files != 1 || audits != 1 || objects != 1 {
		t.Fatal("concurrent key was executed more than once", created, files, audits, objects, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("concurrent upload duplicated external storage", len(entries), err)
	}
	expect(t, ticketUploadStatus(h, path, customer, "concurrent-upload-001"), 200)
}

func TestTicketAttachmentUploadHeadersAndLegacyCompatibility(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "upload-header-owner", "customer")
	ticket, path := makeUploadTicket(t, h, customer, "Header semantics")
	for _, keys := range [][]string{{""}, {"   "}, {"short"}, {strings.Repeat("中", 4)}, {strings.Repeat("x", 129)}, {"repeated-key-001", "repeated-key-001"}} {
		expect(t, keyedTicketUpload(h, path, customer, "header.txt", []byte("valid bytes"), keys...), 400)
	}
	expect(t, ticketUploadStatus(h, path, customer, ""), 400)
	expect(t, request(h, "GET", path+"/attachment-upload?key=url-is-not-allowed", nil, customer, ""), 400)
	expect(t, ticketUploadStatus(h, path, customer, "never-created-001"), 404)
	// Old clients remain intentionally outside the keyed recovery guarantee.
	for range 2 {
		legacy := keyedTicketUpload(h, path, customer, "legacy.txt", []byte("legacy bytes"))
		expect(t, legacy, 201)
		if data(t, legacy)["state"] != nil {
			t.Fatal("legacy upload unexpectedly acquired a keyed receipt")
		}
	}
	var files, receipts int
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ticket_attachments WHERE ticket_id=$1),(SELECT count(*) FROM mutation_operations WHERE scope LIKE 'ticket:attachment-upload%')`, ticket).Scan(&files, &receipts); err != nil || files != 2 || receipts != 0 {
		t.Fatal("header rejection or compatibility changed persistence", files, receipts, err)
	}
}

func TestTicketAttachmentUploadClosedAndMergedResults(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "upload-close-root", "super_admin")
	_, customer := user(t, db, h, "upload-close-owner", "customer")
	canonical, _ := makeUploadTicket(t, h, customer, "Canonical ticket")
	ticket, path := makeUploadTicket(t, h, customer, "Closing result")
	key := "closed-upload-result-001"
	bytes := []byte("committed before closing")
	expect(t, keyedTicketUpload(h, path, customer, "closed.txt", bytes, key), 201)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE tickets SET status='closed' WHERE id=$1", ticket); err != nil {
		t.Fatal(err)
	}
	expect(t, ticketUploadStatus(h, path, customer, key), 200)
	expect(t, keyedTicketUpload(h, path, customer, "closed.txt", bytes, key), 200)
	closed := keyedTicketUpload(h, path, customer, "new.txt", bytes, "new-upload-after-close")
	expect(t, closed, 409)
	if errorData(t, closed)["code"] != "ticket_closed" {
		t.Fatal("closed ticket admitted a new upload")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE tickets SET status='open' WHERE id=$1", ticket); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/admin/tickets/merge", map[string]any{"ticket_ids": []int64{canonical, ticket}}, root, ""), 200)
	merged := ticketUploadStatus(h, path, customer, key)
	expect(t, merged, 409)
	if errorData(t, merged)["code"] != "ticket_merged" {
		t.Fatal("merged-source result contradicted existing ticket-read behavior")
	}
}

func TestTicketAttachmentUploadAuthorizationIsConsistent(t *testing.T) {
	db, h, _, customer, staff, _, staffID, _, ticket, _ := ticketStorageFixture(t)
	path := fmt.Sprintf("/api/v1/tickets/%d", ticket)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	for _, keys := range [][]string{nil, {"foreign-customer-upload-001"}} {
		expect(t, keyedTicketUpload(h, path, staff, "scope.txt", []byte("forbidden"), keys...), 404)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='all' WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	key := "staff-readable-result-001"
	payload := []byte("staff-managed attachment")
	expect(t, keyedTicketUpload(h, path, staff, "managed.txt", payload, key), 201)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['tickets.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	// Result reads need current read authority; replaying POST needs management.
	expect(t, ticketUploadStatus(h, path, staff, key), 200)
	expect(t, keyedTicketUpload(h, path, staff, "managed.txt", payload, key), 404)
	expect(t, ticketUploadStatus(h, path, customer, key), 404)
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM admin_ticket_departments WHERE user_id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, ticketUploadStatus(h, path, staff, key), 404)
}

type ticketObjectService struct {
	mu      sync.Mutex
	methods map[string]int
	objects map[string][]byte
	unknown bool
}

func (o *ticketObjectService) serve(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.methods[r.Method]++
	if r.Method != http.MethodPut {
		w.WriteHeader(500)
		return
	}
	content, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(400)
		return
	}
	o.objects[r.URL.Path] = content
	if o.unknown {
		// The service has durably accepted bytes but loses the response.
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	w.WriteHeader(200)
}

func TestTicketAttachmentUploadUnconfirmedStorageAndRollback(t *testing.T) {
	for _, scenario := range []string{"inline_audit_rollback", "external_audit_rollback", "unknown_put"} {
		t.Run(scenario, func(t *testing.T) {
			objects := &ticketObjectService{methods: map[string]int{}, objects: map[string][]byte{}, unknown: scenario == "unknown_put"}
			mock := httptest.NewServer(http.HandlerFunc(objects.serve))
			defer mock.Close()
			db, h := testServerConfig(t, func(cfg *config.Config) {
				if scenario != "inline_audit_rollback" {
					profile := filestorage.Profile{ID: "ticket_mock", Implementation: "s3", Values: map[string]any{"endpoint": mock.URL, "region": "us-east-1", "bucket": "ticket-private", "prefix": "uploads"}, Secrets: map[string]string{"access_key": "TESTKEY", "secret_key": "isolated-object-secret"}}
					cfg.FileStorage = fileStorageManager(t, profile.ID, profile)
				}
			})
			_, customer := user(t, db, h, "uncertain-upload-owner", "customer")
			ticket, path := makeUploadTicket(t, h, customer, "Uncertain upload")
			if scenario != "unknown_put" {
				if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_upload_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='ticket.attachment.uploaded' THEN RAISE EXCEPTION 'isolated upload audit rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_upload_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_upload_audit()`); err != nil {
					t.Fatal(err)
				}
			}
			key := "unconfirmed-upload-001"
			payload := []byte("externally stored but not confirmed")
			expectUploadUnconfirmed(t, keyedTicketUpload(h, path, customer, "unknown.txt", payload, key))
			status := ticketUploadStatus(h, path, customer, key)
			expect(t, status, 200)
			if data(t, status)["state"] != "unconfirmed" || data(t, status)["attachment"] != nil {
				t.Fatal("missing atomic business receipt was reported as confirmed")
			}
			expectUploadUnconfirmed(t, keyedTicketUpload(h, path, customer, "unknown.txt", payload, key))
			var files, finalReceipts, intents int
			if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ticket_attachments WHERE ticket_id=$1),(SELECT count(*) FROM mutation_operations WHERE scope='ticket:attachment-upload'),(SELECT count(*) FROM mutation_operations WHERE scope='ticket:attachment-upload-intent')`, ticket).Scan(&files, &finalReceipts, &intents); err != nil || files != 0 || finalReceipts != 0 || intents != 1 {
				t.Fatal("uncertain upload lost its intent or fabricated confirmation", files, finalReceipts, intents, err)
			}
			if scenario != "inline_audit_rollback" {
				var state string
				var unbound bool
				if err := db.Pool.QueryRow(t.Context(), "SELECT state,NOT file_storage_object_referenced(id) FROM file_storage_objects WHERE kind='ticket_attachments'").Scan(&state, &unbound); err != nil || !unbound || (scenario == "unknown_put" && state != "uncertain") || (scenario == "external_audit_rollback" && state != "ready") {
					t.Fatal("failed upload lost its durable storage journal", state, unbound, err)
				}
				objects.mu.Lock()
				puts, methods, stored := objects.methods[http.MethodPut], len(objects.methods), len(objects.objects)
				objects.mu.Unlock()
				if puts != 1 || methods != 1 || stored != 1 {
					t.Fatal("query or replay retried/touched an uncertain external object", puts, methods, stored)
				}
			}
		})
	}
}

// Drop exactly one backend COMMIT acknowledgement after PostgreSQL has sent
// CommandComplete. The actual transaction is committed, but pgx sees EOF. This
// exercises wire uncertainty without a production fault-injection hook.
type uploadCommitAckReader struct {
	reader  io.Reader
	count   *atomic.Int32
	dropAt  int32
	pending []byte
	dropped bool
}

func (r *uploadCommitAckReader) Read(dst []byte) (int, error) {
	if r.dropped {
		return 0, io.ErrUnexpectedEOF
	}
	if len(dst) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 {
		var header [5]byte
		if _, err := io.ReadFull(r.reader, header[:]); err != nil {
			return 0, err
		}
		size := int(binary.BigEndian.Uint32(header[1:]))
		if size < 4 || size > 64<<20 {
			return 0, errors.New("invalid isolated PostgreSQL message")
		}
		message := make([]byte, size+1)
		copy(message, header[:])
		if _, err := io.ReadFull(r.reader, message[5:]); err != nil {
			return 0, err
		}
		if message[0] == 'C' && string(message[5:]) == "COMMIT\x00" && r.count.Add(1) == r.dropAt {
			r.dropped = true
			return 0, io.ErrUnexpectedEOF
		}
		r.pending = message
	}
	n := copy(dst, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func TestTicketAttachmentUploadCommitUnknownFence(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		dropAt    int32
		confirmed bool
	}{{"intent_commit_ack_lost", 1, false}, {"business_commit_ack_lost", 2, true}} {
		t.Run(scenario.name, func(t *testing.T) {
			db, h, _, customer, _, owner, _, _, ticket, dir := ticketStorageFixture(t)
			var commits atomic.Int32
			cfg := db.Pool.Config().Copy()
			cfg.ConnConfig.BuildFrontend = func(reader io.Reader, writer io.Writer) *pgproto3.Frontend {
				return pgproto3.NewFrontend(&uploadCommitAckReader{reader: reader, count: &commits, dropAt: scenario.dropAt}, writer)
			}
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			uploads := &store.Store{Pool: pool, FileStorage: db.FileStorage}
			defer uploads.Close()
			ctx := store.WithStorageCredential(t.Context(), tokenHash(cookieValue(customer)), false)
			payload := []byte("committed despite response loss\n")
			key := "commit-unknown-upload-001"
			_, err = uploads.UploadTicketAccountAttachmentWithKey(ctx, owner, ticket, "commit.txt", "text/plain; charset=utf-8", fmt.Sprintf("%x", sha256.Sum256(payload)), payload, key)
			var uncertain *store.TicketAccountError
			if !errors.As(err, &uncertain) || uncertain.Code != "attachment_upload_unconfirmed" || commits.Load() < scenario.dropAt {
				t.Fatal("lost acknowledgement did not reach the intended commit boundary", err, commits.Load())
			}
			var intents, receipts, files int
			if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM mutation_operations WHERE scope='ticket:attachment-upload-intent'),(SELECT count(*) FROM mutation_operations WHERE scope='ticket:attachment-upload'),(SELECT count(*) FROM ticket_attachments WHERE ticket_id=$1)`, ticket).Scan(&intents, &receipts, &files); err != nil || intents != 1 {
				t.Fatal("wire loss did not leave its already committed intent", intents, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/api/v1/tickets/%d", ticket)
			status := ticketUploadStatus(h, path, customer, key)
			expect(t, status, 200)
			if scenario.confirmed {
				if receipts != 1 || files != 1 || len(entries) != 1 || data(t, status)["state"] != "confirmed" {
					t.Fatal("durable business commit was not recovered by its receipt", receipts, files, len(entries), status.Body.String())
				}
				result, err := db.UploadTicketAccountAttachmentWithKey(ctx, owner, ticket, "commit.txt", "text/plain; charset=utf-8", fmt.Sprintf("%x", sha256.Sum256(payload)), payload, key)
				if err != nil || result["replayed"] != true {
					t.Fatal("business commit acknowledgement loss could not replay", err)
				}
			} else {
				if receipts != 0 || files != 0 || len(entries) != 0 || data(t, status)["state"] != "unconfirmed" {
					t.Fatal("lost intent acknowledgement authorized Put", receipts, files, len(entries), status.Body.String())
				}
				_, err := db.UploadTicketAccountAttachmentWithKey(ctx, owner, ticket, "commit.txt", "text/plain; charset=utf-8", fmt.Sprintf("%x", sha256.Sum256(payload)), payload, key)
				if !errors.As(err, &uncertain) || uncertain.Code != "attachment_upload_unconfirmed" {
					t.Fatal("uncertain intent was retried", err)
				}
			}
		})
	}
}

func TestTicketAttachmentUploadReservationFailureNeverPuts(t *testing.T) {
	db, h, _, customer, _, _, _, _, ticket, dir := ticketStorageFixture(t)
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_upload_intent() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.scope='ticket:attachment-upload-intent' THEN RAISE EXCEPTION 'isolated reservation rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_upload_intent BEFORE INSERT ON mutation_operations FOR EACH ROW EXECUTE FUNCTION reject_upload_intent()`); err != nil {
		t.Fatal(err)
	}
	path, key := fmt.Sprintf("/api/v1/tickets/%d", ticket), "reservation-rollback-001"
	payload := []byte("no external write without a committed intent")
	expect(t, keyedTicketUpload(h, path, customer, "reserve.txt", payload, key), 500)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed upload reservation issued Put", len(entries), err)
	}
	var receipts, objects int
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM mutation_operations WHERE scope LIKE 'ticket:attachment-upload%'),(SELECT count(*) FROM file_storage_objects WHERE kind='ticket_attachments')`).Scan(&receipts, &objects); err != nil || receipts != 0 || objects != 0 {
		t.Fatal("failed reservation left an external journal or receipt", receipts, objects, err)
	}
	expect(t, ticketUploadStatus(h, path, customer, key), 404)
	if _, err := db.Pool.Exec(t.Context(), "DROP TRIGGER reject_upload_intent ON mutation_operations"); err != nil {
		t.Fatal(err)
	}
	// A known rollback left no intent, so the first successful reservation is
	// still allowed. This differs from a durable but unconfirmed intent.
	expect(t, keyedTicketUpload(h, path, customer, "reserve.txt", payload, key), 201)
}

func TestTicketAttachmentUploadRechecksAuthorityAfterPut(t *testing.T) {
	for _, scenario := range []string{"customer_disabled", "session_revoked", "api_key_revoked", "customer_scope_revoked", "department_revoked", "manage_revoked", "delegation_parent_revoked"} {
		t.Run(scenario, func(t *testing.T) {
			db, h, root, customer, staff, owner, staffID, department, ticket, dir := ticketStorageFixture(t)
			actor := staff
			if scenario == "customer_disabled" || scenario == "session_revoked" {
				actor = customer
			}
			if scenario == "delegation_parent_revoked" {
				actor = customerKeyDelegation(t, h, owner, root)
			}
			client, secret := h, ""
			if scenario == "api_key_revoked" {
				client, secret = ticketUploadAPIClient(t, h, customer)
				actor = ""
			}
			if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION gate_keyed_upload_ready() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='ready' AND OLD.state='uncertain' THEN PERFORM pg_advisory_xact_lock(704811072); END IF; RETURN NEW; END $$; CREATE TRIGGER gate_keyed_upload_ready BEFORE UPDATE ON file_storage_objects FOR EACH ROW EXECUTE FUNCTION gate_keyed_upload_ready()`); err != nil {
				t.Fatal(err)
			}
			gate, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(704811072)"); err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/api/v1/tickets/%d", ticket)
			payload := []byte("private bytes before revocation\n")
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- keyedTicketUpload(client, path, actor, "revoked.txt", payload, "revoked-keyed-upload-001")
			}()
			waitUploadBlockedBy(t, db, gate.Conn().PgConn().PID())
			var objectID string
			if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM file_storage_objects WHERE kind='ticket_attachments'").Scan(&objectID); err != nil {
				t.Fatal(err)
			}
			stored, err := os.ReadFile(filepath.Join(dir, objectID))
			if err != nil || !bytes.Equal(stored, payload) {
				t.Fatal("revocation did not occur after external Put", err)
			}
			want := 404
			switch scenario {
			case "customer_disabled":
				_, err = gate.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", owner)
				want = 401
			case "session_revoked":
				_, err = gate.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(customer)))
				want = 401
			case "api_key_revoked":
				_, err = gate.Exec(t.Context(), "UPDATE api_keys SET revoked_at=clock_timestamp() WHERE secret_hash=$1", tokenHash(secret))
				want = 401
			case "customer_scope_revoked":
				_, err = gate.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID)
			case "department_revoked":
				_, err = gate.Exec(t.Context(), "DELETE FROM admin_ticket_departments WHERE user_id=$1 AND department_id=$2", staffID, department)
			case "manage_revoked":
				_, err = gate.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['tickets.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID)
			case "delegation_parent_revoked":
				_, err = gate.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(root)))
				want = 401
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case response := <-done:
				expect(t, response, want)
				if scenario == "delegation_parent_revoked" && errorData(t, response)["code"] != "unauthorized" {
					t.Fatal("invalid delegation lost its authorization semantics", response.Body.String())
				}
			case <-time.After(7 * time.Second):
				t.Fatal("upload stalled after post-Put revocation")
			}
			var files, receipts, intents int
			if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ticket_attachments WHERE ticket_id=$1),(SELECT count(*) FROM mutation_operations WHERE scope='ticket:attachment-upload'),(SELECT count(*) FROM mutation_operations WHERE scope='ticket:attachment-upload-intent')`, ticket).Scan(&files, &receipts, &intents); err != nil || files != 0 || receipts != 0 || intents != 1 {
				t.Fatal("revoked upload committed a business result or lost its intent", files, receipts, intents, err)
			}
		})
	}
}

func TestTicketAttachmentUploadResultRechecksAuthorityAfterWait(t *testing.T) {
	for _, scenario := range []string{"customer_disabled", "session_revoked", "api_key_revoked", "customer_scope_revoked", "department_revoked", "delegation_parent_revoked"} {
		t.Run(scenario, func(t *testing.T) {
			db, h, root, customer, staff, owner, staffID, department, ticket, _ := ticketStorageFixture(t)
			actor := staff
			if scenario == "customer_disabled" || scenario == "session_revoked" {
				actor = customer
			}
			if scenario == "delegation_parent_revoked" {
				actor = customerKeyDelegation(t, h, owner, root)
			}
			client, secret := h, ""
			if scenario == "api_key_revoked" {
				client, secret = ticketUploadAPIClient(t, h, customer)
				actor = ""
			}
			path, key := fmt.Sprintf("/api/v1/tickets/%d", ticket), "result-after-lock-001"
			expect(t, keyedTicketUpload(client, path, actor, "query.txt", []byte("private committed attachment"), key), 201)
			gate, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err = gate.Exec(t.Context(), "SELECT id FROM tickets WHERE id=$1 FOR UPDATE", ticket); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- ticketUploadStatus(client, path, actor, key) }()
			waitUploadBlockedBy(t, db, gate.Conn().PgConn().PID())
			want := 404
			switch scenario {
			case "customer_disabled":
				_, err = gate.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", owner)
				want = 401
			case "session_revoked":
				_, err = gate.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(customer)))
				want = 401
			case "api_key_revoked":
				_, err = gate.Exec(t.Context(), "UPDATE api_keys SET revoked_at=clock_timestamp() WHERE secret_hash=$1", tokenHash(secret))
				want = 401
			case "customer_scope_revoked":
				_, err = gate.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID)
			case "department_revoked":
				_, err = gate.Exec(t.Context(), "DELETE FROM admin_ticket_departments WHERE user_id=$1 AND department_id=$2", staffID, department)
			case "delegation_parent_revoked":
				_, err = gate.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(root)))
				want = 401
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case response := <-done:
				expect(t, response, want)
				if strings.Contains(response.Body.String(), `"attachment"`) {
					t.Fatal("result query exposed committed metadata after revocation")
				}
			case <-time.After(7 * time.Second):
				t.Fatal("result query stalled after authority revocation")
			}
		})
	}
}

func TestTicketAttachmentUploadReplayRechecksAuthorityAfterReceiptWait(t *testing.T) {
	for _, scenario := range []string{"session_expired", "customer_scope_revoked", "department_revoked", "manage_revoked"} {
		t.Run(scenario, func(t *testing.T) {
			db, h, _, _, staff, _, staffID, department, ticket, _ := ticketStorageFixture(t)
			path, key := fmt.Sprintf("/api/v1/tickets/%d", ticket), "replay-after-receipt-wait"
			payload := []byte("confirmed before receipt lock wait")
			expect(t, keyedTicketUpload(h, path, staff, "replay.txt", payload, key), 201)
			gate, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("mutation:%d:ticket:attachment-upload:%s", staffID, key)); err != nil {
				t.Fatal(err)
			}
			if scenario == "session_expired" {
				if _, err = db.Pool.Exec(t.Context(), "UPDATE sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE token_hash=$1", tokenHash(cookieValue(staff))); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- keyedTicketUpload(h, path, staff, "replay.txt", payload, key) }()
			waitUploadBlockedBy(t, db, gate.Conn().PgConn().PID())
			want := 404
			switch scenario {
			case "session_expired":
				<-time.After(1200 * time.Millisecond)
				want = 401
			case "customer_scope_revoked":
				_, err = gate.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID)
			case "department_revoked":
				_, err = gate.Exec(t.Context(), "DELETE FROM admin_ticket_departments WHERE user_id=$1 AND department_id=$2", staffID, department)
			case "manage_revoked":
				_, err = gate.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['tickets.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case response := <-done:
				expect(t, response, want)
				if strings.Contains(response.Body.String(), `"filename"`) {
					t.Fatal("replay exposed immutable metadata after authority was withdrawn")
				}
			case <-time.After(7 * time.Second):
				t.Fatal("replay stalled after receipt wait revocation")
			}
		})
	}
}
