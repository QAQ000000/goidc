package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/codex/zjmfgo/internal/provision"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	SupplierDownstreamURL      string
	Pool                       *pgxpool.Pool
	Provisioners               map[string]*provision.NOKVM
	ProvisionEncryptionKey     []byte
	PurchaseFieldEncryptionKey []byte
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, err
	}
	var version int
	if err = p.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil {
		p.Close()
		return nil, err
	}
	if version < 180000 {
		p.Close()
		return nil, fmt.Errorf("PostgreSQL 18 or newer is required")
	}
	return &Store{Pool: p}, nil
}
func (s *Store) Close() { s.Pool.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108188)"); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return err
	}
	entries, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)
	for _, name := range entries {
		content, err := migrations.Files.ReadFile(name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		checksum := hex.EncodeToString(sum[:])
		if exists {
			var saved string
			err = tx.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version=$1", name).Scan(&saved)
			if err == nil {
				if saved != checksum {
					return fmt.Errorf("migration %s checksum mismatch", name)
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		// Exec without parameters uses PostgreSQL's simple protocol; SQL is parsed by PostgreSQL.
		if _, err = tx.Exec(ctx, string(content)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)", name, checksum); err != nil {
			return err
		}
		exists = true
	}
	return tx.Commit(ctx)
}

func (s *Store) CheckSchema(ctx context.Context) error {
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		return err
	}
	for _, name := range names {
		content, _ := migrations.Files.ReadFile(name)
		sum := sha256.Sum256(content)
		var checksum string
		if err = s.Pool.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version=$1", name).Scan(&checksum); err != nil {
			return fmt.Errorf("schema unavailable: run go run ./cmd/migrate")
		}
		if !strings.EqualFold(checksum, hex.EncodeToString(sum[:])) {
			return fmt.Errorf("schema checksum mismatch: %s", name)
		}
	}
	return nil
}
