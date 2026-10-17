package g09fixture

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CohortTaskObservation struct {
	FlowID, Kind, Status            string
	ID                              int64
	Attempts                        int
	MaxAttempts                     *int // nil for provision jobs (no such schema column).
	CreatedAt, AvailableAt          time.Time
	LockedAt, FinishedAt, ExpiresAt *time.Time
}

type CohortVerification struct {
	Status             string // PENDING, PASS, or latched FAIL; PASS refers only to this cohort.
	FailureStage       string // Fixed safe stage code; never a SQL/driver error string.
	Sequence           int
	ObservedAt         *time.Time
	Complete           *bool // nil on unavailable/failed sample, never coerced to false/zero.
	Tasks              []CohortTaskObservation
	InvariantsVerified *bool
}

type CohortVerifier struct {
	state     atomic.Int32 // 0 ready, 1 sampling, 2 permanently failed
	pool      *pgxpool.Pool
	config    PGObserverConfig
	cohort    Cohort
	baseline  CohortBaseline
	schemaOID uint32
	count     int
}

// NewCohortVerifier freezes all caller-owned data without connecting. The same
// explicit loopback/test-database/schema/time budget as PGObserver is required.
func NewCohortVerifier(pool *pgxpool.Pool, cfg PGObserverConfig, c Cohort, b CohortBaseline) (*CohortVerifier, error) {
	if err := validateObserverConfig(pool, cfg); err != nil {
		return nil, err
	}
	frozen, err := SealCohortSources(c.Sources())
	if err != nil {
		return nil, err
	}
	baseline, err := freezeCohortBaseline(frozen, b)
	if err != nil {
		return nil, err
	}
	accounts := map[int64]bool{}
	for _, r := range frozen.sources {
		if r.SupplierAccountID > 0 {
			accounts[r.SupplierAccountID] = true
		}
	}
	if len(cfg.SupplierAccountIDs) != len(accounts) {
		return nil, errors.New("cohort_authorized_accounts_invalid")
	}
	for _, id := range cfg.SupplierAccountIDs {
		if !accounts[id] {
			return nil, errors.New("cohort_authorized_accounts_invalid")
		}
	}
	cfg.SupplierAccountIDs = append([]int64(nil), cfg.SupplierAccountIDs...)
	return &CohortVerifier{pool: pool, config: cfg, cohort: frozen, baseline: baseline}, nil
}

// Sample never changes business data or reads protocol payloads. Normal pending
// work is incomplete. Identity, SQL, financial or entity invariant errors latch
// FAIL, including overlap/budget violations. No later sample can erase a failure.
func (v *CohortVerifier) Sample(ctx context.Context) (out CohortVerification, err error) {
	out.Status = "FAIL"
	if !v.state.CompareAndSwap(0, 1) {
		v.state.Store(2)
		out.FailureStage = "refused"
		return out, errors.New("cohort_sampling_refused")
	}
	stage := "budget"
	defer func() {
		if err != nil {
			v.state.Store(2)
			out = CohortVerification{Status: "FAIL", FailureStage: stage}
			err = errors.New("cohort_sample_failed_" + stage)
		}
	}()
	if v.count >= v.config.MaxSamples {
		return out, errors.New("budget")
	}
	ctx, cancel := context.WithTimeout(ctx, v.config.Timeout)
	defer cancel()
	stage = "begin"
	tx, err := v.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	stage = "timeout"
	if _, err = tx.Exec(ctx, `SELECT pg_catalog.set_config('statement_timeout',$1,true),pg_catalog.set_config('lock_timeout',$1,true)`, fmt.Sprint(v.config.Timeout.Milliseconds())+"ms"); err != nil {
		return out, err
	}
	var db, schema, path, addr string
	var version int
	var oid uint32
	var now time.Time
	stage = "identity"
	err = tx.QueryRow(ctx, `SELECT current_database(),current_schema(),current_setting('search_path'),current_setting('server_version_num')::int,host(inet_server_addr()),n.oid,clock_timestamp() FROM pg_catalog.pg_namespace n WHERE n.nspname=$1`, v.config.Schema).Scan(&db, &schema, &path, &version, &addr, &oid, &now)
	if err != nil {
		return out, err
	}
	if db != v.config.Database || schema != v.config.Schema || path != v.config.Schema || addr != "127.0.0.1" || version < 180000 || version >= 190000 || (v.schemaOID != 0 && oid != v.schemaOID) || now.Before(v.baseline.CapturedAtUTC) {
		return out, errors.New("identity")
	}
	v.schemaOID = oid
	complete := true
	out.Tasks = make([]CohortTaskObservation, 0, 600)
	for _, r := range v.cohort.sources {
		stage = "acceptance_time"
		if r.AcceptedAtUTC.After(now) {
			return out, errors.New("acceptance_time")
		}
		var tasks []CohortTaskObservation
		if r.Kind == "ticket" {
			stage = "ticket"
			tasks, err = v.ticket(ctx, tx, r, now)
		} else {
			stage = "purchase"
			tasks, err = v.purchase(ctx, tx, r, now)
		}
		if err != nil {
			return out, err
		}
		for _, task := range tasks {
			if task.Status != "succeeded" && task.Status != "sent" {
				complete = false
			}
		}
		out.Tasks = append(out.Tasks, tasks...)
	}
	stage = "denominator"
	if len(out.Tasks) != 600 {
		return out, errors.New("denominator")
	}
	stage = "notification_identities"
	if err = v.notificationIdentities(ctx, tx); err != nil {
		return out, err
	}
	stage = "finance"
	if err = v.finance(ctx, tx); err != nil {
		return out, err
	}
	stage = "inventory"
	if err = v.inventory(ctx, tx); err != nil {
		return out, err
	}
	stage = "commit"
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	v.count++
	out.Sequence = v.count
	out.ObservedAt = &now
	out.Complete = &complete
	ok := true
	out.InvariantsVerified = &ok
	out.Status = "PENDING"
	if complete {
		out.Status = "PASS"
	}
	stage = "overlap"
	if !v.state.CompareAndSwap(1, 0) {
		return out, errors.New("overlap")
	}
	return out, nil
}

func (v *CohortVerifier) table(name string) string {
	return pgx.Identifier{v.config.Schema, name}.Sanitize()
}

func (v *CohortVerifier) purchase(ctx context.Context, tx pgx.Tx, r CohortSourceRecord, now time.Time) ([]CohortTaskObservation, error) {
	q := `SELECT o.status,i.status,i.paid_at,s.status,s.upstream_id,j.status,j.attempts,j.created_at,j.available_at,j.locked_at,j.completed_at,
 (o.total_cents=1000 AND i.amount_cents=1000 AND i.invoice_kind='automatic' AND oi.quantity=1 AND oi.total_cents=1000 AND s.unit_number=1 AND NOT s.inventory_released AND s.provision_mode=$8),
 (SELECT count(*) FROM ` + v.table("order_items") + ` x WHERE x.order_id=o.id),
 (SELECT count(*) FROM ` + v.table("services") + ` x WHERE x.order_id=o.id),
 (SELECT count(*) FROM ` + v.table("provision_jobs") + ` x WHERE x.service_id=s.id AND x.action='create'),
 (SELECT count(*) FROM ` + v.table("balance_ledger") + ` l WHERE l.reference_type='invoice' AND l.reference_id=i.id),
 (SELECT count(*) FROM ` + v.table("balance_ledger") + ` l WHERE l.reference_type='invoice' AND l.reference_id=i.id AND l.entry_type='payment' AND l.user_id=$2 AND l.delta_cents=-1000),
 (SELECT count(*) FROM ` + v.table("payment_transactions") + ` p WHERE p.invoice_id=i.id),
 (SELECT count(*) FROM ` + v.table("credit_invoice_payments") + ` p WHERE p.invoice_id=i.id),
 (SELECT count(*) FROM ` + v.table("external_refunds") + ` f WHERE f.invoice_id=i.id),
 coalesce(s.delivery_snapshot->>'supplier_id',''),coalesce(s.delivery_snapshot->>'binding_id',''),
 (SELECT count(*) FROM ` + v.table("supplier_product_bindings") + ` b JOIN ` + v.table("supplier_accounts") + ` a ON a.id=b.supplier_id WHERE b.id=$9 AND b.product_id=$5 AND a.id=$10 AND a.active),
 o.created_at,i.created_at,s.created_at
 FROM ` + v.table("orders") + ` o JOIN ` + v.table("invoices") + ` i ON i.order_id=o.id AND i.id=$3 AND i.user_id=$2
 JOIN ` + v.table("order_items") + ` oi ON oi.order_id=o.id AND oi.id=$4 AND oi.product_id=$5
 JOIN ` + v.table("services") + ` s ON s.order_id=o.id AND s.order_item_id=oi.id AND s.id=$6 AND s.product_id=$5 AND s.user_id=$2
 JOIN ` + v.table("provision_jobs") + ` j ON j.service_id=s.id AND j.id=$7 AND j.action='create'
 WHERE o.id=$1 AND o.user_id=$2`
	t := CohortTaskObservation{FlowID: r.FlowID, Kind: r.Kind, ID: r.ProvisionJobID}
	var orderStatus, invoiceStatus, serviceStatus, upstream, supplier, binding string
	var paid *time.Time
	var valid bool
	var itemN, serviceN, jobN, ledgerN, paymentN, externalN, creditN, refundN, bindingN int64
	var orderAt, invoiceAt, serviceAt time.Time
	err := tx.QueryRow(ctx, q, r.OrderID, r.CustomerID, r.InvoiceID, r.OrderItemID, r.ProductID, r.ServiceID, r.ProvisionJobID, r.Kind, r.BindingID, r.SupplierAccountID).Scan(&orderStatus, &invoiceStatus, &paid, &serviceStatus, &upstream, &t.Status, &t.Attempts, &t.CreatedAt, &t.AvailableAt, &t.LockedAt, &t.FinishedAt, &valid, &itemN, &serviceN, &jobN, &ledgerN, &paymentN, &externalN, &creditN, &refundN, &supplier, &binding, &bindingN, &orderAt, &invoiceAt, &serviceAt)
	if err != nil {
		return nil, err
	}
	if !valid || (orderStatus != "paid" && orderStatus != "active") || invoiceStatus != "paid" || paid == nil || paid.Before(invoiceAt) || paid.After(r.AcceptedAtUTC) || itemN != 1 || serviceN != 1 || jobN != 1 || ledgerN != 1 || paymentN != 1 || externalN != 0 || creditN != 0 || refundN != 0 {
		return nil, errors.New("purchase_financial_mapping")
	}
	for _, at := range []time.Time{orderAt, invoiceAt, serviceAt, t.CreatedAt} {
		if at.Before(v.baseline.CapturedAtUTC) || at.After(r.AcceptedAtUTC) {
			return nil, errors.New("purchase_time")
		}
	}
	if r.Kind == "supplier" && (supplier != fmt.Sprint(r.SupplierAccountID) || binding != fmt.Sprint(r.BindingID) || bindingN != 1) {
		return nil, errors.New("supplier_mapping")
	}
	if r.Kind == "simulation" && (supplier != "" || binding != "" || bindingN != 0) {
		return nil, errors.New("simulation_mapping")
	}
	if err = checkCohortTask(t, now, false); err != nil {
		return nil, err
	}
	if (t.Status == "succeeded" && (serviceStatus != "active" || upstream == "")) || (t.Status == "pending" && serviceStatus != "queued") || (t.Status == "running" && serviceStatus != "queued" && serviceStatus != "provisioning") {
		return nil, errors.New("service_job_state")
	}
	return []CohortTaskObservation{t}, nil
}

func checkCohortTask(t CohortTaskObservation, now time.Time, outbox bool) error {
	bad := errors.New("cohort_task_state_invalid")
	if t.CreatedAt.IsZero() || t.AvailableAt.Before(t.CreatedAt) || t.CreatedAt.After(now) || t.Attempts < 0 || t.Attempts > 1 {
		return bad
	}
	terminal, running := "succeeded", "running"
	if outbox {
		terminal, running = "sent", "sending"
		if t.MaxAttempts == nil || *t.MaxAttempts < 1 || *t.MaxAttempts > 20 || t.Attempts > *t.MaxAttempts || t.ExpiresAt == nil || t.ExpiresAt.Sub(t.CreatedAt) < 7*24*time.Hour-time.Minute || t.ExpiresAt.Sub(t.CreatedAt) > 7*24*time.Hour+time.Minute {
			return bad
		}
	}
	switch t.Status {
	case "pending":
		if t.Attempts != 0 || t.LockedAt != nil || t.FinishedAt != nil {
			return bad
		}
	case running:
		if t.Attempts != 1 || t.LockedAt == nil || t.FinishedAt != nil || t.LockedAt.Before(t.CreatedAt) || t.LockedAt.After(now) {
			return bad
		}
	case terminal:
		if t.Attempts != 1 || t.LockedAt != nil || t.FinishedAt == nil || t.FinishedAt.Before(t.CreatedAt) || t.FinishedAt.After(now) || (t.ExpiresAt != nil && !t.FinishedAt.Before(*t.ExpiresAt)) {
			return bad
		}
	default:
		return bad
	}
	if outbox && t.Status != terminal && !t.ExpiresAt.After(now) {
		return bad
	}
	return nil
}
