package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/codex/zjmfgo/internal/config"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Opt-in fixture in a disposable _test database. It adds no runtime endpoint,
// never rewrites immutable evidence, and uses only the compiled mock gateway.
func TestPaymentCloseExpiredRecoveryBrowserFixture(t *testing.T) {
	ready, finish := os.Getenv("ZJMFGO_BROWSER_READY_FILE"), os.Getenv("ZJMFGO_BROWSER_FINISH_FILE")
	if ready == "" || finish == "" {
		t.Skip("opt-in expired recovery browser fixture")
	}
	for _, p := range []string{ready, finish} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("fresh fixture paths required")
		}
	}
	f := newCloseVerificationFixture(t, false)
	actor, _ := closeActor(t, f.upgradeFixture, f.verifier)
	other, otherCookie := externalRefundActor(t, f.db, f.h, "expired-other", []string{"invoices.view", "payments.close_verify"})
	key := "expired-browser-original-20261017"
	raw, _ := json.Marshal([]any{"lookup", f.invoice, f.pid, f.operation.ID, int64(1), ""})
	sum := sha256.Sum256(raw)
	var id int64
	err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO payment_close_verification_requests(close_operation_id,payment_id,invoice_id,user_id,actor_id,provider,channel_instance_id,provider_payment_id,operation_id,amount_cents,credential_sha256,expected_version,idempotency_key,request_hash,created_at,lease_until)
 SELECT id,payment_id,invoice_id,user_id,$2,provider,channel_instance_id,provider_payment_id,operation_id,amount_cents,credential_sha256,1,$3,$4,clock_timestamp()-interval '3 minutes',clock_timestamp()-interval '1 minute' FROM payment_close_operations WHERE id=$1 RETURNING id`, f.operation.ID, actor, key, hex.EncodeToString(sum[:])).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	// Every newer unrelated record is produced through the actual Lookup API.
	// They all refer to the same original close; the oldest key must be found on page 2.
	for n := 0; n < 105; n++ {
		expect(t, request(f.h, "POST", verificationPath(f.invoice, f.pid), map[string]any{"close_operation_id": f.operation.ID, "expected_version": 1}, otherCookie, fmt.Sprintf("expired-browser-unrelated-%03d", n)), 201)
	}
	// Association mismatch attempts cannot manufacture the wrong-amount facts.
	_, err = f.db.Pool.Exec(t.Context(), `INSERT INTO payment_close_verification_requests(close_operation_id,payment_id,invoice_id,user_id,actor_id,provider,channel_instance_id,provider_payment_id,operation_id,amount_cents,credential_sha256,expected_version,idempotency_key,request_hash,created_at,lease_until)
 SELECT id,payment_id,invoice_id,user_id,$2,provider,channel_instance_id,provider_payment_id,operation_id,amount_cents+1,credential_sha256,1,'expired-wrong-amount-fixture',repeat('a',64),clock_timestamp()-interval '3 minutes',clock_timestamp()-interval '1 minute' FROM payment_close_operations WHERE id=$1`, f.operation.ID, actor)
	if err == nil {
		t.Fatal("database accepted mismatched amount")
	}
	origin := os.Getenv("ZJMFGO_BROWSER_ORIGIN")
	server := httptest.NewServer(New(f.db, config.Config{PublicOrigin: origin, PaymentMockEnabled: true, PaymentGatewaySecret: "integration-payment-secret", ProvisionEncryptionKey: []byte("01234567890123456789012345678901")}, slog.Default()).Handler())
	defer server.Close()
	var schema string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"api_origin": server.URL, "schema": schema, "actor_id": actor, "other_actor_id": other, "invoice_id": f.invoice, "payment_id": f.pid, "close_operation_id": f.operation.ID, "operation_id": f.operation.OperationID, "verification_id": id, "key": key, "amount_cents": f.operation.AmountCents, "expected_version": 1, "username": "close-verifier", "other_username": "expired-other", "password": "integration-password", "unrelated_count": 105, "amount_mismatch_rejected": true})
	if err = os.WriteFile(ready, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("isolated expired browser fixture ready")
	deadline := time.NewTimer(40 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("fixture timeout")
		case <-tick.C:
			if _, err = os.Stat(finish); err == nil {
				goto done
			}
		}
	}
done:
	closeState(t, f.upgradeFixture, f.invoice, f.pid, "pending", 1)
	var status string
	var unresolved bool
	var results, decisions, requests int
	err = f.db.Pool.QueryRow(t.Context(), `SELECT status,payment_close_operation_unresolved(id),(SELECT count(*) FROM payment_close_verification_results WHERE request_id=$2),(SELECT count(*) FROM payment_close_verification_decisions),(SELECT count(*) FROM payment_close_verification_requests) FROM payment_close_operations WHERE id=$1`, f.operation.ID, id).Scan(&status, &unresolved, &results, &decisions, &requests)
	if err != nil || status != "unknown" || !unresolved || results != 0 || decisions != 0 || requests != 107 {
		t.Fatalf("immutable/freeze invariant status=%s unresolved=%v results=%d decisions=%d requests=%d err=%v", status, unresolved, results, decisions, requests, err)
	}
	evidence, _ := json.Marshal(map[string]any{"status": "PASS", "original_close_status": status, "unresolved": unresolved, "expired_original_results": results, "decisions": decisions, "requests": requests, "payment_status": "pending", "invoice_status": "unpaid", "invoice_version": 1, "ledger_rows": 0, "amount_mismatch_rejected": true})
	if err = os.WriteFile(ready+".evidence.json", evidence, 0600); err != nil {
		t.Fatal(err)
	}
}
