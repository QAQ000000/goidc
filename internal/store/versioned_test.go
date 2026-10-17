package store

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestVersionedCASConcurrencyBindingAndRollback(t *testing.T) {
	db := isolatedTestStore(t)
	ctx := t.Context()
	if _, err := db.Pool.Exec(ctx, `CREATE TABLE "VersionedFixture"(id bigint PRIMARY KEY, value text, version bigint NOT NULL DEFAULT 1, updated_at timestamptz DEFAULT now()); INSERT INTO "VersionedFixture"(id,value) VALUES(7,'initial')`); err != nil {
		t.Fatal(err)
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	start := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := db.WithTx(ctx, func(tx pgx.Tx) error {
				args := make([]any, 1, 4)
				args[0] = "bound ' $2 value"
				next, err := UpdateVersioned(ctx, tx, "VersionedFixture", "value=$1", 7, 1, args...)
				if err == nil && next != 2 {
					return errors.New("wrong next version")
				}
				if args[:cap(args)][1] != nil {
					return errors.New("caller arguments overwritten")
				}
				return err
			})
			if err == nil {
				winners.Add(1)
			} else {
				var conflict *Conflict
				if !errors.As(err, &conflict) || conflict.Code != "version_conflict" {
					errs <- err
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if winners.Load() != 1 {
		t.Fatalf("winners=%d", winners.Load())
	}
	aborted := errors.New("audit failure")
	if err := db.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := LockVersion(ctx, tx, "VersionedFixture", 7, 2, "reload"); err != nil {
			return err
		}
		if _, err := UpdateVersioned(ctx, tx, "VersionedFixture", "value=$1", 7, 2, "rolled back"); err != nil {
			return err
		}
		return aborted
	}); !errors.Is(err, aborted) {
		t.Fatal(err)
	}
	var version int64
	var value string
	if err := db.Pool.QueryRow(ctx, `SELECT version,value FROM "VersionedFixture" WHERE id=7`).Scan(&version, &value); err != nil || version != 2 || value != "bound ' $2 value" {
		t.Fatal(version, value, err)
	}
	if err := db.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := LockVersion(ctx, tx, "VersionedFixture", 7, 1, "domain reload")
		var conflict *Conflict
		if !errors.As(err, &conflict) || conflict.Message != "domain reload" {
			t.Fatalf("lock conflict: %v", err)
		}
		_, err = UpdateVersioned(ctx, tx, "VersionedFixture", "value=$1", 99, 1, "missing")
		if !errors.As(err, &conflict) || conflict.Code != "version_conflict" {
			t.Fatalf("missing row: %v", err)
		}
		if _, err = tx.Exec(ctx, `CREATE TABLE version_string_fixture(id text PRIMARY KEY,version bigint); INSERT INTO version_string_fixture VALUES('channel',3)`); err != nil {
			return err
		}
		_, err = LockVersion(ctx, tx, "version_string_fixture", "channel", 3, "reload")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
