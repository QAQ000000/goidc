package g09fixture

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func pureCohort(t *testing.T) (Cohort, CohortBaseline) {
	t.Helper()
	at := time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC)
	sources := make([]CohortSourceRecord, 0, 450)
	b := CohortBaseline{CapturedAtUTC: at.Add(-time.Minute), Customers: map[int64]CohortCustomerBaseline{}, Products: map[int64]CohortProductBaseline{}}
	for i := int64(1); i <= 300; i++ {
		r := CohortSourceRecord{FlowID: fmt.Sprintf("purchase-%d", i), Kind: "simulation", CustomerID: (i-1)%150 + 1, ProductID: 1, OrderID: i, InvoiceID: i, OrderItemID: i, ServiceID: i, ProvisionJobID: i, AcceptedAtUTC: at}
		if i > 270 {
			r.Kind = "supplier"
			r.SupplierAccountID = (i-271)/3 + 1
			r.BindingID = r.SupplierAccountID
			r.ProductID = r.SupplierAccountID + 1
		}
		sources = append(sources, r)
		stock := int64(1000)
		b.Products[r.ProductID] = CohortProductBaseline{Stock: &stock, Services: map[int64]bool{}}
	}
	for i := int64(1); i <= 150; i++ {
		sources = append(sources, CohortSourceRecord{FlowID: fmt.Sprintf("ticket-%d", i), Kind: "ticket", CustomerID: i, TicketID: i, EventID: i, StaffID: 10000 + i, DepartmentID: i, EmailDeliveryID: 2*i - 1, SMSDeliveryID: 2 * i, EmailOutboxID: i, SMSOutboxID: i, AcceptedAtUTC: at})
		b.Customers[i] = CohortCustomerBaseline{BalanceCents: 10000, Ledger: []CohortLedgerCheckpoint{{ID: i, DeltaCents: 10000, BalanceAfter: 10000}}}
	}
	c, err := SealCohortSources(sources)
	if err != nil {
		t.Fatal(err)
	}
	return c, b
}

func TestCohortPureSealDenominatorsAndIdentity(t *testing.T) {
	c, _ := pureCohort(t)
	for _, tc := range []struct {
		name   string
		change func([]CohortSourceRecord) []CohortSourceRecord
	}{
		{"missing-source", func(r []CohortSourceRecord) []CohortSourceRecord { return r[:449] }},
		{"extra-source", func(r []CohortSourceRecord) []CohortSourceRecord { return append(r, r[0]) }},
		{"count-cancelled-by-duplicate", func(r []CohortSourceRecord) []CohortSourceRecord { r[1] = r[0]; r[1].FlowID = "other"; return r }},
		{"same-invoice-wrong-owner", func(r []CohortSourceRecord) []CohortSourceRecord { r[1].InvoiceID = r[0].InvoiceID; return r }},
		{"same-job-different-order", func(r []CohortSourceRecord) []CohortSourceRecord { r[1].ProvisionJobID = r[0].ProvisionJobID; return r }},
		{"cross-channel-delivery-reused", func(r []CohortSourceRecord) []CohortSourceRecord {
			r[300].SMSDeliveryID = r[300].EmailDeliveryID
			return r
		}},
		{"staff-reused", func(r []CohortSourceRecord) []CohortSourceRecord { r[301].StaffID = r[300].StaffID; return r }},
		{"department-reused", func(r []CohortSourceRecord) []CohortSourceRecord { r[301].DepartmentID = r[300].DepartmentID; return r }},
		{"ticket-customer-reused", func(r []CohortSourceRecord) []CohortSourceRecord { r[301].CustomerID = r[300].CustomerID; return r }},
		{"supplier-count-compensation", func(r []CohortSourceRecord) []CohortSourceRecord {
			r[270].SupplierAccountID = r[273].SupplierAccountID
			r[270].BindingID = r[273].BindingID
			r[270].ProductID = r[273].ProductID
			return r
		}},
		{"purchase-budget", func(r []CohortSourceRecord) []CohortSourceRecord {
			r[269].Kind = "supplier"
			r[269].SupplierAccountID = 1
			r[269].BindingID = 1
			r[269].ProductID = 2
			return r
		}},
		{"missing-owner", func(r []CohortSourceRecord) []CohortSourceRecord { r[0].CustomerID = 0; return r }},
		{"zero-acceptance", func(r []CohortSourceRecord) []CohortSourceRecord { r[0].AcceptedAtUTC = time.Time{}; return r }},
		{"non-utc", func(r []CohortSourceRecord) []CohortSourceRecord {
			r[0].AcceptedAtUTC = r[0].AcceptedAtUTC.In(time.FixedZone("test", 3600))
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := SealCohortSources(tc.change(c.Sources())); err == nil {
				t.Fatal("invalid cohort sealed")
			}
		})
	}
	r := c.Sources()
	r[0].CustomerID = 90000
	if c.Sources()[0].CustomerID != 1 {
		t.Fatal("source accessor leaked mutable state")
	}
	r = c.Sources()
	sealed, err := SealCohortSources(r)
	if err != nil {
		t.Fatal(err)
	}
	r[0].InvoiceID = 90000
	if sealed.Sources()[0].InvoiceID != 1 {
		t.Fatal("sealer retained caller slice")
	}
}

func TestCohortPureBaselineFreeze(t *testing.T) {
	c, b := pureCohort(t)
	stock := int64(1000)
	b.Products[1] = CohortProductBaseline{Stock: &stock, Services: map[int64]bool{90000: false}}
	frozen, err := freezeCohortBaseline(c, b)
	if err != nil {
		t.Fatal(err)
	}
	stock = 0
	b.Products[1].Services[90000] = true
	b.Customers[1].Ledger[0].DeltaCents = 5
	delete(b.Customers, 2)
	if *frozen.Products[1].Stock != 1000 || frozen.Products[1].Services[90000] || frozen.Customers[1].Ledger[0].DeltaCents != 10000 || len(frozen.Customers) != 150 {
		t.Fatal("baseline retained caller memory")
	}
	for _, change := range []func(*CohortBaseline){
		func(b *CohortBaseline) { delete(b.Customers, 1) },
		func(b *CohortBaseline) { delete(b.Products, 1) },
		func(b *CohortBaseline) { b.Products[1].Services[1] = false },
		func(b *CohortBaseline) { x := b.Customers[1]; x.BalanceCents = 9999; b.Customers[1] = x },
		func(b *CohortBaseline) { b.CapturedAtUTC = b.CapturedAtUTC.Add(time.Hour) },
		func(b *CohortBaseline) { b.Customers[2].Ledger[0].ID = 1 },
	} {
		_, b := pureCohort(t)
		change(&b)
		if _, err := freezeCohortBaseline(c, b); err == nil {
			t.Fatal("invalid baseline accepted")
		}
	}
}

func TestCohortPureTaskTerminalProof(t *testing.T) {
	created := time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC)
	now := created.Add(time.Minute)
	finished := created.Add(time.Second)
	expires := created.Add(7 * 24 * time.Hour)
	maxAttempts := 5
	base := CohortTaskObservation{Status: "sent", Attempts: 1, MaxAttempts: &maxAttempts, CreatedAt: created, AvailableAt: created, FinishedAt: &finished, ExpiresAt: &expires}
	if err := checkCohortTask(base, now, true); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*CohortTaskObservation){
		func(t *CohortTaskObservation) { t.Attempts = 0 }, func(t *CohortTaskObservation) { t.Attempts = 2 },
		func(t *CohortTaskObservation) { t.LockedAt = &finished }, func(t *CohortTaskObservation) { t.FinishedAt = nil },
		func(t *CohortTaskObservation) { t.ExpiresAt = nil }, func(t *CohortTaskObservation) { t.Status = "queued" },
		func(t *CohortTaskObservation) { t.Status = "dead_letter" }, func(t *CohortTaskObservation) { t.AvailableAt = created.Add(-time.Second) },
	} {
		bad := base
		change(&bad)
		if checkCohortTask(bad, now, true) == nil {
			t.Fatal("invalid terminal accepted")
		}
	}
	pending := base
	pending.Status = "pending"
	pending.Attempts = 0
	pending.FinishedAt = nil
	if err := checkCohortTask(pending, now, true); err != nil {
		t.Fatal("normal pending must stay incomplete", err)
	}
	job := base
	job.Status = "succeeded"
	job.ExpiresAt = nil
	if err := checkCohortTask(job, now, false); err != nil {
		t.Fatal(err)
	}
}

func pureCohortVerifier(t *testing.T) (*CohortVerifier, *atomic.Int64) {
	t.Helper()
	c, b := pureCohort(t)
	cfg, err := pgxpool.ParseConfig("postgres://synthetic:private@127.0.0.1:1/g09_cohort_test?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg.MinConns = 0
	cfg.MaxConns = 1
	cfg.ConnConfig.RuntimeParams["search_path"] = "zjmfgo_test_0123456789abcdef"
	dials := new(atomic.Int64)
	cfg.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("pure_test_no_network")
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	config := PGObserverConfig{Database: "g09_cohort_test", Schema: "zjmfgo_test_0123456789abcdef", MaxSamples: 2, Timeout: time.Second, SupplierAccountIDs: []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}}
	for _, ids := range [][]int64{nil, {1, 2, 3, 4, 5, 6, 7, 8, 9}, {1, 2, 3, 4, 5, 6, 7, 8, 9, 11}} {
		bad := config
		bad.SupplierAccountIDs = ids
		if _, err := NewCohortVerifier(pool, bad, c, b); err == nil {
			t.Fatal("inexact account authorization accepted")
		}
	}
	v, err := NewCohortVerifier(pool, config, c, b)
	if err != nil {
		t.Fatal(err)
	}
	if dials.Load() != 0 {
		t.Fatal("constructor connected")
	}
	config.SupplierAccountIDs[0] = 90000
	if v.config.SupplierAccountIDs[0] != 1 {
		t.Fatal("account authorization slice not frozen")
	}
	return v, dials
}

func TestCohortPureFailureLatchAndNulls(t *testing.T) {
	v, dials := pureCohortVerifier(t)
	// DialFunc is a local stub, so this exercises an actual BeginTx error path
	// without opening any socket, resolving a name or connecting to PostgreSQL.
	out, err := v.Sample(t.Context())
	if err == nil || out.Status != "FAIL" || out.Complete != nil || out.ObservedAt != nil || out.InvariantsVerified != nil || out.Tasks != nil {
		t.Fatal("failed SQL sample fabricated available data")
	}
	before := dials.Load()
	_, err = v.Sample(t.Context())
	if err == nil || dials.Load() != before {
		t.Fatal("failure did not latch")
	}
	for _, state := range []int32{1, 2} {
		v, _ := pureCohortVerifier(t)
		v.state.Store(state)
		out, err := v.Sample(t.Context())
		if err == nil || out.Status != "FAIL" || v.state.Load() != 2 {
			t.Fatal("overlap did not latch")
		}
	}
	v, dials = pureCohortVerifier(t)
	v.count = v.config.MaxSamples
	if _, err = v.Sample(t.Context()); err == nil || v.state.Load() != 2 || dials.Load() != 0 {
		t.Fatal("budget violation was not refused before connection")
	}
}

func TestCohortPureConcurrentSampleLatchesBoth(t *testing.T) {
	v, _ := pureCohortVerifier(t)
	started, release := make(chan struct{}), make(chan struct{})
	cfg := v.pool.Config()
	cfg.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		close(started)
		<-release
		return nil, errors.New("private_driver_error_with_sql")
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	v.pool = pool
	done := make(chan CohortVerification, 1)
	go func() { out, _ := v.Sample(t.Context()); done <- out }()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("sample did not reach bounded local dial stub")
	}
	second, err := v.Sample(t.Context())
	close(release)
	if err == nil || second.Status != "FAIL" || second.FailureStage != "refused" {
		t.Fatal("overlap accepted")
	}
	select {
	case first := <-done:
		if first.Status != "FAIL" || first.FailureStage != "begin" || first.Complete != nil {
			t.Fatal("first overlapping sample escaped latch")
		}
	case <-time.After(time.Second):
		t.Fatal("sample did not exit")
	}
	if v.state.Load() != 2 {
		t.Fatal("latch reset")
	}
}
