package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
)

func paymentSnapshotFixtureRuntime(t *testing.T) *PaymentRuntime {
	t.Helper()
	desc, ok := channel.Lookup(channel.Payment, "epay")
	if !ok {
		t.Fatal("built-in payment descriptor missing")
	}
	return &PaymentRuntime{InstanceID: "runtime-snapshot-fixture", Implementation: "epay", Version: 7, SchemaVersion: desc.SchemaVersion, Config: module.Config{
		Values: map[string]any{"api_url": "https://snapshot.example.test", "pid": "12345", "types": []string{"alipay"}},
		Secret: "snapshot-original-private", Secrets: map[string]string{"key": "snapshot-original-private"}, PublicOrigin: "https://shop.example.test",
	}}
}

func TestPaymentRuntimeSnapshotEncryptionAndIdentity(t *testing.T) {
	db := &Store{ProvisionEncryptionKey: bytes.Repeat([]byte{3}, 32)}
	runtime := paymentSnapshotFixtureRuntime(t)
	const reference = "runtime-snapshot-payment"
	sealed, err := db.snapshotRuntimePayment(reference, "epay_alipay", runtime)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(runtime.Config.Secret)) || bytes.Contains(sealed, []byte(runtime.InstanceID)) {
		t.Fatal("runtime snapshot was not encrypted")
	}
	raw, err := db.paymentCredentials(reference, sealed, false)
	if err != nil {
		t.Fatal(err)
	}
	var saved paymentRuntimeSnapshot
	if err = json.Unmarshal(raw, &saved); err != nil || saved.SnapshotVersion != 2 || saved.InstanceVersion != 7 || saved.Method != "epay_alipay" || saved.Secret != runtime.Config.Secret || !validPaymentMerchantScope(saved.Method, saved.MerchantScope) {
		t.Fatal("runtime identity or credentials were not frozen", err)
	}
	gateway, available, err := runtimePaymentGateway(raw, "epay_alipay", runtime.InstanceID)
	if err != nil || !available || gateway == nil || gateway.Name() != "epay_alipay" {
		t.Fatal("frozen gateway could not be restored", err)
	}
	if _, available, err = runtimePaymentGateway(raw, "epay_alipay", "another-instance"); err == nil || available {
		t.Fatal("snapshot accepted for another instance")
	}
	if _, available, err = runtimePaymentGateway(raw, "epay_wxpay", runtime.InstanceID); err == nil || available {
		t.Fatal("snapshot accepted for another payment method")
	}
	if _, err = db.externalRefundMerchantScope("epay_alipay", reference, sealed, "another-instance"); err == nil {
		t.Fatal("refund evidence accepted another instance's snapshot")
	}
	if _, err = db.paymentCredentials("different-reference", sealed, false); err == nil {
		t.Fatal("runtime ciphertext accepted for another reference")
	}
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1
	if _, err = db.paymentCredentials(reference, tampered, false); err == nil {
		t.Fatal("tampered runtime ciphertext accepted")
	}
	db.ProvisionEncryptionKey = bytes.Repeat([]byte{4}, 32)
	if _, err = db.paymentCredentials(reference, sealed, false); err == nil {
		t.Fatal("runtime ciphertext accepted under another encryption key")
	}
}

func TestPaymentRuntimeSnapshotRejectsUnsupportedEnvelope(t *testing.T) {
	db := &Store{ProvisionEncryptionKey: bytes.Repeat([]byte{3}, 32)}
	runtime := paymentSnapshotFixtureRuntime(t)
	sealed, err := db.snapshotRuntimePayment("runtime-snapshot-payment", "epay_alipay", runtime)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.paymentCredentials("runtime-snapshot-payment", sealed, false)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(map[string]any){
		"unknown_snapshot_version": func(v map[string]any) { v["snapshot_version"] = 999 },
		"zero_snapshot_version":    func(v map[string]any) { v["snapshot_version"] = 0 },
		"null_snapshot_version":    func(v map[string]any) { v["snapshot_version"] = nil },
		"missing_snapshot_version": func(v map[string]any) { delete(v, "snapshot_version") },
		"unknown_schema":           func(v map[string]any) { v["schema_version"] = 999 },
		"missing_implementation":   func(v map[string]any) { delete(v, "implementation") },
		"unknown_implementation":   func(v map[string]any) { v["implementation"] = "missing_payment_fixture" },
		"different_method":         func(v map[string]any) { v["method"] = "epay_wxpay" },
		"empty_instance":           func(v map[string]any) { v["instance_id"] = "" },
		"zero_instance_version":    func(v map[string]any) { v["instance_version"] = 0 },
		"wrong_merchant_scope":     func(v map[string]any) { v["merchant_scope"] = "epay:" + string(bytes.Repeat([]byte{'0'}, 64)) },
		"simulation_scope":         func(v map[string]any) { v["merchant_scope"] = "mock:test-only" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var fields map[string]any
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			mutate(fields)
			invalid, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if gateway, available, err := runtimePaymentGateway(invalid, "epay_alipay", runtime.InstanceID); err == nil || available || gateway != nil {
				t.Fatal("unsupported historical envelope restored a gateway")
			}
		})
	}
}

func TestPaymentRuntimeMalformedHistoryNeverUsesCurrentCredentials(t *testing.T) {
	db := isolatedTestStore(t)
	db.ProvisionEncryptionKey = bytes.Repeat([]byte{3}, 32)
	_, customer := topUpFixture(t, db)
	if _, err := db.Pool.Exec(t.Context(), `UPDATE payment_settings SET configured=true,enabled=false,mock_enabled=false,key_ciphertext='broken-current-credential'::bytea`); err != nil {
		t.Fatal(err)
	}
	for index, raw := range []string{
		`{"unavailable":true}`,
		`{"snapshot_version":0,"implementation":"mock","secret":"old-private"}`,
		`{"snapshot_version":null,"implementation":"mock","secret":"old-private"}`,
		`{"implementation":"mock","secret":"old-private","method":"mock"}`,
		`{"snapshot_version":999,"implementation":"mock","secret":"old-private"}`,
	} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			reference := fmt.Sprintf("runtime-malformed-%d", index)
			sealed, err := db.paymentCredentials(reference, []byte(raw), true)
			if err != nil {
				t.Fatal(err)
			}
			var invoice int64
			if err = db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents) VALUES($1,$2,'manual','unpaid',1234) RETURNING id`, reference, customer).Scan(&invoice); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Pool.Exec(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,credential_snapshot) VALUES('mock',$1,$2,$3,1234,'pending',$4)`, reference, invoice, customer, sealed); err != nil {
				t.Fatal(err)
			}
			gateway, available, _ := db.PaymentTransactionGateway(t.Context(), "mock", reference, PaymentConfiguration{MockSecret: "current-replacement-private"})
			if available || gateway != nil {
				t.Fatal("malformed or unavailable history borrowed current credentials")
			}
		})
	}
}
