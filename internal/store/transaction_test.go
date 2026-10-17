package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestWithTxCommitRollbackPanicAndDeferredFailure(t *testing.T) {
	s := isolatedTestStore(t)
	ctx := t.Context()
	if _, err := s.Pool.Exec(ctx, `CREATE TABLE tx_probe(id int PRIMARY KEY,parent int REFERENCES tx_probe(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatal(err)
	}
	insert := func(tx pgx.Tx, id, parent any) error {
		_, err := tx.Exec(ctx, "INSERT INTO tx_probe VALUES($1,$2)", id, parent)
		return err
	}
	if err := s.WithTx(ctx, func(tx pgx.Tx) error { return insert(tx, 1, nil) }); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("callback rejected")
	if err := s.WithTx(ctx, func(tx pgx.Tx) error {
		if err := insert(tx, 2, nil); err != nil {
			return err
		}
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("callback error lost: %v", err)
	}
	func() {
		defer func() {
			if recover() != "probe panic" {
				t.Fatal("callback panic lost")
			}
		}()
		_ = s.WithTx(ctx, func(tx pgx.Tx) error {
			if err := insert(tx, 3, nil); err != nil {
				t.Fatal(err)
			}
			panic("probe panic")
		})
	}()
	value, err := withTx(ctx, s, func(tx pgx.Tx) (int, error) {
		return 42, insert(tx, 4, 999)
	})
	if err == nil || value != 42 {
		t.Fatalf("deferred commit failure/result lost: %d, %v", value, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	if err = s.WithTx(canceled, func(tx pgx.Tx) error {
		if err := insert(tx, 5, nil); err != nil {
			return err
		}
		cancel()
		return nil
	}); err == nil {
		t.Fatal("canceled transaction committed")
	}
	var ids []int
	if err = s.Pool.QueryRow(ctx, "SELECT array_agg(id ORDER BY id) FROM tx_probe").Scan(&ids); err != nil || len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("failed transactions left rows: %v, %v", ids, err)
	}
}
