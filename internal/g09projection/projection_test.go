package g09projection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type fakeRows struct {
	rows   [][]any
	index  int
	closed bool
	err    error
}

func (r *fakeRows) Next() bool {
	if r.index >= len(r.rows) {
		return false
	}
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	if r.index >= len(r.rows) {
		return errors.New("scan after end")
	}
	row := r.rows[r.index]
	if len(dest) != len(row) {
		return fmt.Errorf("destination count %d, row count %d", len(dest), len(row))
	}
	for i := range dest {
		switch ptr := dest[i].(type) {
		case *int64:
			value, ok := row[i].(int64)
			if !ok {
				return errors.New("not int64")
			}
			*ptr = value
		default:
			return errors.New("unsupported destination")
		}
	}
	r.index++
	return nil
}

func (r *fakeRows) Err() error { return r.err }
func (r *fakeRows) Close()     { r.closed = true }

type fakeQueryer struct {
	rows       map[string]*fakeRows
	calls      []string
	options    *pgx.TxOptions
	beginErr   error
	queryErrAt int
	commitErr  error
	committed  bool
	rolledBack bool
}

func (q *fakeQueryer) BeginTx(_ context.Context, options pgx.TxOptions) (Tx, error) {
	q.options = &options
	if q.beginErr != nil {
		return nil, q.beginErr
	}
	return q, nil
}

func (q *fakeQueryer) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	if len(args) != 0 {
		return nil, errors.New("unexpected query arguments")
	}
	q.calls = append(q.calls, sql)
	if q.queryErrAt > 0 && len(q.calls) == q.queryErrAt {
		return nil, errors.New("query failed")
	}
	if !strings.HasPrefix(sql, "SELECT ") {
		return nil, errors.New("non-select query")
	}
	r := q.rows[sql]
	if r == nil {
		return nil, fmt.Errorf("unexpected SQL: %s", sql)
	}
	return r, nil
}

func (q *fakeQueryer) Commit(context.Context) error {
	if q.commitErr != nil {
		return q.commitErr
	}
	q.committed = true
	return nil
}

func (q *fakeQueryer) Rollback(context.Context) error { q.rolledBack = true; return nil }

func validRows() *fakeQueryer {
	customers := make([][]any, ExpectedCustomers)
	for i := range customers {
		customers[i] = []any{int64(i + 1), int64(98000)}
	}
	services := make([][]any, ExpectedServices)
	for i := range services {
		services[i] = []any{int64(i + 1), int64(i/2 + 1)}
	}
	invoices := make([][]any, ExpectedInvoices)
	for i := range invoices {
		invoices[i] = []any{int64(i + 1), int64(i/10 + 1), int64(1000)}
	}
	suppliers := make([][]any, ExpectedSupplierAccounts)
	for i := range suppliers {
		suppliers[i] = []any{int64(i + 1)}
	}
	return &fakeQueryer{rows: map[string]*fakeRows{
		customersSQL: {rows: customers}, servicesSQL: {rows: services},
		invoicesSQL: {rows: invoices}, suppliersSQL: {rows: suppliers},
	}}
}

func TestLoadFixedReadOnlyProjection(t *testing.T) {
	q := validRows()
	loader, err := NewLoader(q, 99)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Customers) != ExpectedCustomers || len(got.Services) != ExpectedServices || len(got.Invoices) != ExpectedInvoices || len(got.SupplierAccounts) != ExpectedSupplierAccounts {
		t.Fatalf("unexpected projection counts: %+v", got)
	}
	if got.Customers[0] != (Customer{ID: 1, BalanceCents: 98000}) || got.Invoices[0] != (Invoice{ID: 1, CustomerID: 1, AmountCents: 1000}) {
		t.Fatalf("unexpected first rows: %+v %+v", got.Customers[0], got.Invoices[0])
	}
	if got.UnknownSupplierAccount.ID != 99 {
		t.Fatalf("unknown supplier was not isolated: %+v", got.UnknownSupplierAccount)
	}
	if len(q.calls) != 4 {
		t.Fatalf("expected four SELECTs, got %d", len(q.calls))
	}
	if q.options == nil || q.options.IsoLevel != pgx.RepeatableRead || q.options.AccessMode != pgx.ReadOnly {
		t.Fatalf("unexpected transaction options: %+v", q.options)
	}
	if !q.committed || q.rolledBack {
		t.Fatalf("transaction completion mismatch: committed=%t rolledBack=%t", q.committed, q.rolledBack)
	}
	for _, call := range q.calls {
		if strings.Contains(strings.ToUpper(call), "UPDATE") || strings.Contains(strings.ToUpper(call), "INSERT") || strings.Contains(strings.ToUpper(call), "DELETE") {
			t.Fatalf("mutation in query: %s", call)
		}
	}
	for sql, rows := range q.rows {
		if !rows.closed {
			t.Fatalf("rows not closed for %s", sql)
		}
	}
}

func TestLoadRejectsCountAndCrossOwner(t *testing.T) {
	q := validRows()
	q.rows[customersSQL].rows = q.rows[customersSQL].rows[:ExpectedCustomers-1]
	loader, _ := NewLoader(q, 99)
	if _, err := loader.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "customers_count") {
		t.Fatalf("expected customer count error, got %v", err)
	}

	q = validRows()
	q.rows[servicesSQL].rows[0][1] = int64(ExpectedCustomers + 1)
	loader, _ = NewLoader(q, 99)
	if _, err := loader.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "service_owner") {
		t.Fatalf("expected service ownership error, got %v", err)
	}
	if !q.rolledBack || q.committed {
		t.Fatalf("validation failure did not rollback: %+v", q)
	}
}

func TestLoadRejectsInvalidMoneyDuplicateAndUnknownCollision(t *testing.T) {
	q := validRows()
	q.rows[invoicesSQL].rows[0][2] = int64(-1)
	loader, _ := NewLoader(q, 99)
	if _, err := loader.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "invoice_value") {
		t.Fatalf("expected invoice amount error, got %v", err)
	}

	q = validRows()
	q.rows[customersSQL].rows[1][0] = int64(1)
	loader, _ = NewLoader(q, 99)
	if _, err := loader.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "customers_duplicate") {
		t.Fatalf("expected duplicate customer error, got %v", err)
	}

	q = validRows()
	loader, _ = NewLoader(q, 1)
	if _, err := loader.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "unknown_supplier_collision") {
		t.Fatalf("expected unknown supplier collision, got %v", err)
	}
	if !q.rolledBack || q.committed {
		t.Fatalf("collision did not rollback: %+v", q)
	}
}

func TestLoadQueryAndCommitFailuresRollback(t *testing.T) {
	q := validRows()
	q.queryErrAt = 3
	loader, _ := NewLoader(q, 99)
	if _, err := loader.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "invoices_query") {
		t.Fatalf("expected query failure, got %v", err)
	}
	if !q.rolledBack || q.committed {
		t.Fatalf("query failure did not rollback: %+v", q)
	}
	for _, sql := range []string{customersSQL, servicesSQL} {
		if !q.rows[sql].closed {
			t.Fatalf("previous rows leaked after later query failure: %s", sql)
		}
	}

	q = validRows()
	q.rows[servicesSQL].err = errors.New("rows failed")
	loader, _ = NewLoader(q, 99)
	if _, err := loader.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "services_rows") {
		t.Fatalf("expected rows failure, got %v", err)
	}
	if !q.rows[servicesSQL].closed || !q.rolledBack || q.committed {
		t.Fatalf("rows failure did not close and rollback: %+v", q)
	}

	q = validRows()
	q.commitErr = errors.New("commit failed")
	loader, _ = NewLoader(q, 99)
	if _, err := loader.Load(context.Background()); err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("expected commit failure, got %v", err)
	}
	if !q.rolledBack || q.committed {
		t.Fatalf("commit failure did not rollback: %+v", q)
	}
}

func TestLoaderConfigurationAndNoImplicitIOLifecycle(t *testing.T) {
	if _, err := NewLoader(nil, 99); err == nil {
		t.Fatal("nil transaction source accepted")
	}
	if _, err := NewLoader(validRows(), 0); err == nil {
		t.Fatal("invalid unknown ID accepted")
	}
	loader, err := NewLoader(validRows(), 99)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.Load(nil); err == nil {
		t.Fatal("nil context accepted")
	}
}
