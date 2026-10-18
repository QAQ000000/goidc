package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/filestorage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFileStorageInlineCompatibilityAndLimits(t *testing.T) {
	s := &Store{}
	data := []byte("private inline content")
	for _, kind := range []string{"downloads", "customer_files", "ticket_attachments", "content_images", "invoice_application_documents"} {
		payload, err := s.PersistFile(t.Context(), nil, kind, data)
		if err != nil || payload.ObjectID != nil || !bytes.Equal(payload.Content, data) {
			t.Fatal("inline compatibility changed", kind, err)
		}
		got, err := s.ReadFileContent(t.Context(), nil, kind, payload.Content, nil)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("inline read changed", kind, err)
		}
	}
	for _, test := range []struct {
		kind string
		data []byte
	}{{"unknown", data}, {"downloads", nil}, {"content_images", make([]byte, (5<<20)+1)}} {
		if _, err := s.PersistFile(t.Context(), nil, test.kind, test.data); err == nil {
			t.Fatal("invalid object kind or size accepted")
		}
	}
	manager, err := filestorage.NewManager("database", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.FileStorage = manager
	if payload, err := s.PersistFile(t.Context(), nil, "customer_files", data); err != nil || !bytes.Equal(payload.Content, data) || payload.ObjectID != nil {
		t.Fatal("explicit database profile changed compatibility", err)
	}
	first, _ := fileRandomKey()
	second, _ := fileRandomKey()
	if len(first) != 64 || first == second || strings.Contains(first, "private") {
		t.Fatal("object key was not opaque and unique")
	}
}

func fileStorageFixture(t *testing.T) (*Store, string, int64, int64) {
	t.Helper()
	db := isolatedTestStore(t)
	actor, customer := topUpFixture(t, db)
	root := t.TempDir()
	db.FileStorage = fileStorageManager(t, "files", root)
	return db, root, actor, customer
}

func fileStorageManager(t *testing.T, defaultID, root string) *filestorage.Manager {
	t.Helper()
	manager, err := filestorage.NewManager(defaultID, []filestorage.Profile{{ID: "files", Implementation: "local", Values: map[string]any{"root": root}}})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func fileStorageUpload(t *testing.T, db *Store, tx pgx.Tx, kind string, data []byte) FilePayload {
	t.Helper()
	payload, err := db.PersistFile(t.Context(), tx, kind, data)
	if err != nil || payload.ObjectID == nil || payload.Content != nil {
		t.Fatal("external upload failed", err)
	}
	return payload
}

func fileStorageAttachCustomer(t *testing.T, db *Store, tx pgx.Tx, actor, customer int64, payload FilePayload, data []byte) int64 {
	t.Helper()
	digest := sha256.Sum256(data)
	var id int64
	if err := tx.QueryRow(t.Context(), `INSERT INTO customer_files(customer_id,title,filename,mime_type,size_bytes,sha256,content,storage_object_id,uploader_id) VALUES($1,'Private file','fixture.txt','text/plain',$2,$3,$4,$5,$6) RETURNING id`, customer, len(data), hex.EncodeToString(digest[:]), payload.Content, payload.ObjectID, actor).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

type fileStoragePutProbe struct {
	filestorage.Backend
	before func(context.Context, string)
	after  func()
}

func (p fileStoragePutProbe) Put(ctx context.Context, key string, data []byte, checksum string) error {
	if p.before != nil {
		p.before(ctx, key)
	}
	if err := p.Backend.Put(ctx, key, data, checksum); err != nil {
		return err
	}
	if p.after != nil {
		p.after()
	}
	return nil
}

func TestFileStorageUploadFencePrecedesExternalWrite(t *testing.T) {
	for _, crash := range []bool{false, true} {
		name := "successful_put"
		if crash {
			name = "crash_after_external_write"
		}
		t.Run(name, func(t *testing.T) {
			db, root, _, _ := fileStorageFixture(t)
			profile, backend, location, err := db.FileStorage.Default()
			if err != nil {
				t.Fatal(err)
			}
			data := []byte("external write must start behind its durable uncertain fence")
			var objectID string
			probe := fileStoragePutProbe{Backend: backend, before: func(ctx context.Context, key string) {
				objectID = key
				var fenced bool
				if err := db.Pool.QueryRow(ctx, `SELECT state='uncertain' AND last_error_code='' FROM file_storage_objects WHERE id=$1`, key).Scan(&fenced); err != nil || !fenced {
					t.Fatal("Put started without a durably committed uncertain intent", err)
				}
				if _, err := db.Pool.Exec(ctx, `UPDATE file_storage_objects SET gc_after=clock_timestamp()-interval '2 days' WHERE id=$1`, key); err != nil {
					t.Fatal(err)
				}
				if n, err := db.CollectFileObjects(ctx, 10); err != nil || n != 0 {
					t.Fatal("collector claimed an uncertain write at the Put boundary", n, err)
				}
			}}
			crashMarker := new(int)
			if crash {
				probe.after = func() { panic(crashMarker) }
			}
			var payload FilePayload
			var putErr error
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						if !crash || recovered != crashMarker {
							panic(recovered)
						}
					}
				}()
				payload, putErr = db.persistExternalFile(t.Context(), "customer_files", data, profile, probe, location)
				if crash {
					t.Fatal("crash fixture reached finalization")
				}
			}()
			if objectID == "" || putErr != nil {
				t.Fatal("fixture never reached a successful external write", putErr)
			}
			content, err := os.ReadFile(filepath.Join(root, objectID))
			if err != nil || !bytes.Equal(content, data) {
				t.Fatal("external payload was lost before finalization", err)
			}
			var state string
			if err := db.Pool.QueryRow(t.Context(), `SELECT state FROM file_storage_objects WHERE id=$1`, objectID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if crash {
				if state != "uncertain" {
					t.Fatal("process termination left an automatically collectable intent", state)
				}
				if n, err := db.CollectFileObjects(t.Context(), 10); err != nil || n != 0 {
					t.Fatal("collector deleted a write after process termination", n, err)
				}
				if _, err := os.Stat(filepath.Join(root, objectID)); err != nil {
					t.Fatal("process termination deleted externally persisted bytes", err)
				}
			} else if state != "ready" || payload.ObjectID == nil || *payload.ObjectID != objectID {
				t.Fatal("confirmed Put did not leave an attachable ready object", state)
			}
		})
	}
	t.Run("fence_rejected", func(t *testing.T) {
		db, _, _, _ := fileStorageFixture(t)
		if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_file_storage_put_fence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='uncertain' THEN RAISE EXCEPTION 'fixture rejects fence'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_file_storage_put_fence BEFORE UPDATE ON file_storage_objects FOR EACH ROW EXECUTE FUNCTION reject_file_storage_put_fence()`); err != nil {
			t.Fatal(err)
		}
		profile, backend, location, err := db.FileStorage.Default()
		if err != nil {
			t.Fatal(err)
		}
		called := false
		probe := fileStoragePutProbe{Backend: backend, before: func(context.Context, string) { called = true }}
		if _, err := db.persistExternalFile(t.Context(), "customer_files", []byte("must never issue Put"), profile, probe, location); err == nil || called {
			t.Fatal("Put crossed a rejected or unconfirmed durable fence", called, err)
		}
		var retained bool
		if err := db.Pool.QueryRow(t.Context(), `SELECT count(*)=1 AND bool_and(state='staging') FROM file_storage_objects`).Scan(&retained); err != nil || !retained {
			t.Fatal("rejected fence lost the not-yet-issued intent", err)
		}
	})
}

func TestFileStorageJournalSurvivesRollbackAndCommitUnknown(t *testing.T) {
	db, root, actor, customer := fileStorageFixture(t)
	// A single occupied business connection cannot block its independent journal.
	cfg := db.Pool.Config().Copy()
	cfg.MaxConns, cfg.MinConns = 1, 0
	small, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	previous := db.Pool
	db.Pool = small
	previous.Close()
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("rollback object")
	payload := fileStorageUpload(t, db, tx, "customer_files", data)
	fileStorageAttachCustomer(t, db, tx, actor, customer, payload, data)
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = db.Pool.QueryRow(t.Context(), `SELECT state FROM file_storage_objects WHERE id=$1`, payload.ObjectID).Scan(&state); err != nil || state != "ready" {
		t.Fatal("rollback lost or attached the committed upload intent", state, err)
	}
	if _, err = os.Stat(filepath.Join(root, *payload.ObjectID)); err != nil {
		t.Fatal("rollback immediately deleted an object", err)
	}
	if count, err := db.CollectFileObjects(t.Context(), 10); err != nil || count != 0 {
		t.Fatal("new rollback object bypassed retention", count, err)
	}
	if _, err = db.Pool.Exec(t.Context(), `UPDATE file_storage_objects SET gc_after=clock_timestamp()-interval '1 second' WHERE id=$1`, payload.ObjectID); err != nil {
		t.Fatal(err)
	}
	if count, err := db.CollectFileObjects(t.Context(), 10); err != nil || count != 1 {
		t.Fatal("expired rollback object was not collected", count, err)
	}
	if _, err = os.Stat(filepath.Join(root, *payload.ObjectID)); !os.IsNotExist(err) {
		t.Fatal("collected object remains on disk")
	}
	tx, err = db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	committed := fileStorageUpload(t, db, tx, "customer_files", []byte("committed private file"))
	id := fileStorageAttachCustomer(t, db, tx, actor, customer, committed, []byte("committed private file"))
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Model a lost commit response by relying solely on the durable reference,
	// then deliberately present an overdue journal state to the collector.
	if _, err = db.Pool.Exec(t.Context(), `UPDATE file_storage_objects SET state='ready',gc_after=clock_timestamp()-interval '1 second' WHERE id=$1`, committed.ObjectID); err != nil {
		t.Fatal(err)
	}
	if count, err := db.CollectFileObjects(t.Context(), 10); err != nil || count != 0 {
		t.Fatal("commit-unknown cleanup deleted a referenced object", count, err)
	}
	if got, err := db.ReadFileContent(t.Context(), nil, "customer_files", nil, committed.ObjectID); err != nil || string(got) != "committed private file" {
		t.Fatal("committed reference could not be read", err)
	}
	if _, err = db.Pool.Exec(t.Context(), `UPDATE customer_files SET title='metadata-only rename' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), `DELETE FROM customer_files WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	var delayed bool
	if err = db.Pool.QueryRow(t.Context(), `SELECT state='ready' AND gc_after>=clock_timestamp()+interval '59 minutes' AND unreferenced_at IS NOT NULL FROM file_storage_objects WHERE id=$1`, committed.ObjectID).Scan(&delayed); err != nil || !delayed {
		t.Fatal("deletion did not preserve the post-reference grace period", err)
	}
}

func TestFileStorageReadRejectsLocationMissingProfileAndCorruption(t *testing.T) {
	db, root, actor, customer := fileStorageFixture(t)
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("complete bounded private content")
	payload := fileStorageUpload(t, db, tx, "customer_files", data)
	fileStorageAttachCustomer(t, db, tx, actor, customer, payload, data)
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	db.FileStorage = fileStorageManager(t, "database", root)
	if got, err := db.ReadFileContent(t.Context(), nil, "customer_files", nil, payload.ObjectID); err != nil || !bytes.Equal(got, data) {
		t.Fatal("default change redirected an existing object", err)
	}
	db.FileStorage, err = filestorage.NewManager("database", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReadFileContent(t.Context(), nil, "customer_files", nil, payload.ObjectID); err == nil {
		t.Fatal("missing historical profile was ignored")
	}
	db.FileStorage = fileStorageManager(t, "files", t.TempDir())
	if _, err := db.ReadFileContent(t.Context(), nil, "customer_files", nil, payload.ObjectID); err == nil {
		t.Fatal("same profile ID redirected to another location")
	}
	db.FileStorage = fileStorageManager(t, "files", root)
	if _, err := db.ReadFileContent(t.Context(), nil, "ticket_attachments", nil, payload.ObjectID); err == nil {
		t.Fatal("object crossed a business kind boundary")
	}
	for _, changed := range [][]byte{data[:len(data)-1], append(append([]byte(nil), data...), '!'), bytes.Repeat([]byte{'x'}, len(data))} {
		if err := os.WriteFile(filepath.Join(root, *payload.ObjectID), changed, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ReadFileContent(t.Context(), nil, "customer_files", nil, payload.ObjectID); err == nil {
			t.Fatal("truncated, extended or wrong-digest object was accepted")
		}
	}
}

func TestFileStorageGCLeaseAndAttachFence(t *testing.T) {
	db, _, actor, customer := fileStorageFixture(t)
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("unattached object")
	payload := fileStorageUpload(t, db, tx, "customer_files", data)
	_ = tx.Rollback(t.Context())
	if _, err := db.Pool.Exec(t.Context(), `UPDATE file_storage_objects SET gc_after=clock_timestamp()-interval '1 second' WHERE id=$1`, payload.ObjectID); err != nil {
		t.Fatal(err)
	}
	journal, err := db.fileJournal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := claimFileObject(t.Context(), journal)
	if err != nil || claim == nil {
		t.Fatal("GC did not claim expired object", err)
	}
	if another, err := claimFileObject(t.Context(), journal); err != nil || another != nil {
		t.Fatal("live delete lease was reclaimed", err)
	}
	tx, err = db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	_, err = tx.Exec(t.Context(), `INSERT INTO customer_files(customer_id,title,filename,mime_type,size_bytes,sha256,storage_object_id,uploader_id) VALUES($1,'late attach','late.txt','text/plain',$2,$3,$4,$5)`, customer, len(data), hex.EncodeToString(digest[:]), payload.ObjectID, actor)
	_ = tx.Rollback(t.Context())
	if err == nil {
		t.Fatal("business transaction attached an object fenced for deletion")
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE file_storage_objects SET delete_lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, payload.ObjectID); err != nil {
		t.Fatal(err)
	}
	recovered, err := claimFileObject(t.Context(), journal)
	if err != nil || recovered == nil || recovered.token == claim.token {
		t.Fatal("expired delete lease did not recover with a fresh fence", err)
	}
	if tag, err := db.Pool.Exec(t.Context(), `UPDATE file_storage_objects SET state='deleted' WHERE id=$1 AND delete_lease_token=$2`, payload.ObjectID, claim.token); err != nil || tag.RowsAffected() != 0 {
		t.Fatal("stale collector completed a new lease", err)
	}
}

func TestFileStorageVerifiedReadRetainsDeletionFence(t *testing.T) {
	db, _, actor, customer := fileStorageFixture(t)
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("read fence protects complete verified bytes")
	payload := fileStorageUpload(t, db, tx, "customer_files", data)
	fileStorageAttachCustomer(t, db, tx, actor, customer, payload, data)
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	readTx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Rollback(t.Context())
	if got, err := db.ReadFileContent(t.Context(), readTx, "customer_files", nil, payload.ObjectID); err != nil || !bytes.Equal(got, data) {
		t.Fatal("verified read failed", err)
	}
	collector, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = collector.Exec(t.Context(), `SELECT id FROM file_storage_objects WHERE id=$1 FOR UPDATE NOWAIT`, payload.ObjectID)
	_ = collector.Rollback(t.Context())
	var lockErr *pgconn.PgError
	if !errors.As(err, &lockErr) || lockErr.Code != "55P03" {
		t.Fatal("verified read released its deletion fence before the business transaction ended", err)
	}
	if err = readTx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	collector, err = db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer collector.Rollback(t.Context())
	if _, err = collector.Exec(t.Context(), `SELECT id FROM file_storage_objects WHERE id=$1 FOR UPDATE NOWAIT`, payload.ObjectID); err != nil {
		t.Fatal("completed business transaction left its deletion fence held", err)
	}
}

func TestFileStorageCollectionFailuresRetainRetryableIntent(t *testing.T) {
	db, root, _, _ := fileStorageFixture(t)
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	payload := fileStorageUpload(t, db, tx, "customer_files", []byte("retryable orphan"))
	_ = tx.Rollback(t.Context())
	expire := func() {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), `UPDATE file_storage_objects SET gc_after=clock_timestamp()-interval '1 second',delete_lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, payload.ObjectID); err != nil {
			t.Fatal(err)
		}
	}
	assertFailure := func(code string, attempts int) {
		t.Helper()
		if n, err := db.CollectFileObjects(t.Context(), 10); err != nil || n != 0 {
			t.Fatal("failed cleanup reported deletion or lost its retry task", n, err)
		}
		var retained bool
		if err := db.Pool.QueryRow(t.Context(), `SELECT state='deleting' AND last_error_code=$2 AND delete_attempts=$3 AND delete_lease_token<>'' AND delete_lease_until>=clock_timestamp()+interval '14 minutes' FROM file_storage_objects WHERE id=$1`, payload.ObjectID, code, attempts).Scan(&retained); err != nil || !retained {
			t.Fatal("cleanup failure did not retain its fenced retry record", code, err)
		}
		if n, err := db.CollectFileObjects(t.Context(), 10); err != nil || n != 0 {
			t.Fatal("cleanup failure ignored its retry backoff", n, err)
		}
		var actual int
		if err := db.Pool.QueryRow(t.Context(), `SELECT delete_attempts FROM file_storage_objects WHERE id=$1`, payload.ObjectID).Scan(&actual); err != nil || actual != attempts {
			t.Fatal("live retry lease was reclaimed", actual, err)
		}
	}
	expire()
	db.FileStorage, err = filestorage.NewManager("database", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertFailure("profile_unavailable", 1)
	expire()
	db.FileStorage = fileStorageManager(t, "files", t.TempDir())
	assertFailure("location_mismatch", 2)
	if _, err = os.Stat(filepath.Join(root, *payload.ObjectID)); err != nil {
		t.Fatal("unavailable profile or location mismatch deleted original bytes", err)
	}
	expire()
	db.FileStorage = fileStorageManager(t, "files", root)
	if err = os.Rename(root, root+"-moved"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(root+"-moved", root) })
	assertFailure("delete_failed", 3)
	if _, err = os.Stat(filepath.Join(root+"-moved", *payload.ObjectID)); err != nil {
		t.Fatal("failed deletion lost original bytes", err)
	}
	if err = os.Rename(root+"-moved", root); err != nil {
		t.Fatal(err)
	}
	expire()
	if n, err := db.CollectFileObjects(t.Context(), 10); err != nil || n != 1 {
		t.Fatal("recovered storage could not finish its durable cleanup task", n, err)
	}
	var deleted bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT state='deleted' AND delete_attempts=4 AND delete_lease_token='' AND delete_lease_until IS NULL AND last_error_code='' FROM file_storage_objects WHERE id=$1`, payload.ObjectID).Scan(&deleted); err != nil || !deleted {
		t.Fatal("successful retry did not finish and clear the task", err)
	}
}

func TestFileStorageConcurrentJournalAndCollection(t *testing.T) {
	db, _, _, _ := fileStorageFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for n := range 6 {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			tx, err := db.Pool.Begin(ctx)
			if err != nil {
				results <- err
				return
			}
			defer tx.Rollback(ctx)
			_, err = db.PersistFile(ctx, tx, "customer_files", []byte(fmt.Sprintf("concurrent-private-%d", n)))
			results <- err
		})
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal("concurrent upload journal failed", err)
		}
	}
	var count, distinct int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*),count(DISTINCT object_key) FROM file_storage_objects`).Scan(&count, &distinct); err != nil || count != 6 || distinct != 6 {
		t.Fatal("concurrent upload intents collided", count, distinct, err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE file_storage_objects SET gc_after=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	counts := make(chan int, 2)
	errors := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			n, err := db.CollectFileObjects(t.Context(), 10)
			counts <- n
			errors <- err
		})
	}
	wg.Wait()
	if first, second := <-errors, <-errors; first != nil || second != nil {
		t.Fatal("concurrent collection failed", first, second)
	}
	if collected := <-counts + <-counts; collected != 6 {
		t.Fatal("concurrent collectors missed or double-counted objects", collected)
	}
}

func TestFileStorageFailedPutRemainsTrackedAndIsNotReplayed(t *testing.T) {
	db, root, _, _ := fileStorageFixture(t)
	// Remove the configured directory after Manager retained its identity.
	// Put fails locally, but its independently committed intent must survive.
	if err := os.Rename(root, root+"-moved"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(root+"-moved", root) })
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.PersistFile(t.Context(), tx, "customer_files", []byte("unconfirmed private upload")); err == nil {
		t.Fatal("failed Put returned an attachable payload")
	}
	_ = tx.Rollback(t.Context())
	var tracked bool
	if err = db.Pool.QueryRow(t.Context(), `SELECT count(*)=1 AND bool_and(state='uncertain' AND last_error_code='put_failed') FROM file_storage_objects`).Scan(&tracked); err != nil || !tracked {
		t.Fatal("failed external write lost its durable intent", err)
	}
	if _, err = db.Pool.Exec(t.Context(), `UPDATE file_storage_objects SET gc_after=clock_timestamp()-interval '2 days'`); err != nil {
		t.Fatal(err)
	}
	if n, err := db.CollectFileObjects(t.Context(), 10); err != nil || n != 0 {
		t.Fatal("uncertain Put was automatically replayed or deleted", n, err)
	}
}

func fileStorageOwners(t *testing.T, db *Store, actor, customer int64) (ticket, article, application int64) {
	t.Helper()
	var profile, address int64
	for _, seed := range []struct {
		query string
		args  []any
		id    *int64
	}{
		{`INSERT INTO tickets(ticket_no,user_id,subject,body) VALUES('FILE-STORAGE-TICKET',$1,'private ticket','body') RETURNING id`, []any{customer}, &ticket},
		{`INSERT INTO content_articles(content_type,title,slug,body,author_id) VALUES('news','File storage','file-storage','body',$1) RETURNING id`, []any{actor}, &article},
		{`INSERT INTO invoice_profiles(user_id,profile_type,title) VALUES($1,'individual','File storage') RETURNING id`, []any{customer}, &profile},
		{`INSERT INTO invoice_addresses(user_id,recipient,phone,address) VALUES($1,'File storage','123','Private address') RETURNING id`, []any{customer}, &address},
		{`INSERT INTO invoice_applications(user_id,profile_id,address_id,profile_snapshot,address_snapshot,amount_cents) VALUES($1,$2,$3,'{}','{}',100) RETURNING id`, []any{customer, profile, address}, &application},
	} {
		// Profile/address parameters are resolved only when their turn arrives.
		if seed.id == &application {
			seed.args = []any{customer, profile, address}
		}
		if err := db.Pool.QueryRow(t.Context(), seed.query, seed.args...).Scan(seed.id); err != nil {
			t.Fatal(err)
		}
	}
	return ticket, article, application
}

func TestFileStorageCascadeAndReplacementReleaseAllKinds(t *testing.T) {
	db, _, actor, customer := fileStorageFixture(t)
	ticket, article, application := fileStorageOwners(t, db, actor, customer)
	data := []byte("private object payload")
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	objects := map[string]*string{}
	var customerFile int64
	for _, kind := range []string{"downloads", "customer_files", "ticket_attachments", "content_images", "invoice_application_documents"} {
		tx, err := db.Pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		payload := fileStorageUpload(t, db, tx, kind, data)
		objects[kind] = payload.ObjectID
		switch kind {
		case "downloads":
			_, err = tx.Exec(t.Context(), `INSERT INTO downloads(title,access_level,source_type,filename,mime_type,size_bytes,sha256,storage_object_id,author_id) VALUES('Private download','public','local','file.txt','text/plain',$1,$2,$3,$4)`, len(data), hash, payload.ObjectID, actor)
		case "customer_files":
			customerFile = fileStorageAttachCustomer(t, db, tx, actor, customer, payload, data)
		case "ticket_attachments":
			_, err = tx.Exec(t.Context(), `INSERT INTO ticket_attachments(ticket_id,uploader_id,filename,mime_type,size_bytes,sha256,storage_object_id) VALUES($1,$2,'file.txt','text/plain',$3,$4,$5)`, ticket, actor, len(data), hash, payload.ObjectID)
		case "content_images":
			_, err = tx.Exec(t.Context(), `INSERT INTO content_images(article_id,uploader_id,filename,mime_type,size_bytes,storage_object_id) VALUES($1,$2,'file.png','image/png',$3,$4)`, article, actor, len(data), payload.ObjectID)
		case "invoice_application_documents":
			_, err = tx.Exec(t.Context(), `INSERT INTO invoice_application_documents(application_id,filename,mime_type,size_bytes,sha256,storage_object_id,uploader_id) VALUES($1,'file.pdf','application/pdf',$2,$3,$4,$5)`, application, len(data), hash, payload.ObjectID, actor)
		}
		if err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(kind, err)
		}
		if err = tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for kind, object := range objects {
		if got, err := db.ReadFileContent(t.Context(), nil, kind, nil, object); err != nil || !bytes.Equal(got, data) {
			t.Fatal("attached business object could not be read", kind, err)
		}
	}
	// Inline replacement releases the previous object in the same transaction.
	if _, err := db.Pool.Exec(t.Context(), `UPDATE customer_files SET content=$2,storage_object_id=NULL WHERE id=$1`, customerFile, data); err != nil {
		t.Fatal(err)
	}
	for _, deletion := range []struct {
		query string
		id    int64
	}{
		{`DELETE FROM downloads`, 0},
		{`DELETE FROM tickets WHERE id=$1`, ticket},
		{`DELETE FROM content_articles WHERE id=$1`, article},
		{`DELETE FROM invoice_applications WHERE id=$1`, application},
	} {
		var err error
		if deletion.id == 0 {
			_, err = db.Pool.Exec(t.Context(), deletion.query)
		} else {
			_, err = db.Pool.Exec(t.Context(), deletion.query, deletion.id)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	var released bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*)=5 AND bool_and(state='ready' AND gc_after>=clock_timestamp()+interval '59 minutes' AND NOT file_storage_object_referenced(id)) FROM file_storage_objects`).Scan(&released); err != nil || !released {
		t.Fatal("cascade or replacement failed to release one of the five object kinds", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE file_storage_objects SET gc_after=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if count, err := db.CollectFileObjects(t.Context(), 10); err != nil || count != 5 {
		t.Fatal("released objects were not collected exactly once", count, err)
	}
}
