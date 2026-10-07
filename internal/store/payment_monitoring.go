package store

import (
	"context"
	"errors"
	"time"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/jackc/pgx/v5"
)

// Claims and manual checks lock the same payment row before reserving a lease.
// Queries are reads, so an abandoned lease may safely be reclaimed.
func (s *Store) ClaimAutomaticPaymentCheck(ctx context.Context, providers []string) (PaymentCheck, error) {
	check := PaymentCheck{automatic: true}
	if len(providers) == 0 {
		return check, pgx.ErrNoRows
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE payment_reconciliation_checks SET status='error',result_code='lease_expired',completed_at=clock_timestamp()
 WHERE status='checking' AND lease_until<=clock_timestamp()`); err != nil {
		return check, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return check, err
	}
	defer tx.Rollback(ctx)
	if err = recheckPaymentQueryConfiguration(ctx, tx); err != nil {
		return check, err
	}
	var attempts int
	if err = tx.QueryRow(ctx, `SELECT p.id,p.invoice_id,p.user_id,p.provider,p.provider_payment_id,p.amount_cents,p.auto_reconcile_attempts
 FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id AND i.user_id=p.user_id
 WHERE p.status IN ('pending','expired') AND i.status='unpaid' AND p.provider LIKE 'epay_%' AND p.provider=ANY($1::text[])
 AND p.next_reconcile_at<=clock_timestamp() AND p.auto_reconcile_attempts<32
 AND NOT EXISTS(SELECT 1 FROM payment_reconciliation_checks c WHERE c.payment_id=p.id
   AND (c.status='checking' OR c.started_at>clock_timestamp()-interval '30 seconds'))
 ORDER BY p.next_reconcile_at,p.id FOR UPDATE OF p SKIP LOCKED LIMIT 1`, providers).
		Scan(&check.PaymentID, &check.InvoiceID, &check.UserID, &check.Provider, &check.ProviderPaymentID, &check.AmountCents, &attempts); err != nil {
		return check, err
	}
	attempts++
	if _, err = tx.Exec(ctx, `UPDATE payment_transactions SET auto_reconcile_attempts=$2,
 next_reconcile_at=CASE WHEN $2=32 THEN NULL ELSE clock_timestamp()+make_interval(secs=>$3::double precision) END WHERE id=$1`, check.PaymentID, attempts, automaticPaymentDelay(attempts).Seconds()); err != nil {
		return check, err
	}
	if err = tx.QueryRow(ctx, "INSERT INTO payment_reconciliation_checks(payment_id,actor_id,source) VALUES($1,NULL,'worker') RETURNING id", check.PaymentID).Scan(&check.ID); err != nil {
		return check, err
	}
	if err = Audit(ctx, tx, 0, "payment.reconciliation.requested", "invoice", check.InvoiceID, map[string]any{"payment_id": check.PaymentID, "check_id": check.ID, "source": "worker", "attempt": attempts}); err != nil {
		return check, err
	}
	return check, tx.Commit(ctx)
}

func automaticPaymentDelay(attempts int) time.Duration {
	switch {
	case attempts <= 3:
		return 5 * time.Minute
	case attempts <= 6:
		return 15 * time.Minute
	case attempts <= 12:
		return time.Hour
	default:
		return 6 * time.Hour
	}
}

// Processes at most one query. Payment settlement reuses callback validation
// and commits together with the fenced query result and audit.
func (s *Store) ProcessAutomaticPaymentCheck(ctx context.Context, gateways map[string]payment.QueryGateway) (bool, error) {
	providers := make([]string, 0, len(gateways))
	for name, gateway := range gateways {
		if gateway != nil && gateway.Name() == name {
			providers = append(providers, name)
		}
	}
	check, err := s.ClaimAutomaticPaymentCheck(ctx, providers)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.processAutomaticPaymentCheck(ctx, check, gateways[check.Provider])
}

func (s *Store) processAutomaticPaymentCheck(ctx context.Context, check PaymentCheck, gateway payment.QueryGateway) (bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, queryErr := gateway.Query(queryCtx, payment.QueryRequest{ProviderPaymentID: check.ProviderPaymentID, AmountCents: check.AmountCents})
	var err error
	code := "provider_unavailable"
	if queryErr == nil {
		_, err = s.CompletePaymentCheck(queryCtx, check, result)
		if err == nil {
			return true, nil
		}
		code = "settlement_failed"
	} else {
		err = queryErr
		if errors.Is(queryErr, payment.ErrQueryInvalid) {
			code = "provider_invalid"
		}
	}
	finishCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer stop()
	if failErr := s.FailPaymentCheck(finishCtx, check, code); failErr != nil {
		return true, failErr
	}
	return true, err
}
