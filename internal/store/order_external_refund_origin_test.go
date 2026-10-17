package store

import "testing"

func TestOrderExternalRefundImportOriginCannotBeCleared(t *testing.T) {
	for _, object := range []string{"order", "invoice", "invoice_review"} {
		t.Run(object, func(t *testing.T) {
			f := newOrderRefundFixture(t, false)
			ctx := WithExternalRefundSession(t.Context(), "order-refund-request")
			set := map[string]string{
				"order":          `UPDATE orders SET legacy_source='isolated-origin',legacy_id=987654 WHERE id=$1`,
				"invoice":        `UPDATE invoices SET legacy_source='isolated-origin',legacy_id=987654 WHERE id=$1`,
				"invoice_review": `UPDATE invoices SET import_review_required=true WHERE id=$1`,
			}[object]
			clear := map[string]string{
				"order":          `UPDATE orders SET legacy_source='',legacy_id=NULL WHERE id=$1`,
				"invoice":        `UPDATE invoices SET legacy_source='',legacy_id=NULL WHERE id=$1`,
				"invoice_review": `UPDATE invoices SET import_review_required=false WHERE id=$1`,
			}[object]
			id := f.invoice
			if object == "order" {
				id = f.order
			}
			for step, query := range []string{set, clear} {
				if _, err := f.db.Pool.Exec(ctx, query, id); err != nil {
					t.Fatal(err)
				}
				c, err := f.db.ExternalRefundOrderContext(ctx, f.actor, f.invoice)
				if err != nil || c.Eligible || c.ReasonCode == "" {
					t.Fatalf("origin step %d incorrectly eligible: %v", step, err)
				}
				input := f.in
				input.ExpectedVersion, input.ExpectedOrderVersion = c.InvoiceVersion, c.OrderVersion
				if _, err = f.db.CreateExternalRefund(ctx, f.actor, f.invoice, input, "origin-must-not-be-adopted"); err == nil {
					t.Fatal("import origin accepted for order refund")
				}
			}
			var holds, refunds, stock int
			if err := f.db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM external_refund_order_holds),(SELECT count(*) FROM external_refunds),stock FROM products WHERE id=$1`, f.product).Scan(&holds, &refunds, &stock); err != nil || holds != 0 || refunds != 0 || stock != 10 {
				t.Fatal("rejected origin created refund or inventory facts", err)
			}
		})
	}
}
