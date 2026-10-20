package httpapi

import (
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// seedHistoricalCreditPayment builds pre-retirement debt only in test fixtures.
// Production HTTP retires both new credit and historical recovery operations.
func seedHistoricalCreditPayment(t *testing.T, db *store.Store, uid, invoice int64, cookie, key string) store.PaymentResult {
	t.Helper()
	var version int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT version FROM invoices WHERE id=$1", invoice).Scan(&version); err != nil {
		t.Fatal(err)
	}
	out, err := db.PayInvoiceCredit(t.Context(), uid, invoice, key, version, tokenHash(cookieValue(cookie)))
	if err != nil {
		t.Fatal("seed historical credit debt", err)
	}
	return out
}
