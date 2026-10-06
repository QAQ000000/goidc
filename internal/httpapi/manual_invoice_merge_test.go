package httpapi

import (
	"fmt"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestManualInvoiceMerge(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	a := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "merge-source-a")
	bInput := manualInvoiceInput(f.uid)
	bInput.Items = []store.ManualInvoiceItem{{Description: "第二项", AmountCents: 1200}}
	b := manualInvoiceCreate(t, f.h, f.root, bInput, "merge-source-b")
	in := store.ManualInvoiceMergeInput{Sources: []store.ManualInvoiceMergeSource{{InvoiceID: a, ExpectedVersion: 1}, {InvoiceID: b, ExpectedVersion: 1}}}
	w := request(f.h, "POST", "/api/v1/admin/invoices/merge", in, f.root, "merge-operation")
	if w.Code != 201 {
		t.Fatalf("merge status %d: %s", w.Code, w.Body.String())
	}
	result := data(t, w)
	merged := int64(result["invoice_id"].(float64))
	if result["amount_cents"] != float64(4200) {
		t.Fatalf("merged amount %#v", result)
	}
	if replay := request(f.h, "POST", "/api/v1/admin/invoices/merge", in, f.root, "merge-operation"); replay.Code != 200 || !bool(data(t, replay)["replayed"].(bool)) {
		t.Fatalf("merge replay %d", replay.Code)
	}
	for _, id := range []int64{a, b} {
		if got := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/admin/invoices/%d", id), nil, f.root, ""))["status"]; got != "cancelled" {
			t.Fatalf("source %d status %v", id, got)
		}
	}
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/admin/invoices/%d", merged), nil, f.root, ""))
	if detail["invoice_kind"] != "manual" || len(detail["items"].([]any)) != 3 {
		t.Fatalf("merged detail %#v", detail)
	}
}
