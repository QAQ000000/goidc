package httpapi

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestLegacyTerminalPaymentLostCredentialsAllowNewMerchant(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	f.h = New(f.db, settingsPaymentConfig(), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	var references []string
	for _, state := range []string{"succeeded", "failed"} {
		invoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "legacy-terminal-"+state)
		reference := fmt.Sprintf("epay_%d_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", invoice)
		if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,completed_at)
 VALUES('epay_alipay',$1,$2,$3,3000,$4,now())`, reference, invoice, f.uid, state); err != nil {
			t.Fatal(err)
		}
		references = append(references, reference)
	}
	settings := paymentSettingsBody("https://replacement.example", 1)
	expect(t, request(f.h, "PUT", paymentSettingsPath, settings, f.root, ""), 200)
	fallback := store.PaymentConfiguration{PublicOrigin: settingsPaymentConfig().PublicOrigin}
	configuration, err := f.db.PaymentConfiguration(t.Context(), fallback)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := configuration.Gateway("epay_alipay", false); !ok {
		t.Fatal("replacement merchant unavailable")
	}
	newInvoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "legacy-terminal-new-invoice")
	epayIntent(t, f, newInvoice, "epay_alipay", "legacy-terminal-new-intent")
	for _, reference := range references {
		gateway, ok, err := f.db.PaymentTransactionGateway(t.Context(), "epay_alipay", reference, configuration)
		if err != nil || ok || gateway != nil {
			t.Fatal("terminal payment adopted replacement credentials", reference, ok, err)
		}
		var ciphertext []byte
		if err = f.db.Pool.QueryRow(t.Context(), "SELECT credential_snapshot FROM payment_transactions WHERE provider_payment_id=$1", reference).Scan(&ciphertext); err != nil || len(ciphertext) == 0 || bytes.Contains(ciphertext, []byte("unavailable")) {
			t.Fatal("unavailable snapshot not encrypted", err)
		}
		if _, err = f.db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET credential_snapshot=''::bytea WHERE provider_payment_id=$1", reference); err == nil {
			t.Fatal("unavailable snapshot mutable")
		}
	}
	if _, err = f.db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET expires_at=now()-interval '1 hour' WHERE invoice_id=$1", newInvoice); err != nil {
		t.Fatal(err)
	}
	settings["expected_version"], settings["key"] = 2, "next-merchant-secret"
	expect(t, request(f.h, "PUT", paymentSettingsPath, settings, f.root, ""), 200)
	configuration, err = f.db.PaymentConfiguration(t.Context(), fallback)
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range references {
		if gateway, ok, err := f.db.PaymentTransactionGateway(t.Context(), "epay_alipay", reference, configuration); err != nil || ok || gateway != nil {
			t.Fatal("later rotation revived unavailable credentials", ok, err)
		}
	}
}

func TestLegacyNonterminalPaymentLostCredentialsBlockNewMerchant(t *testing.T) {
	for _, state := range []string{"pending", "expired"} {
		t.Run(state, func(t *testing.T) {
			f := configurationUpgradeFixture(t, false)
			f.h = New(f.db, settingsPaymentConfig(), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
			invoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "legacy-nonterminal-invoice")
			reference := fmt.Sprintf("epay_%d_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", invoice)
			if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,expires_at)
 VALUES('epay_alipay',$1,$2,$3,3000,$4,now()-interval '1 hour')`, reference, invoice, f.uid, state); err != nil {
				t.Fatal(err)
			}
			response := request(f.h, "PUT", paymentSettingsPath, paymentSettingsBody("https://replacement.example", 1), f.root, "")
			expect(t, response, 409)
			if errorData(t, response)["code"] != "payment_credentials_unavailable" {
				t.Fatal("nonterminal missing credentials not rejected", response.Body.String())
			}
			var version int64
			var snapshotLength int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT ps.version,octet_length(pt.credential_snapshot)
 FROM payment_settings ps CROSS JOIN payment_transactions pt WHERE ps.id=1 AND pt.provider_payment_id=$1`, reference).Scan(&version, &snapshotLength); err != nil || version != 1 || snapshotLength != 0 {
				t.Fatal("rejected rotation changed credentials", version, snapshotLength, err)
			}
		})
	}
}
