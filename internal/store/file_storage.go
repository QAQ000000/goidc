package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"time"

	"github.com/codex/zjmfgo/internal/filestorage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type FilePayload struct {
	Content  []byte
	ObjectID *string
}

func fileKindLimit(kind string) int64 {
	switch kind {
	case "downloads", "customer_files":
		return 50 << 20
	case "ticket_attachments":
		return 10 << 20
	case "content_images":
		return 5 << 20
	case "invoice_application_documents":
		return 20 << 20
	default:
		return 0
	}
}

func fileRandomKey() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func fileLocationHash(location string) string {
	digest := sha256.Sum256([]byte(location))
	return hex.EncodeToString(digest[:])
}

// A business transaction can occupy the last business connection. Journaling
// must never wait for that same pool, or an upload would deadlock its caller.
// Copying its config preserves a disposable schema's search_path and TLS setup.
func (s *Store) fileJournal(ctx context.Context) (*pgxpool.Pool, error) {
	s.fileJournalMu.Lock()
	defer s.fileJournalMu.Unlock()
	if s.fileJournalClosed || s.Pool == nil {
		return nil, conflict("file_storage_unavailable", "文件存储暂不可用")
	}
	if s.fileJournalPool != nil {
		return s.fileJournalPool, nil
	}
	cfg := s.Pool.Config().Copy()
	cfg.MaxConns, cfg.MinConns = 2, 0
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, conflict("file_storage_unavailable", "文件存储暂不可用")
	}
	s.fileJournalPool = pool
	return pool, nil
}

// PersistFile creates an upload intent before the first external write. It
// never retries Put or deletes on failure: the business commit may be unknown.
func (s *Store) PersistFile(ctx context.Context, tx pgx.Tx, kind string, content []byte) (FilePayload, error) {
	return s.persistFileWithKey(ctx, tx, kind, content, "")
}

// A ticket upload reserves its private object key in mutation_operations before
// entering the business transaction. Other file domains continue generating a
// fresh key here; this method never resumes or repeats a prior Put.
func (s *Store) persistFileWithKey(ctx context.Context, tx pgx.Tx, kind string, content []byte, objectKey string) (FilePayload, error) {
	limit := fileKindLimit(kind)
	if limit == 0 || len(content) == 0 || int64(len(content)) > limit {
		return FilePayload{}, conflict("invalid_file", "文件类型或大小无效")
	}
	if s.FileStorage == nil {
		return FilePayload{Content: content}, nil
	}
	profile, backend, location, err := s.FileStorage.Default()
	if err != nil {
		return FilePayload{}, conflict("file_storage_unavailable", "文件存储配置不可用")
	}
	if profile == "database" && backend == nil {
		return FilePayload{Content: content}, nil
	}
	if tx == nil || profile == "" || backend == nil || location == "" {
		return FilePayload{}, conflict("file_storage_unavailable", "文件存储配置不可用")
	}
	if objectKey == "" {
		return s.persistExternalFile(ctx, kind, content, profile, backend, location)
	}
	decoded, keyErr := hex.DecodeString(objectKey)
	if keyErr != nil || len(decoded) != 32 || objectKey != hex.EncodeToString(decoded) {
		return FilePayload{}, conflict("file_storage_unavailable", "文件上传记录无效")
	}
	return s.persistExternalFileWithKey(ctx, kind, content, profile, backend, location, objectKey)
}

func (s *Store) persistExternalFile(ctx context.Context, kind string, content []byte, profile string, backend filestorage.Backend, location string) (FilePayload, error) {
	key, err := fileRandomKey()
	if err != nil {
		return FilePayload{}, conflict("file_storage_unavailable", "文件存储暂不可用")
	}
	return s.persistExternalFileWithKey(ctx, kind, content, profile, backend, location, key)
}

func (s *Store) persistExternalFileWithKey(ctx context.Context, kind string, content []byte, profile string, backend filestorage.Backend, location, key string) (FilePayload, error) {
	digest := sha256.Sum256(content)
	checksum := hex.EncodeToString(digest[:])
	journalCtx, cancelJournal := context.WithTimeout(ctx, 10*time.Second)
	journal, err := s.fileJournal(journalCtx)
	if err == nil {
		_, err = journal.Exec(journalCtx, `INSERT INTO file_storage_objects(id,profile_id,location_sha256,kind,object_key,size_bytes,sha256,state) VALUES($1,$2,$3,$4,$1,$5,$6,'staging')`, key, profile, fileLocationHash(location), kind, len(content), checksum)
	}
	cancelJournal()
	if err != nil {
		// An uncertain journal commit has not issued a Put. An eventual staging
		// row is safe for delayed idempotent collection; do not retry this key.
		return FilePayload{}, conflict("file_storage_unavailable", "文件上传记录暂不可用")
	}
	// A committed staging row proves Put has not been issued. Before crossing
	// that boundary, durably fence the intent out of automatic collection. A
	// crash or lost response after Put starts must remain uncertain, even when
	// the external service completes the write after our request has ended.
	fenceCtx, cancelFence := context.WithTimeout(ctx, 10*time.Second)
	fence, err := journal.Exec(fenceCtx, `UPDATE file_storage_objects SET state='uncertain',updated_at=clock_timestamp() WHERE id=$1 AND state='staging'`, key)
	cancelFence()
	if err != nil || fence.RowsAffected() != 1 {
		return FilePayload{}, conflict("file_storage_unavailable", "文件上传记录未确认，请联系管理员核验")
	}
	putCtx, cancelPut := context.WithTimeout(ctx, 45*time.Second)
	defer cancelPut()
	putErr := backend.Put(putCtx, key, content, checksum)
	cancelPut()
	finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelFinish()
	if putErr != nil {
		// Keep even known rejections as uncertain: adapters must not silently
		// change the safety boundary by classifying a transport failure as safe.
		_, _ = journal.Exec(finishCtx, `UPDATE file_storage_objects SET last_error_code='put_failed',updated_at=clock_timestamp() WHERE id=$1 AND state='uncertain'`, key)
		return FilePayload{}, conflict("file_storage_upload_failed", "文件上传未确认，请稍后重试或联系管理员核验")
	}
	tag, err := journal.Exec(finishCtx, `UPDATE file_storage_objects SET state='ready',updated_at=clock_timestamp() WHERE id=$1 AND state='uncertain'`, key)
	if err != nil || tag.RowsAffected() != 1 {
		// The durable intent remains. Never compensate with Delete when the
		// ready transition or a later business commit could have succeeded.
		return FilePayload{}, conflict("file_storage_unavailable", "文件上传记录未确认，请联系管理员核验")
	}
	return FilePayload{ObjectID: &key}, nil
}

// ReadFileContent retains the bounded in-memory contract of the existing
// authorization and Range handlers. The object lock fences GC until verified
// bytes have been copied into memory.
func (s *Store) ReadFileContent(ctx context.Context, tx pgx.Tx, kind string, inline []byte, objectID *string) ([]byte, error) {
	if objectID == nil {
		return inline, nil
	}
	if inline != nil || fileKindLimit(kind) == 0 || s.FileStorage == nil {
		return nil, conflict("file_storage_unavailable", "文件存储配置不可用")
	}
	if tx == nil {
		if s.Pool == nil {
			return nil, conflict("file_storage_unavailable", "文件暂不可用")
		}
		var err error
		tx, err = s.Pool.Begin(ctx)
		if err != nil {
			return nil, conflict("file_storage_unavailable", "文件暂不可用")
		}
		defer tx.Rollback(ctx)
	}
	var profile, locationHash, storedKind, key, checksum, state string
	var size int64
	err := tx.QueryRow(ctx, `SELECT profile_id,location_sha256,kind,object_key,size_bytes,sha256,state FROM file_storage_objects WHERE id=$1 FOR SHARE`, *objectID).Scan(&profile, &locationHash, &storedKind, &key, &size, &checksum, &state)
	if err != nil || storedKind != kind || size < 1 || size > fileKindLimit(kind) || state != "attached" {
		return nil, conflict("file_storage_unavailable", "文件暂不可用")
	}
	backend, location, err := s.FileStorage.Resolve(profile)
	if err != nil || backend == nil || location == "" || fileLocationHash(location) != locationHash {
		return nil, conflict("file_storage_unavailable", "文件存储配置不可用")
	}
	readCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	reader, err := backend.Open(readCtx, key, size, checksum)
	if err != nil || reader == nil {
		return nil, conflict("file_storage_read_failed", "文件读取失败，请稍后重试")
	}
	content, readErr := io.ReadAll(io.LimitReader(reader, size+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || int64(len(content)) != size {
		return nil, conflict("file_storage_read_failed", "文件完整性检查失败")
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != checksum {
		return nil, conflict("file_storage_read_failed", "文件完整性检查失败")
	}
	return content, nil
}

type fileObjectCollection struct {
	id, profile, locationHash, key, token string
}

// claimFileObject commits a deleting fence before network I/O. Attach triggers
// take the same object lock and reject that state, including an expired lease.
func claimFileObject(ctx context.Context, journal *pgxpool.Pool) (*fileObjectCollection, error) {
	tx, err := journal.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var item fileObjectCollection
	err = tx.QueryRow(ctx, `SELECT id,profile_id,location_sha256,object_key FROM file_storage_objects WHERE
 (state IN ('staging','ready') AND gc_after<=clock_timestamp()) OR
 (state='deleting' AND delete_lease_until<=clock_timestamp()) ORDER BY gc_after,id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&item.id, &item.profile, &item.locationHash, &item.key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var referenced bool
	if err = tx.QueryRow(ctx, `SELECT file_storage_object_referenced($1)`, item.id).Scan(&referenced); err != nil {
		return nil, err
	}
	if referenced {
		_, err = tx.Exec(ctx, `UPDATE file_storage_objects SET state='attached',gc_after=NULL,delete_lease_until=NULL,delete_lease_token='',updated_at=clock_timestamp() WHERE id=$1`, item.id)
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	item.token, err = fileRandomKey()
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE file_storage_objects SET state='deleting',delete_lease_token=$2,delete_lease_until=clock_timestamp()+interval '2 minutes',delete_attempts=delete_attempts+1,updated_at=clock_timestamp() WHERE id=$1`, item.id, item.token); err != nil {
		return nil, err
	}
	return &item, tx.Commit(ctx)
}

// CollectFileObjects retries only idempotent Delete. Put is never replayed.
// A missing profile or changed location leaves a durable retryable task.
func (s *Store) CollectFileObjects(ctx context.Context, limit int) (int, error) {
	if s.FileStorage == nil || limit <= 0 {
		return 0, nil
	}
	if limit > 100 {
		limit = 100
	}
	journal, err := s.fileJournal(ctx)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for range limit {
		WorkerRuntimeProgress(ctx)
		claimCtx, cancelClaim := context.WithTimeout(ctx, 10*time.Second)
		item, err := claimFileObject(claimCtx, journal)
		cancelClaim()
		if err != nil {
			return deleted, conflict("file_storage_unavailable", "文件回收记录暂不可用")
		}
		if item == nil {
			return deleted, nil
		}
		backend, location, resolveErr := s.FileStorage.Resolve(item.profile)
		code := ""
		switch {
		case resolveErr != nil || backend == nil || location == "":
			code = "profile_unavailable"
		case fileLocationHash(location) != item.locationHash:
			code = "location_mismatch"
		default:
			deleteCtx, cancelDelete := context.WithTimeout(ctx, 45*time.Second)
			if err = backend.Delete(deleteCtx, item.key); err != nil {
				code = "delete_failed"
			}
			cancelDelete()
		}
		finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		var tagErr error
		var affected int64
		if code == "" {
			tag, e := journal.Exec(finishCtx, `UPDATE file_storage_objects SET state='deleted',gc_after=NULL,delete_lease_until=NULL,delete_lease_token='',last_error_code='',updated_at=clock_timestamp() WHERE id=$1 AND state='deleting' AND delete_lease_token=$2`, item.id, item.token)
			tagErr, affected = e, tag.RowsAffected()
		} else {
			_, tagErr = journal.Exec(finishCtx, `UPDATE file_storage_objects SET delete_lease_until=clock_timestamp()+interval '15 minutes',last_error_code=$3,updated_at=clock_timestamp() WHERE id=$1 AND state='deleting' AND delete_lease_token=$2`, item.id, item.token, code)
		}
		cancelFinish()
		if tagErr != nil {
			return deleted, conflict("file_storage_unavailable", "文件回收记录暂不可用")
		}
		if affected == 1 {
			deleted++
		}
	}
	return deleted, nil
}
