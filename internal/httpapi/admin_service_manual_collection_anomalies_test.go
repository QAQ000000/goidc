package httpapi

import (
	"fmt"
	"testing"
	"time"
)

// Use the existing isolated Epay credential fixture and the trusted close /
// callback Store entrypoints. This never contacts an external payment service.
func manualCollectionClosedRealPaymentFixture(t *testing.T) (adminRenewalFixture, map[string]any, int64, int64, string) {
	t.Helper()
	base, invoice, payment, reference := closeFixture(t, "epay_alipay")
	actor, hash := closeActor(t, base, base.root)
	op, err := base.db.BeginPaymentClose(t.Context(), actor, invoice, payment, 1, "manual-late-original-close", hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = base.db.CompletePaymentClose(t.Context(), op, closeVerified(op), hash); err != nil {
		t.Fatal(err)
	}
	staffID, staff := user(t, base.db, base.h, "manual-late-collector", "admin")
	f := adminRenewalFixture{base, staffID, staff}
	manualCollectionGrants(t, f)
	renewal := f.createRenewal(t, "manual-late-renewal")
	return f, renewal, invoice, payment, reference
}

func manualCollectionLateReceipt(t *testing.T, f adminRenewalFixture, invoice int64, reference, event, rawTransaction string) {
	t.Helper()
	result, err := f.db.ProcessPaymentWebhook(t.Context(), "epay_alipay", event, reference, invoice, 3000, "succeeded", []byte(`{"fixture":"manual-collection-late-receipt"}`), rawTransaction)
	if err != nil || (!result.Replayed && result.Status != "closed") {
		t.Fatal("trusted late receipt was not retained after final close", result, err)
	}
}

func TestAdminServiceManualCollectionRejectsExistingLateReceipt(t *testing.T) {
	for _, rejectedRefund := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejected_refund=%t", rejectedRefund), func(t *testing.T) {
			f, renewal, originalInvoice, originalPayment, reference := manualCollectionClosedRealPaymentFixture(t)
			rawTransaction := "manual-existing-real-late-receipt"
			input := manualCollectionInput(t, f, renewal, rawTransaction)
			// This valid quote predates the arrival on a different invoice. Apply
			// must recheck source identity even though its own invoice is unchanged.
			quoted := manualCollectionQuote(t, f, renewal, input)
			manualCollectionLateReceipt(t, f, originalInvoice, reference, "manual-existing-real-late-event", rawTransaction)
			var anchor int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT min(id) FROM payment_close_anomalies WHERE payment_id=$1", originalPayment).Scan(&anchor); err != nil {
				t.Fatal(err)
			}
			if rejectedRefund {
				basePath, createPath := anomalyPaths(originalInvoice, anchor)
				created := request(f.h, "POST", createPath, paymentAnomalyInput(anchor, "manual-existing-late-rejected-refund"), f.root, "manual-existing-late-refund-create")
				expect(t, created, 201)
				refundID := int64(data(t, created)["refund"].(map[string]any)["id"].(float64))
				_, reviewer := anomalyReviewer(t, f.upgradeFixture)
				rejectPath := fmt.Sprintf("%s/payment-anomaly-refunds/%d/reject", basePath, refundID)
				expect(t, request(f.h, "POST", rejectPath, map[string]any{"expected_version": 1, "expected_group_version": anchor, "note": "独立驳回退回证据，原迟到收款事实仍然存在"}, reviewer, "manual-existing-late-refund-reject"), 201)
				var binding, rejected int
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM payment_close_anomaly_merchant_bindings WHERE payment_id=$1),
 (SELECT count(*) FROM payment_close_anomaly_refunds WHERE id=$2 AND status='rejected')`, originalPayment, refundID).Scan(&binding, &rejected); err != nil || binding != 1 || rejected != 1 {
					t.Fatal("rejected refund fixture did not preserve verified original merchant identity", binding, rejected, err)
				}
			}
			var originalExternal string
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT external_transaction_id FROM payment_transactions WHERE id=$1", originalPayment).Scan(&originalExternal); err != nil || originalExternal != "" {
				t.Fatal("fixture must retain the raw receipt only in the late anomaly", originalExternal, err)
			}
			before := manualCollectionFacts(t, f)
			for _, attempt := range []struct {
				path string
				body map[string]any
				key  string
			}{
				{manualCollectionPath(f, renewal) + "/manual-collection-quotes", input, ""},
				{manualCollectionPath(f, renewal) + "/manual-collections", quoted, "manual-existing-late-collection"},
			} {
				w := request(f.h, "POST", attempt.path, attempt.body, f.staff, attempt.key)
				expect(t, w, 409)
				if errorData(t, w)["code"] != "manual_collection_source_occupied" {
					t.Fatal("existing late receipt was not rejected by the source fence", w.Body.String())
				}
				manualCollectionUnchanged(t, f, before)
			}
			var status string
			var collections int
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT status,(SELECT count(*) FROM manual_collections) FROM invoices WHERE id=$1", int64(renewal["invoice_id"].(float64))).Scan(&status, &collections); err != nil || status != "unpaid" || collections != 0 {
				t.Fatal("late source rejection changed the other invoice or created a manual receipt", status, collections, err)
			}
		})
	}
}

func TestAdminServiceManualCollectionPreservesLaterAnomalyWithoutDoubleCash(t *testing.T) {
	f, renewal, originalInvoice, originalPayment, reference := manualCollectionClosedRealPaymentFixture(t)
	rawTransaction := "manual-accepted-before-real-late-receipt"
	input := manualCollectionInput(t, f, renewal, rawTransaction)
	quoted := manualCollectionQuote(t, f, renewal, input)
	out := manualCollectionApply(t, f, renewal, quoted, "manual-before-late-collection")
	collectionID := int64(out["collection_id"].(float64))
	manualPaymentID := int64(out["payment_id"].(float64))
	originalFacts := func() string {
		t.Helper()
		var facts string
		if err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_array(to_jsonb(c),to_jsonb(p))::text
 FROM manual_collections c JOIN payment_transactions p ON p.id=c.payment_id WHERE c.id=$1 AND p.id=$2`, collectionID, manualPaymentID).Scan(&facts); err != nil {
			t.Fatal(err)
		}
		return facts
	}
	collectedFacts := originalFacts()
	settled := f.snapshot(t)
	manualCollectionLateReceipt(t, f, originalInvoice, reference, "manual-after-collection-real-late", rawTransaction)
	// Preserve idempotent callback acknowledgement and its single ignored event.
	manualCollectionLateReceipt(t, f, originalInvoice, reference, "manual-after-collection-real-late", rawTransaction)
	var anomalies, ignored, succeeded int
	var originalStatus, externalTransaction string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT p.status,p.external_transaction_id,
 (SELECT count(*) FROM payment_close_anomalies WHERE payment_id=p.id AND external_transaction_id=$2),
 (SELECT count(*) FROM payment_webhook_events WHERE provider='epay_alipay' AND event_id='manual-after-collection-real-late' AND status='ignored'),
 (SELECT count(*) FROM payment_transactions WHERE status='succeeded')
 FROM payment_transactions p WHERE p.id=$1`, originalPayment, rawTransaction).Scan(&originalStatus, &externalTransaction, &anomalies, &ignored, &succeeded); err != nil || originalStatus != "closed" || externalTransaction != "" || anomalies != 1 || ignored != 1 || succeeded != 1 {
		t.Fatal("later trusted callback was lost, duplicated or incorrectly settled", originalStatus, externalTransaction, anomalies, ignored, succeeded, err)
	}
	if after := f.snapshot(t); after.balance != settled.balance || after.ledger != settled.ledger || after.effects != settled.effects || after.serviceVersion != settled.serviceVersion || !after.due.Equal(settled.due) {
		t.Fatal("closed late callback changed the already settled renewal or wallet", settled, after)
	}
	if got := originalFacts(); got != collectedFacts {
		t.Fatal("later anomaly rewrote the immutable manual receipt or payment", collectedFacts, got)
	}
	// Match the verified merchant-binding fixture used by the annual report's
	// merchant-scope tests. Even with a verifiable online identity, an offline
	// receipt cannot prove that it belongs to that merchant; retain the anomaly
	// as unverified instead of counting the raw transaction twice as real cash.
	var sealed []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT credential_snapshot FROM payment_transactions WHERE id=$1", originalPayment).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	scope, err := f.db.ReportPaymentMerchantScope("epay_alipay", reference, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(t.Context(), `INSERT INTO payment_close_anomaly_merchant_bindings(payment_id,credential_sha256,merchant_scope,provider,provider_payment_id,channel_instance_id)
 SELECT id,encode(sha256(credential_snapshot),'hex'),$2,provider,provider_payment_id,coalesce(channel_instance_id,'') FROM payment_transactions WHERE id=$1`, originalPayment, scope); err != nil {
		t.Fatal(err)
	}
	var received time.Time
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT received_at FROM manual_collections WHERE id=$1", collectionID).Scan(&received); err != nil {
		t.Fatal(err)
	}
	year := received.In(time.FixedZone("Shanghai", 8*3600)).Year()
	w := request(f.h, "GET", fmt.Sprintf("/api/v1/admin/reports/annual-cash?year=%d", year), nil, f.root, "")
	expect(t, w, 200)
	total := data(t, w)["total"].(map[string]any)
	for key, want := range map[string]any{
		"manual_received_cents": renewal["amount_cents"], "received_cents": renewal["amount_cents"],
		"gateway_received_cents": float64(0), "anomaly_received_cents": float64(0),
		"unverified_anomaly_cents": float64(3000), "refund_cents": float64(0),
	} {
		if total[key] != want {
			t.Fatal("annual cash duplicated or misclassified a manual/late raw receipt", key, want, total)
		}
	}
	if got := originalFacts(); got != collectedFacts {
		t.Fatal("cash report changed original manual collection facts", collectedFacts, got)
	}
}
