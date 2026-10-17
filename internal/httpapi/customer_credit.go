package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type customerCreditAccount struct {
	DisplayEnabled bool      `json:"display_enabled"`
	PaymentEnabled bool      `json:"payment_enabled"`
	LimitCents     int64     `json:"limit_cents"`
	UsedCents      int64     `json:"used_cents"`
	AvailableCents int64     `json:"available_cents"`
	Frozen         bool      `json:"frozen"`
	Version        int64     `json:"version"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type customerCreditConsumption struct {
	InvoiceID      int64      `json:"invoice_id"`
	InvoiceNo      string     `json:"invoice_no"`
	AmountCents    int64      `json:"amount_cents"`
	RemainingCents int64      `json:"remaining_cents"`
	Status         string     `json:"status"`
	Version        int64      `json:"version"`
	CreatedAt      time.Time  `json:"created_at"`
	RefundedAt     *time.Time `json:"refunded_at"`
}
type customerCreditRepayment struct {
	ID          int64     `json:"id"`
	InvoiceID   int64     `json:"invoice_id"`
	InvoiceNo   string    `json:"invoice_no"`
	AmountCents int64     `json:"amount_cents"`
	ReceivedAt  time.Time `json:"received_at"`
	CreatedAt   time.Time `json:"created_at"`
}
type customerCreditDetail struct {
	customerCreditConsumption
	RepaidCents int64 `json:"repaid_cents"`
}
type customerCreditPage[T any] struct {
	Items  []T   `json:"items"`
	Total  int64 `json:"total"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}

// Set cache policy before authentication, including authentication failures.
func (s *Server) customerCreditAuth(next http.HandlerFunc) http.HandlerFunc {
	authenticated := s.sessionAuth(func(w http.ResponseWriter, r *http.Request) {
		if identityFrom(r).Role != "customer" {
			writeError(w, 403, "forbidden", "需要客户会话")
			return
		}
		next(w, r)
	}, false)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		authenticated(w, r)
	}
}
func customerCreditQuery(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	for key, values := range r.URL.Query() {
		found := false
		for _, a := range allowed {
			if key == a {
				found = true
				break
			}
		}
		if !found || len(values) != 1 {
			writeError(w, 400, "invalid_credit_query", "信用查询参数无效")
			return false
		}
	}
	return true
}
func (s *Server) customerCreditAccount(w http.ResponseWriter, r *http.Request) {
	if !customerCreditQuery(w, r) {
		return
	}
	var out customerCreditAccount
	err := s.db.Pool.QueryRow(r.Context(), `SELECT (s.admin_settings->>'frontend_credit_enabled')::boolean,c.enabled,a.limit_cents,a.used_cents,GREATEST(a.limit_cents-a.used_cents,0),a.frozen,a.version,a.updated_at FROM customer_credit_accounts a CROSS JOIN credit_settings c CROSS JOIN site_settings s WHERE a.user_id=$1 AND c.id=1 AND s.id=1`, identityFrom(r).ID).Scan(&out.DisplayEnabled, &out.PaymentEnabled, &out.LimitCents, &out.UsedCents, &out.AvailableCents, &out.Frozen, &out.Version, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		s.log.Error("customer credit account/settings missing", "customer_id", identityFrom(r).ID)
		writeError(w, 503, "credit_account_unavailable", "信用账户暂不可用，请重试或联系管理员")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}

type customerCreditFilter struct {
	from, to      *time.Time
	invoice       *int64
	q, status     string
	limit, offset int
}

func parseCustomerCreditFilter(w http.ResponseWriter, r *http.Request, consumptions bool) (customerCreditFilter, bool) {
	var f customerCreditFilter
	allowed := []string{"q", "date_from", "date_to", "invoice_id", "limit", "offset"}
	if consumptions {
		allowed = append(allowed, "status")
	}
	if !customerCreditQuery(w, r, allowed...) {
		return f, false
	}
	f.q = strings.TrimSpace(r.URL.Query().Get("q"))
	if !utf8.ValidString(f.q) || utf8.RuneCountInString(f.q) > 100 || strings.ContainsRune(f.q, 0) {
		writeError(w, 400, "invalid_credit_query", "搜索内容无效或过长")
		return f, false
	}
	f.q = "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.q) + "%"
	// The shared parser enforces Shanghai natural days and debt status values.
	var ok bool
	_, f.from, f.to, f.status, ok = creditFilters(w, r)
	if !ok {
		return f, false
	}
	if raw := r.URL.Query().Get("invoice_id"); raw != "" {
		id, err := parsePositiveID(raw)
		if err != nil {
			writeError(w, 400, "invalid_invoice", "账单ID无效")
			return f, false
		}
		f.invoice = &id
	}
	f.limit, f.offset = pagination(r)
	return f, true
}

const customerConsumptionColumns = `c.invoice_id,i.invoice_no,c.amount_cents,c.remaining_cents,c.status,c.version,c.created_at,c.refunded_at`
const customerConsumptionFrom = ` FROM credit_invoice_payments c JOIN invoices i ON i.id=c.invoice_id AND i.user_id=c.user_id WHERE c.user_id=$1 AND (i.invoice_no ILIKE $2 OR i.id::text ILIKE $2) AND ($3::bigint IS NULL OR i.id=$3) AND ($4::timestamptz IS NULL OR c.created_at>=$4) AND ($5::timestamptz IS NULL OR c.created_at<$5) AND ($6='' OR c.status=$6)`
const customerRepaymentFrom = ` FROM credit_repayments p JOIN credit_invoice_payments c ON c.invoice_id=p.invoice_id AND c.user_id=p.user_id JOIN invoices i ON i.id=p.invoice_id AND i.user_id=p.user_id WHERE p.user_id=$1 AND (i.invoice_no ILIKE $2 OR i.id::text ILIKE $2) AND ($3::bigint IS NULL OR i.id=$3) AND ($4::timestamptz IS NULL OR p.received_at>=$4) AND ($5::timestamptz IS NULL OR p.received_at<$5)`

func scanCustomerConsumption(row pgx.Row, out *customerCreditConsumption) error {
	return row.Scan(&out.InvoiceID, &out.InvoiceNo, &out.AmountCents, &out.RemainingCents, &out.Status, &out.Version, &out.CreatedAt, &out.RefundedAt)
}
func (s *Server) customerCreditConsumptions(w http.ResponseWriter, r *http.Request) {
	f, ok := parseCustomerCreditFilter(w, r, true)
	if !ok {
		return
	}
	serveCustomerCreditPage(s, w, r, f, customerConsumptionFrom, customerConsumptionColumns, "c.created_at DESC,c.invoice_id DESC", []any{identityFrom(r).ID, f.q, f.invoice, f.from, f.to, f.status}, func(row pgx.Row) (customerCreditConsumption, error) {
		var out customerCreditConsumption
		err := scanCustomerConsumption(row, &out)
		return out, err
	})
}
func (s *Server) customerCreditRepayments(w http.ResponseWriter, r *http.Request) {
	f, ok := parseCustomerCreditFilter(w, r, false)
	if !ok {
		return
	}
	serveCustomerCreditPage(s, w, r, f, customerRepaymentFrom, `p.id,p.invoice_id,i.invoice_no,p.amount_cents,p.received_at,p.created_at`, "p.received_at DESC,p.id DESC", []any{identityFrom(r).ID, f.q, f.invoice, f.from, f.to}, func(row pgx.Row) (customerCreditRepayment, error) {
		var out customerCreditRepayment
		err := row.Scan(&out.ID, &out.InvoiceID, &out.InvoiceNo, &out.AmountCents, &out.ReceivedAt, &out.CreatedAt)
		return out, err
	})
}
func serveCustomerCreditPage[T any](s *Server, w http.ResponseWriter, r *http.Request, f customerCreditFilter, from, columns, order string, args []any, scan func(pgx.Row) (T, error)) {
	tx, err := s.db.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	out := customerCreditPage[T]{Items: make([]T, 0), Limit: f.limit, Offset: f.offset}
	if err = tx.QueryRow(r.Context(), "SELECT count(*)"+from, args...).Scan(&out.Total); err != nil {
		s.fail(w, r, err)
		return
	}
	// Only fixed server-owned SQL fragments are concatenated here.
	limitParam, offsetParam := fmt.Sprintf("$%d", len(args)+1), fmt.Sprintf("$%d", len(args)+2)
	args = append(args, f.limit, f.offset)
	rows, err := tx.Query(r.Context(), "SELECT "+columns+from+" ORDER BY "+order+" LIMIT "+limitParam+" OFFSET "+offsetParam, args...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for rows.Next() {
		item, e := scan(rows)
		if e != nil {
			rows.Close()
			s.fail(w, r, e)
			return
		}
		out.Items = append(out.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
func (s *Server) customerInvoiceCredit(w http.ResponseWriter, r *http.Request) {
	if !customerCreditQuery(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, err := s.db.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var exists bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM invoices WHERE id=$1 AND user_id=$2)`, id, identityFrom(r).ID).Scan(&exists); err != nil {
		s.fail(w, r, err)
		return
	}
	if !exists {
		writeError(w, 404, "not_found", "账单不存在")
		return
	}
	var out customerCreditDetail
	err = scanCustomerConsumption(tx.QueryRow(r.Context(), `SELECT `+customerConsumptionColumns+` FROM credit_invoice_payments c JOIN invoices i ON i.id=c.invoice_id AND i.user_id=c.user_id WHERE c.invoice_id=$1 AND c.user_id=$2`, id, identityFrom(r).ID), &out.customerCreditConsumption)
	var result *customerCreditDetail
	if err == nil {
		if err = tx.QueryRow(r.Context(), `SELECT COALESCE(SUM(p.amount_cents),0)::bigint FROM credit_repayments p JOIN credit_invoice_payments c ON c.invoice_id=p.invoice_id AND c.user_id=p.user_id JOIN invoices i ON i.id=p.invoice_id AND i.user_id=p.user_id WHERE p.invoice_id=$1 AND p.user_id=$2`, id, identityFrom(r).ID).Scan(&out.RepaidCents); err != nil {
			s.fail(w, r, err)
			return
		}
		result = &out
	} else if !errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, struct {
		Credit *customerCreditDetail `json:"credit"`
	}{result})
}
