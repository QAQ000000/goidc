package g09fixture

// Test-fixture-only, bounded read-only snapshots. No scheduler, DSN output,
// query-text collection, recovery, state mutation or capacity certification.
import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var observerSchema = regexp.MustCompile(`^zjmfgo_test_[a-f0-9]{16}$`)
var observerDatabase = regexp.MustCompile(`^[a-z][a-z0-9_]{0,57}_test$`)

type PGObserverConfig struct {
	Database, Schema   string
	SupplierAccountIDs []int64
	MaxSamples         int
	Timeout            time.Duration
}

type QueueObservation struct {
	Queue, Status        string
	Count, Ready, Future int64
	OldestReadySeconds   *float64 // nil means no ready task, never zero by coercion.
}

type SupplierLeaseObservation struct {
	AccountID    int64
	RunningJobID *int64
	JobStatus    *string
	LockedAt     *time.Time
	// running_job_id is a retained account ownership fact, not an expiry timer.
}

type DatabaseObservation struct {
	ActivityUnavailableReason, LocksUnavailableReason   string
	Scope                                               string
	Available                                           bool
	UnavailableReason                                   string
	Connections, Active, IdleInTransaction, LockWaiting *int64
	Deadlocks, Commits, Rollbacks                       *int64
	StatsReset                                          *time.Time
	ActivityDetailsAvailable                            bool
	WaitingDatabaseTaggedLocks                          *int64
	LocksAvailable                                      bool
}

type WALObservation struct {
	Scope                   string
	Available               bool
	UnavailableReason       string
	Records, FullPageImages *int64
	Bytes                   *string // Numeric text preserves counters above IEEE-754 exact integers.
	StatsReset              *time.Time
}

type PGObservation struct {
	Sequence         int
	ObservedAt       time.Time
	Queues           []QueueObservation
	SupplierLeases   []SupplierLeaseObservation
	Database         DatabaseObservation
	WAL              WALObservation
	CapacityVerified bool
}

type PGObserver struct {
	mu        sync.Mutex
	pool      *pgxpool.Pool
	config    PGObserverConfig
	schemaOID uint32
	count     int
	failed    bool
}

func validateObserverConfig(pool *pgxpool.Pool, c PGObserverConfig) error {
	if pool == nil || !observerDatabase.MatchString(c.Database) || !observerSchema.MatchString(c.Schema) || c.MaxSamples < 1 || c.MaxSamples > 1000 || c.Timeout < time.Millisecond || c.Timeout > 5*time.Second || len(c.SupplierAccountIDs) > 512 {
		return errors.New("pg_observer_configuration_invalid")
	}
	cfg := pool.Config().ConnConfig
	if cfg.Database != c.Database || cfg.Host != "127.0.0.1" || cfg.Port == 0 || cfg.RuntimeParams["search_path"] != c.Schema {
		return errors.New("pg_observer_pool_identity_invalid")
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback.Host != "127.0.0.1" || fallback.Port != cfg.Port {
			return errors.New("pg_observer_pool_identity_invalid")
		}
	}
	seen := map[int64]bool{}
	for _, id := range c.SupplierAccountIDs {
		if id <= 0 || seen[id] {
			return errors.New("pg_observer_accounts_invalid")
		}
		seen[id] = true
	}
	return nil
}

// NewPGObserver freezes the explicit authorization and budget without connecting.
// The caller must own this pool, with no query tracer that records secrets.
// Physical-session and schema identity are verified inside every sample.
func NewPGObserver(pool *pgxpool.Pool, config PGObserverConfig) (*PGObserver, error) {
	if err := validateObserverConfig(pool, config); err != nil {
		return nil, err
	}
	config.SupplierAccountIDs = append([]int64(nil), config.SupplierAccountIDs...)
	return &PGObserver{pool: pool, config: config}, nil
}

func (o *PGObserver) Sample(ctx context.Context) (out PGObservation, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failed || o.count >= o.config.MaxSamples {
		o.failed = true
		return out, errors.New("pg_observer_sampling_refused")
	}
	defer func() {
		if err != nil {
			o.failed = true
			out = PGObservation{}
			err = errors.New("pg_observer_sample_failed")
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, o.config.Timeout)
	defer cancel()
	tx, err := o.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	// Bound server work too, including relation-lock waits; SET LOCAL via the
	// function remains transaction-local, never changing pool defaults.
	if _, err = tx.Exec(ctx, `SELECT pg_catalog.set_config('statement_timeout',$1,true),pg_catalog.set_config('lock_timeout',$1,true)`, fmt.Sprint(o.config.Timeout.Milliseconds())+"ms"); err != nil {
		return out, err
	}
	var db, schema, searchPath, addr string
	var version int
	var oid uint32
	var walAllowed, activityAllowed, locksAllowed, statsAllowed bool
	err = tx.QueryRow(ctx, `SELECT current_database(),current_schema(),current_setting('search_path'),current_setting('server_version_num')::int,host(inet_server_addr()),n.oid,
 pg_catalog.has_table_privilege('pg_catalog.pg_stat_wal','SELECT'),pg_catalog.has_table_privilege('pg_catalog.pg_stat_activity','SELECT'),pg_catalog.has_table_privilege('pg_catalog.pg_locks','SELECT'),pg_catalog.has_table_privilege('pg_catalog.pg_stat_database','SELECT'),clock_timestamp()
 FROM pg_catalog.pg_namespace n WHERE n.nspname=$1`, o.config.Schema).Scan(&db, &schema, &searchPath, &version, &addr, &oid, &walAllowed, &activityAllowed, &locksAllowed, &statsAllowed, &out.ObservedAt)
	if err != nil {
		return out, err
	}
	if db != o.config.Database || schema != o.config.Schema || searchPath != o.config.Schema || version < 180000 || version >= 190000 || addr != "127.0.0.1" || (o.schemaOID != 0 && oid != o.schemaOID) {
		return out, errors.New("identity")
	}
	if o.schemaOID == 0 {
		o.schemaOID = oid
	}
	out.Queues = []QueueObservation{}
	for _, table := range []string{"provision_jobs", "email_outbox", "sms_outbox"} {
		// Identifiers originate exclusively in the validated frozen schema and fixed list.
		rows, queryErr := tx.Query(ctx, `SELECT status,count(*),count(*) FILTER(WHERE status='pending' AND available_at<=$1::timestamptz),count(*) FILTER(WHERE status='pending' AND available_at>$1::timestamptz),max(extract(epoch FROM ($1::timestamptz-created_at))) FILTER(WHERE status='pending' AND available_at<=$1::timestamptz)::float8 FROM `+pgx.Identifier{o.config.Schema, table}.Sanitize()+` GROUP BY status ORDER BY status LIMIT 32`, out.ObservedAt)
		if queryErr != nil {
			return out, queryErr
		}
		for rows.Next() {
			row := QueueObservation{Queue: table}
			if err = rows.Scan(&row.Status, &row.Count, &row.Ready, &row.Future, &row.OldestReadySeconds); err != nil {
				rows.Close()
				return out, err
			}
			switch row.Status {
			case "pending", "running", "sending", "sent", "succeeded", "failed", "cancelled", "expired", "waiting_manual", "waiting_verification", "dead_letter":
			default:
				rows.Close()
				return out, errors.New("queue_status_invalid")
			}
			out.Queues = append(out.Queues, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	out.SupplierLeases = []SupplierLeaseObservation{}
	if len(o.config.SupplierAccountIDs) > 0 {
		rows, queryErr := tx.Query(ctx, `SELECT a.id,a.running_job_id,j.status,j.locked_at FROM `+pgx.Identifier{o.config.Schema, "supplier_accounts"}.Sanitize()+` a LEFT JOIN `+pgx.Identifier{o.config.Schema, "provision_jobs"}.Sanitize()+` j ON j.id=a.running_job_id WHERE a.id=ANY($1::bigint[]) ORDER BY a.id`, o.config.SupplierAccountIDs)
		if queryErr != nil {
			return out, queryErr
		}
		for rows.Next() {
			var row SupplierLeaseObservation
			if err = rows.Scan(&row.AccountID, &row.RunningJobID, &row.JobStatus, &row.LockedAt); err != nil {
				rows.Close()
				return out, err
			}
			out.SupplierLeases = append(out.SupplierLeases, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		if len(out.SupplierLeases) != len(o.config.SupplierAccountIDs) {
			return out, errors.New("account_missing")
		}
	}
	out.Database = DatabaseObservation{Scope: "authorized_database_all_schemas_including_observer", UnavailableReason: "insufficient_privilege", ActivityUnavailableReason: "insufficient_privilege", LocksUnavailableReason: "insufficient_privilege"}
	if statsAllowed {
		err = tx.QueryRow(ctx, `SELECT numbackends::bigint,deadlocks,xact_commit,xact_rollback,stats_reset FROM pg_catalog.pg_stat_database WHERE datname=$1`, o.config.Database).Scan(&out.Database.Connections, &out.Database.Deadlocks, &out.Database.Commits, &out.Database.Rollbacks, &out.Database.StatsReset)
		if err != nil {
			return out, err
		}
		out.Database.Available = out.Database.Connections != nil && out.Database.Deadlocks != nil && out.Database.Commits != nil && out.Database.Rollbacks != nil
		if out.Database.Available {
			out.Database.UnavailableReason = ""
		} else {
			out.Database.UnavailableReason = "statistics_unavailable"
		}
	}
	if activityAllowed {
		var redacted int64
		err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE state='active'),count(*) FILTER(WHERE state IN ('idle in transaction','idle in transaction (aborted)')),count(*) FILTER(WHERE wait_event_type='Lock'),count(*) FILTER(WHERE state IS NULL) FROM pg_catalog.pg_stat_activity WHERE datname=$1`, o.config.Database).Scan(&out.Database.Active, &out.Database.IdleInTransaction, &out.Database.LockWaiting, &redacted)
		if err != nil {
			return out, err
		}
		out.Database.ActivityDetailsAvailable = redacted == 0
		out.Database.ActivityUnavailableReason = ""
		if redacted > 0 {
			out.Database.ActivityUnavailableReason = "privilege_redacted"
			out.Database.Active = nil
			out.Database.IdleInTransaction = nil
			out.Database.LockWaiting = nil
		}
	}
	if locksAllowed {
		err = tx.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_locks WHERE database=(SELECT oid FROM pg_catalog.pg_database WHERE datname=$1) AND NOT granted`, o.config.Database).Scan(&out.Database.WaitingDatabaseTaggedLocks)
		if err != nil {
			return out, err
		}
		out.Database.LocksAvailable = true
		out.Database.LocksUnavailableReason = ""
	}
	out.WAL = WALObservation{Scope: "whole_cluster_not_schema_or_database_attributable", UnavailableReason: "insufficient_privilege"}
	if walAllowed {
		err = tx.QueryRow(ctx, `SELECT wal_records,wal_fpi,wal_bytes::text,stats_reset FROM pg_catalog.pg_stat_wal`).Scan(&out.WAL.Records, &out.WAL.FullPageImages, &out.WAL.Bytes, &out.WAL.StatsReset)
		if err != nil {
			return out, err
		}
		out.WAL.Available = out.WAL.Records != nil && out.WAL.FullPageImages != nil && out.WAL.Bytes != nil
		if out.WAL.Available {
			out.WAL.UnavailableReason = ""
		} else {
			out.WAL.UnavailableReason = "statistics_unavailable"
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	o.count++
	out.Sequence = o.count
	return out, nil
}
