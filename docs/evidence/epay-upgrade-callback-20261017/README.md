# Epay upgrade concurrent callback repair, 2026-10-17

Scope: isolated worktree based on `46b759b1f9d161a06e76af7e92c4116c79da64f2`, dedicated loopback PostgreSQL 18 `_test` database, isolated per-test schemas. No business database, deployment, real gateway traffic, worker constants, or concurrency assertions were changed. Tests use `scripts/go.sh`.

## Reproduced failure

The earlier serial full-suite run remains **FAIL**: `TestEpayPendingGuardsAndServiceSettlement/upgrade` rejected a concurrent callback with `500 fail`. Its original log is retained independently in `docs/evidence/local-renewal-refund-20261017/serial-fixed-full-suite.log`; this repair does not replace that result or claim a full-suite pass.

- `original-upgrade-before.log`: original upgrade subtest, `-count=10`, reproduced **1/10 FAIL**, package 20.301s.
- `barrier-before.log`: new deterministic real HTTP regression on the old payment implementation, **FAIL**, package 1.844s; actual settlement logger reports `external_refund_order_hold (SQLSTATE 23514)`.

The PostgreSQL server log confirms the same error, trigger and statement in both the full suite and targeted reproduction:

```text
2026-10-17 12:24:05.804 CST [2564176] ERROR: external_refund_order_hold
CONTEXT: PL/pgSQL function guard_external_refund_order_association() line 25 at RAISE
STATEMENT: UPDATE payment_transactions SET external_transaction_id=$2 WHERE id=$1
2026-10-17 12:44:31.151 CST [2585952] ERROR: external_refund_order_hold
CONTEXT: PL/pgSQL function guard_external_refund_order_association() line 25 at RAISE
STATEMENT: UPDATE payment_transactions SET external_transaction_id=$2 WHERE id=$1
```

This was a false refund-hold rejection under ordinary row-lock contention, not a PostgreSQL `40P01` deadlock. Cancellation locks the invoice and then the service, checks pending payment using an ordinary SELECT, and returns 409. The callback held the payment row but updated its external transaction ID before acquiring its invoice. Migration 154's association guard locks that invoice `FOR UPDATE NOWAIT`, converting contention into 23514. A concurrently cancelling request therefore made a legitimate signed callback fail.

## Repair and protection boundary

Move the callback's existing invoice `FOR UPDATE` before its first payment update and reuse the saved invoice status during settlement. The callback now waits at an explicit parent lock; cancellation finishes its pending-payment conflict and releases the invoice. Payment-before-invoice ordering already exists in the successful callback, close verification and external refund entrypoints. Closed/succeeded replay paths retain their existing behavior.

Published migrations 154 and 155 and all refund guards remain byte-for-byte unchanged. In particular, this change does not exempt metadata updates or association moves from the hold check, remove NOWAIT from child-first SQL writers, or alter old/new association checks. No migration 156 is required.

```text
5ff26f58ad73693390b15fd1e35a6d3014107eaced6b15df2a4ad2403995687a  154_order_external_refunds.sql
a3ebfa5a83589be969d84bf59b479161703e8732b13f4d9d7d6b1dd8e8afcd78  155_local_renewal_refunds.sql
```

The deterministic HTTP regression holds only a service row, observes the real cancellation's wait through `pg_blocking_pids`, then launches eight real signed Epay callbacks and observes the callback waiting for that cancelling transaction. Releasing the barrier must produce cancellation 409 and eight `200 success` responses. It also checks exactly one webhook event, success audit and pending upgrade job; paid invoice; succeeded payment with the scoped webhook transaction identity and original raw trade; unchanged customer balance; and no refund hold. Polling observes real lock edges; no sleep selects the interleaving.

`TestPaymentCallbackInvoicePrelockPreservesOrderRefundHold` obtains a genuine pending order refund through real checkout, settlement and refund admission. Even while owning both invoice locks, payment metadata/status changes and moves into/out of the held invoice must return exactly 23514 `external_refund_order_hold`. Payment rows and the pending hold must remain unchanged.

## Commands and after results

Export `ZJMFGO_TEST_DATABASE_URL` to the authorized isolated PostgreSQL 18 `_test` database and `REQUIRE_INTEGRATION=1`. No private DSN or credential is saved here.

```bash
scripts/go.sh test ./internal/httpapi \
  -run '^TestEpayPendingGuardsAndServiceSettlement$/^upgrade$' -count=10 -timeout=5m -v
scripts/go.sh test ./internal/httpapi \
  -run '^TestEpayUpgradeCallbackWaitsForConcurrentCancellation$' -count=1 -timeout=1m -v
scripts/go.sh test ./internal/store \
  -run '^TestPaymentCallbackInvoicePrelockPreservesOrderRefundHold$' -count=1 -timeout=1m -v
scripts/go.sh test ./internal/httpapi \
  -run '^(TestEpayPendingGuardsAndServiceSettlement|TestEpayUpgradeCallbackWaitsForConcurrentCancellation)$' \
  -count=10 -timeout=4m -v
scripts/go.sh test ./internal/store \
  -run '^(TestPaymentCallbackInvoicePrelockPreservesOrderRefundHold|TestOrderExternalRefundLifecycle|TestOrderExternalRefundDeliveryAndItemWaits|TestOrderExternalRefundHoldWinsDeliveryWait|TestOrderExternalRefundReverseLockGuardsFailWithoutWaiting|TestOrderExternalRefundServiceMovesCheckBothOrders|TestOrderExternalRefundUpgradeFrom153DoesNotAdoptHistoricalOrders|TestLocalRenewalUpgradeFrom154DoesNotInventEffects)$' \
  -count=1 -timeout=3m -v
scripts/go.sh test ./internal/httpapi \
  -run '^(TestEpayPaymentLifecycleAndReplay|TestEpayCallbackRollbackAndInvalidFields|TestPaymentReconciliationConcurrentChecksAndCallbackWins|TestPaymentCloseVerificationSignedEpayCallbackAndReviewCompete|TestPaymentCloseConcurrentReservationAndCallbackPriority|TestPaymentAnomalyRefundPreventsReverseSettlement)$' \
  -count=1 -timeout=2m -v
scripts/go.sh test -race -p=1 ./internal/httpapi ./internal/store \
  -run '^(TestEpayUpgradeCallbackWaitsForConcurrentCancellation|TestPaymentCallbackInvoicePrelockPreservesOrderRefundHold)$' \
  -count=3 -timeout=2m -v
```

After results:

- `hold-after.log`: new genuine pending-hold regression, all four mutations rejected correctly, **PASS**, store 1.985s.
- `scenarios-after.log`: existing Epay order/upgrade/renewal plus deterministic cancellation barrier, each repeated ten times, **PASS**, HTTP 88.644s. All eight success assertions and cancellation 409 assertions remain strict.
- `refund-migrations-after.log`: genuine hold lifecycle, both concurrent delivery/hold orderings, reverse-lock fail-fast guards, both service association directions, new payment hold regression, schema 153 upgrade and schema 154 upgrade, **PASS**, store 25.869s. Both upgrade fixtures call the current `Migrate` and `CheckSchema` twice and preserve historical business facts; `git diff --exit-code HEAD -- db/migrations` also confirms no migration edit.
- `shared-settlement-after.log`: Epay lifecycle/replay and rollback/invalid inputs, independently reserved anomaly refund versus settlement (pending/confirmed/concurrent), close reservation versus callback, signed callback versus independently reviewed close (both orderings/concurrent), query reconciliation versus callback, **PASS**, HTTP 21.068s.
- `race-after.log`: both new regressions repeated three times with `-race -p=1`, **PASS**, HTTP 7.581s and store 5.983s; no race report. Packages ran sequentially against the exclusively reserved test database.

Post-test PostgreSQL verification: version `180000`, zero other active sessions or transactions. No `store_topup_*` or `order_refund_upgrade_*` schemas remain. Two `zjmfgo_test_*` namespaces remain with zero relation objects; they are preserved rather than assuming ownership and deleting them. Static `git diff --check` passed. Default no-DSN runs of both new tests compiled and explicitly skipped; these are compilation/guard checks, not integration passes.

Full-suite validation belongs to the parent integration run; none was run in this worktree.
