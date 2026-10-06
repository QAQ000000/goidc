package httpapi

import (
	"fmt"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestBillingCycleQuoteSubmitAndDelivery(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents,active) VALUES($1,'quarterly',6000,0,true)`, f.pid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE products SET config_upgrade_enabled=true WHERE id=$1`, f.pid); err != nil {
		t.Fatal(err)
	}
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
	quote := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/billing-cycle-quotes", f.sid), store.BillingCycleChangeInput{TargetBillingCycle: "quarterly", ExpectedVersion: int64(detail["version"].(float64)), ExpectedRevision: *configRevision(t, f.h, f.root, f.pid)}, f.customer, "")
	if quote.Code != 201 {
		t.Fatalf("quote status %d: %s", quote.Code, quote.Body.String())
	}
	q := data(t, quote)
	if q["billing_cycle"] != "monthly" || q["new_billing_cycle"] != "quarterly" || q["new_recurring_cents"] != float64(6500) {
		t.Fatalf("unexpected quote: %#v", q)
	}
	change := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/configuration-changes", f.sid), map[string]any{"quote_id": q["id"]}, f.customer, "billing-cycle-submit")
	if change.Code != 201 {
		t.Fatalf("submit status %d: %s", change.Code, change.Body.String())
	}
	invoice := int64(data(t, change)["invoice_id"].(float64))
	if pay := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "billing-cycle-pay"); pay.Code != 201 {
		t.Fatalf("pay status %d: %s", pay.Code, pay.Body.String())
	}
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil || job == nil {
		t.Fatal("billing cycle job missing", err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	var cycle string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT billing_cycle FROM services WHERE id=$1", f.sid).Scan(&cycle); err != nil || cycle != "quarterly" {
		t.Fatal("service cycle not changed", cycle, err)
	}
}
