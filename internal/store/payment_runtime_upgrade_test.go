package store

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/codex/zjmfgo/internal/payment"
)

// Originate at the real 164 boundary: no payment runtime migration has existed
// when historical credential bytes and payment facts are inserted.
func TestPaymentRuntimeUpgrade164PreservesHistoricalIdentity(t *testing.T) {
	db := notificationSchema160(t)
	for _, name := range []string{notificationUpgrade161, notificationUpgrade162, "163_notification_lifecycle.sql", "164_certification_provider_runtime.sql"} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	db.ProvisionEncryptionKey = bytes.Repeat([]byte{9}, 32)
	_, customer := topUpFixture(t, db)
	legacy, err := json.Marshal(payment.EpayConfig{APIURL: "https://legacy.example.test", PID: "12345", Key: "upgrade-legacy-private", PublicOrigin: "https://shop.example.test", Types: []string{"alipay"}})
	if err != nil {
		t.Fatal(err)
	}
	reference := "epay_1_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sealed, err := db.paymentCredentials(reference, legacy, true)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := db.externalRefundMerchantScope("epay_alipay", reference, sealed)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		provider, reference string
		raw                 []byte
	}{{"epay_alipay", reference, sealed}, {"mock", "mock_upgrade", nil}} {
		if item.provider == "mock" {
			item.raw, err = db.paymentCredentials(item.reference, []byte(`{"implementation":"mock","mock_secret":"upgrade-old-mock-private"}`), true)
			if err != nil {
				t.Fatal(err)
			}
		}
		var invoice, pid int64
		if err = db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents,paid_at) VALUES($1,$2,'manual','paid',1234,now()) RETURNING id`, "PAYMENT-UPGRADE-"+item.provider, customer).Scan(&invoice); err != nil {
			t.Fatal(err)
		}
		if err = db.Pool.QueryRow(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,credential_snapshot,external_transaction_id,completed_at) VALUES($1,$2,$3,$4,1234,'succeeded',$5,$6,now()) RETURNING id`, item.provider, item.reference, invoice, customer, item.raw, "upgrade-merchant:"+item.provider).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Pool.Exec(t.Context(), `INSERT INTO payment_webhook_events(provider,event_id,payload_hash,status) VALUES($1,$2,repeat('a',64),'processed')`, item.provider, "upgrade-event-"+item.provider); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() string {
		var raw string
		err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
   'transactions',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM payment_transactions p),
   'webhooks',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM payment_webhook_events e),
   'instances',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM channel_instances c),
   'settings',(SELECT to_jsonb(s) FROM payment_settings s),
   'invoices',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invoices i),
   'ledger',(SELECT jsonb_agg(to_jsonb(l) ORDER BY id) FROM balance_ledger l),
   'checks',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM payment_reconciliation_checks c),
   'close',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM payment_close_operations c),
   'refunds',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM external_refunds r)
  )::text`).Scan(&raw)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	if err = db.CheckSchema(t.Context()); err == nil {
		t.Fatal("new runtime accepted schema 164 before migration")
	}
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if snapshot() != before {
		t.Fatal("payment runtime upgrade changed historical financial or credential facts")
	}
	if err = db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	gateway, ok, err := db.PaymentTransactionGateway(t.Context(), "epay_alipay", reference, PaymentConfiguration{})
	if err != nil || !ok || gateway == nil {
		t.Fatal("historical epay no longer resolves", err)
	}
	actual, err := db.externalRefundMerchantScope("epay_alipay", reference, sealed)
	if err != nil || actual != scope {
		t.Fatal("historical refund merchant scope changed", err)
	}
	if err = db.Migrate(t.Context()); err != nil || snapshot() != before {
		t.Fatal("repeat migration changed historical facts", err)
	}
	assertNotificationUpgradeChecksums(t, db)
}
