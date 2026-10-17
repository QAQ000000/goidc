// Package g09projection contains the bounded, read-only database projection
// consumed by the G09 capacity allocator. It has no write path and does not
// start a service or read deployment configuration.
package g09projection

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ExpectedCustomers        = 1000
	ExpectedServices         = 2000
	ExpectedInvoices         = 10000
	ExpectedSupplierAccounts = 10
)

// Rows is the small part of pgx.Rows needed by this loader. Keeping the
// interface narrow permits contract tests to use in-memory rows and prevents
// the projection from acquiring mutation or transaction capabilities.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

// Tx is the read-only transaction seam used by the loader and unit tests. It
// intentionally exposes no Exec or Copy method.
type Tx interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// TxBeginner is the only dependency accepted by Loader. Production callers
// use the pool adapter below; tests inject an in-memory transaction.
type TxBeginner interface {
	BeginTx(ctx context.Context, options pgx.TxOptions) (Tx, error)
}

// Customer is the only customer data admitted to the capacity allocator.
type Customer struct {
	ID           int64
	BalanceCents int64
}

type Service struct {
	ID         int64
	CustomerID int64
}

type Invoice struct {
	ID          int64
	CustomerID  int64
	AmountCents int64
}

type SupplierAccount struct {
	ID int64
}

// Projection is detached from database rows. UnknownSupplierAccount is a
// synthetic, isolated identity and is never loaded from supplier_accounts.
type Projection struct {
	Customers              []Customer
	Services               []Service
	Invoices               []Invoice
	SupplierAccounts       []SupplierAccount
	UnknownSupplierAccount SupplierAccount
}

// Loader validates the synthetic unknown supplier ID once and executes only
// the four fixed SELECT statements below. The configured ID must not collide
// with a real supplier account.
type Loader struct {
	beginner          TxBeginner
	unknownSupplierID int64
}

// NewLoader creates a loader over an explicitly injected transaction source.
// It performs no I/O.
func NewLoader(beginner TxBeginner, unknownSupplierID int64) (*Loader, error) {
	if beginner == nil || unknownSupplierID <= 0 {
		return nil, errors.New("g09_projection_configuration_invalid")
	}
	return &Loader{beginner: beginner, unknownSupplierID: unknownSupplierID}, nil
}

type poolBeginner struct{ pool *pgxpool.Pool }
type poolTx struct{ tx pgx.Tx }

func (p poolBeginner) BeginTx(ctx context.Context, options pgx.TxOptions) (Tx, error) {
	if p.pool == nil {
		return nil, errors.New("g09_projection_pool_nil")
	}
	tx, err := p.pool.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return poolTx{tx: tx}, nil
}

func (p poolTx) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return p.tx.Query(ctx, sql, args...)
}
func (p poolTx) Commit(ctx context.Context) error   { return p.tx.Commit(ctx) }
func (p poolTx) Rollback(ctx context.Context) error { return p.tx.Rollback(ctx) }

// NewPoolLoader adapts an explicitly supplied pool. It does not connect,
// ping, change pool settings, or read any environment/configuration. Load
// opens one RepeatableRead, ReadOnly transaction for all four SELECTs.
func NewPoolLoader(pool *pgxpool.Pool, unknownSupplierID int64) (*Loader, error) {
	return NewLoader(poolBeginner{pool: pool}, unknownSupplierID)
}

// Load performs four bounded read queries and validates the detached result.
// Every identifier and column in these statements is a package constant; no
// caller-controlled table or column name can enter SQL.
func (l *Loader) Load(ctx context.Context) (Projection, error) {
	if l == nil || l.beginner == nil || ctx == nil {
		return Projection{}, errors.New("g09_projection_configuration_invalid")
	}
	tx, err := l.beginner.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Projection{}, fmt.Errorf("g09_projection_begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	customers, err := loadCustomers(ctx, tx)
	if err != nil {
		return Projection{}, err
	}
	services, err := loadServices(ctx, tx)
	if err != nil {
		return Projection{}, err
	}
	invoices, err := loadInvoices(ctx, tx)
	if err != nil {
		return Projection{}, err
	}
	suppliers, err := loadSuppliers(ctx, tx)
	if err != nil {
		return Projection{}, err
	}
	if err := validate(customers, services, invoices, suppliers, l.unknownSupplierID); err != nil {
		return Projection{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Projection{}, fmt.Errorf("g09_projection_commit: %w", err)
	}
	committed = true
	return Projection{
		Customers:              append([]Customer(nil), customers...),
		Services:               append([]Service(nil), services...),
		Invoices:               append([]Invoice(nil), invoices...),
		SupplierAccounts:       append([]SupplierAccount(nil), suppliers...),
		UnknownSupplierAccount: SupplierAccount{ID: l.unknownSupplierID},
	}, nil
}

const customersSQL = `SELECT id,balance_cents FROM users WHERE role='customer' AND status='active' ORDER BY id LIMIT 1001`
const servicesSQL = `SELECT id,user_id FROM services ORDER BY id LIMIT 2001`
const invoicesSQL = `SELECT id,user_id,amount_cents FROM invoices ORDER BY id LIMIT 10001`
const suppliersSQL = `SELECT id FROM supplier_accounts WHERE active ORDER BY id LIMIT 11`

func loadCustomers(ctx context.Context, q Tx) ([]Customer, error) {
	rows, err := q.Query(ctx, customersSQL)
	if err != nil {
		return nil, fmt.Errorf("g09_projection_customers_query: %w", err)
	}
	return scanRows(rows, ExpectedCustomers, "customers", func(rows Rows) (Customer, error) {
		var row Customer
		if err := rows.Scan(&row.ID, &row.BalanceCents); err != nil {
			return Customer{}, err
		}
		return row, nil
	})
}

func loadServices(ctx context.Context, q Tx) ([]Service, error) {
	rows, err := q.Query(ctx, servicesSQL)
	if err != nil {
		return nil, fmt.Errorf("g09_projection_services_query: %w", err)
	}
	return scanRows(rows, ExpectedServices, "services", func(rows Rows) (Service, error) {
		var row Service
		if err := rows.Scan(&row.ID, &row.CustomerID); err != nil {
			return Service{}, err
		}
		return row, nil
	})
}

func loadInvoices(ctx context.Context, q Tx) ([]Invoice, error) {
	rows, err := q.Query(ctx, invoicesSQL)
	if err != nil {
		return nil, fmt.Errorf("g09_projection_invoices_query: %w", err)
	}
	return scanRows(rows, ExpectedInvoices, "invoices", func(rows Rows) (Invoice, error) {
		var row Invoice
		if err := rows.Scan(&row.ID, &row.CustomerID, &row.AmountCents); err != nil {
			return Invoice{}, err
		}
		return row, nil
	})
}

func loadSuppliers(ctx context.Context, q Tx) ([]SupplierAccount, error) {
	rows, err := q.Query(ctx, suppliersSQL)
	if err != nil {
		return nil, fmt.Errorf("g09_projection_suppliers_query: %w", err)
	}
	return scanRows(rows, ExpectedSupplierAccounts, "suppliers", func(rows Rows) (SupplierAccount, error) {
		var row SupplierAccount
		if err := rows.Scan(&row.ID); err != nil {
			return SupplierAccount{}, err
		}
		return row, nil
	})
}

func scanRows[T any](rows Rows, expected int, label string, scan func(Rows) (T, error)) ([]T, error) {
	if rows == nil {
		return nil, fmt.Errorf("g09_projection_%s_rows_nil", label)
	}
	defer rows.Close()
	out := make([]T, 0, expected)
	for rows.Next() {
		row, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("g09_projection_%s_scan: %w", label, err)
		}
		out = append(out, row)
		if len(out) > expected {
			return nil, fmt.Errorf("g09_projection_%s_count", label)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("g09_projection_%s_rows: %w", label, err)
	}
	if len(out) != expected {
		return nil, fmt.Errorf("g09_projection_%s_count", label)
	}
	return out, nil
}

func validate(customers []Customer, services []Service, invoices []Invoice, suppliers []SupplierAccount, unknownID int64) error {
	customerIDs := make(map[int64]struct{}, len(customers))
	for _, row := range customers {
		if row.ID <= 0 || row.BalanceCents < 0 {
			return errors.New("g09_projection_customer_value_invalid")
		}
		if _, exists := customerIDs[row.ID]; exists {
			return errors.New("g09_projection_customers_duplicate")
		}
		customerIDs[row.ID] = struct{}{}
	}
	serviceIDs := make(map[int64]struct{}, len(services))
	for _, row := range services {
		if row.ID <= 0 || row.CustomerID <= 0 {
			return errors.New("g09_projection_service_value_invalid")
		}
		if _, exists := customerIDs[row.CustomerID]; !exists {
			return errors.New("g09_projection_service_owner_invalid")
		}
		if _, exists := serviceIDs[row.ID]; exists {
			return errors.New("g09_projection_services_duplicate")
		}
		serviceIDs[row.ID] = struct{}{}
	}
	invoiceIDs := make(map[int64]struct{}, len(invoices))
	for _, row := range invoices {
		if row.ID <= 0 || row.CustomerID <= 0 || row.AmountCents < 0 {
			return errors.New("g09_projection_invoice_value_invalid")
		}
		if _, exists := customerIDs[row.CustomerID]; !exists {
			return errors.New("g09_projection_invoice_owner_invalid")
		}
		if _, exists := invoiceIDs[row.ID]; exists {
			return errors.New("g09_projection_invoices_duplicate")
		}
		invoiceIDs[row.ID] = struct{}{}
	}
	supplierIDs := make(map[int64]struct{}, len(suppliers))
	for _, row := range suppliers {
		if row.ID <= 0 {
			return errors.New("g09_projection_supplier_value_invalid")
		}
		if _, exists := supplierIDs[row.ID]; exists {
			return errors.New("g09_projection_suppliers_duplicate")
		}
		supplierIDs[row.ID] = struct{}{}
	}
	if unknownID <= 0 {
		return errors.New("g09_projection_unknown_supplier_invalid")
	}
	if _, exists := supplierIDs[unknownID]; exists {
		return errors.New("g09_projection_unknown_supplier_collision")
	}
	return nil
}

var _ TxBeginner = poolBeginner{}
var _ Tx = poolTx{}
var _ Rows = (pgx.Rows)(nil)
