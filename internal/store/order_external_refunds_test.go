package store

import (
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"testing"
	"time"
)

type orderRefundFixture struct {
	db                                                          *Store
	actor, reviewer, customer, invoice, order, payment, product int64
	in                                                          ExternalRefundInput
}

func newOrderRefundFixture(t *testing.T, infinite bool) orderRefundFixture {
	t.Helper()
	db := isolatedTestStore(t)
	actor, customer := topUpFixture(t, db)
	ctx := t.Context()
	f := orderRefundFixture{db: db, actor: actor, customer: customer}
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(db.Pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name,role) VALUES('order-reviewer','order-reviewer@example.test','unused','reviewer','super_admin') RETURNING id`).Scan(&f.reviewer))
	_, e := db.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('order-refund-request',$1,now()+interval '1 hour'),('order-refund-review',$2,now()+interval '1 hour');`, actor, f.reviewer)
	must(e)
	stock := any(12)
	if infinite {
		stock = nil
	}
	must(db.Pool.QueryRow(ctx, `INSERT INTO products(name,slug,price_cents,setup_fee_cents,stock,billing_cycle,approval_required) VALUES('Order refund','order-refund',1000,200,$1,'monthly',true) RETURNING id`, stock).Scan(&f.product))
	tx, e := db.Pool.Begin(ctx)
	must(e)
	defer tx.Rollback(ctx)
	result, e := db.persistCheckoutLines(ctx, tx, customer, "order-refund-checkout", "checkout-hash", []checkoutLine{{ProductID: f.product, Name: "Order refund", Cycle: "monthly", Qty: 2, UnitPrice: 1000, UnitSetup: 200, OptionIDs: []int64{}, OptionQuantities: map[int64]int64{}, Snapshot: []OptionSnapshot{}, PurchaseSnapshot: []PurchaseFieldSnapshot{}}}, CheckoutResult{TotalCents: 2400}, []RenewalBenefit{{}}, true, 0, checkoutOptions{GenerateInvoice: true, ActorID: actor, Source: "order-refund-test"})
	must(e)
	must(tx.Commit(ctx))
	f.order = result.OrderID
	f.invoice = result.InvoiceID
	pay, e := db.CreatePaymentIntent(ctx, customer, f.invoice, "mock", "order-refund-pay", "", "order-refund-pay-key")
	must(e)
	f.payment = pay.PaymentID
	_, e = db.ProcessPaymentWebhook(ctx, "mock", "order-refund-event", "order-refund-pay", f.invoice, 2400, "succeeded", []byte(`{"signed_fixture":"store verified gateway entrypoint"}`), "order-refund-transaction")
	must(e)
	c, e := db.ExternalRefundOrderContext(WithExternalRefundSession(ctx, "order-refund-request"), actor, f.invoice)
	must(e)
	if !c.Eligible {
		t.Fatalf("real checkout not eligible: %+v", c)
	}
	f.in = ExternalRefundInput{PaymentID: f.payment, AmountCents: 2400, Method: "original_route", ProviderRefundID: "order-refund-actual", ReceivedAt: time.Now(), EvidenceRef: "redacted/order-refund", EvidenceSHA256: strings.Repeat("a", 64), Note: "merchant actual refund evidence", ExpectedVersion: c.InvoiceVersion, ExpectedOrderVersion: c.OrderVersion}
	for _, v := range c.Services {
		f.in.ExpectedServiceVersions = append(f.in.ExpectedServiceVersions, ExternalRefundServiceVersion{ID: v.ID, Version: v.Version})
	}
	return f
}
func (f orderRefundFixture) create(t *testing.T) ExternalRefundResult {
	t.Helper()
	r, e := f.db.CreateExternalRefund(WithExternalRefundSession(t.Context(), "order-refund-request"), f.actor, f.invoice, f.in, "order-refund-create")
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func (f orderRefundFixture) review(t *testing.T, r ExternalRefundResult, confirm bool, key string) (ExternalRefundResult, error) {
	t.Helper()
	return f.db.ReviewExternalRefund(WithExternalRefundSession(t.Context(), "order-refund-review"), f.reviewer, f.invoice, r.Refund.ID, confirm, ExternalRefundReviewInput{ExpectedVersion: r.Refund.Version, Note: "independent original merchant receipt verification"}, key)
}
func TestOrderExternalRefundLifecycle(t *testing.T) {
	for _, infinite := range []bool{false, true} {
		t.Run(fmt.Sprint(infinite), func(t *testing.T) {
			f := newOrderRefundFixture(t, infinite)
			ctx := t.Context()
			var original string
			if e := f.db.Pool.QueryRow(ctx, `SELECT jsonb_build_array(to_jsonb(p),i.paid_at,(SELECT balance_cents FROM users WHERE id=i.user_id))::text FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id WHERE p.id=$1`, f.payment).Scan(&original); e != nil {
				t.Fatal(e)
			}
			pending := f.create(t)
			if pending.Refund.OrderReversal == nil || pending.Refund.OrderReversal.Status != "pending" || pending.Refund.OrderReversal.ServiceCount != 2 {
				t.Fatal(pending)
			}
			for _, q := range []string{`UPDATE orders SET review_status='approved' WHERE id=$1`, `UPDATE services SET status='active' WHERE order_id=$1`, `INSERT INTO provision_jobs(service_id,action,idempotency_key) SELECT id,'create','blocked-'||id FROM services WHERE order_id=$1`, `UPDATE order_items SET product_name='changed' WHERE order_id=$1`, `INSERT INTO invoice_items(invoice_id,item_type,description,amount_cents) SELECT id,'manual','blocked',amount_cents FROM invoices WHERE order_id=$1`} {
				if _, e := f.db.Pool.Exec(ctx, q, f.order); e == nil {
					t.Fatalf("hold bypass: %s", q)
				}
			}
			// Another checkout consuming this product must not invalidate the refund.
			if !infinite {
				if _, e := f.db.Pool.Exec(ctx, `UPDATE products SET stock=stock-3 WHERE id=$1`, f.product); e != nil {
					t.Fatal(e)
				}
			}
			applied, e := f.review(t, pending, true, "order-refund-confirm")
			if e != nil {
				t.Fatal(e)
			}
			if applied.Refund.OrderReversal.Status != "applied" || applied.Refund.OrderReversal.StockUnitsReleased != 2 {
				t.Fatal(applied)
			}
			replay, e := f.review(t, pending, true, "order-refund-confirm")
			if e != nil || !replay.Replayed {
				t.Fatal(replay, e)
			}
			var stock *int
			var orderStatus, reviewStatus, invoiceStatus, final string
			var cancelled, units int
			if e = f.db.Pool.QueryRow(ctx, `SELECT p.stock,o.status,o.review_status,i.status,(SELECT count(*) FROM services WHERE order_id=o.id AND status='cancelled' AND inventory_released),(SELECT sum(units)::int FROM external_refund_order_stock_releases WHERE refund_id=$2) FROM products p CROSS JOIN orders o JOIN invoices i ON i.order_id=o.id WHERE p.id=$1 AND o.id=$3`, f.product, pending.Refund.ID, f.order).Scan(&stock, &orderStatus, &reviewStatus, &invoiceStatus, &cancelled, &units); e != nil {
				t.Fatal(e)
			}
			if orderStatus != "refunded" || reviewStatus != "pending" || invoiceStatus != "refunded" || cancelled != 2 || units != 2 || (infinite && stock != nil) || (!infinite && (stock == nil || *stock != 9)) {
				t.Fatal(stock, orderStatus, reviewStatus, invoiceStatus, cancelled, units)
			}
			if _, e = f.db.ProcessPaymentWebhook(ctx, "mock", "order-refund-event", "order-refund-pay", f.invoice, 2400, "succeeded", []byte(`{"signed_fixture":"store verified gateway entrypoint"}`), "order-refund-transaction"); e != nil {
				t.Fatal(e)
			}
			if e = f.db.Pool.QueryRow(ctx, `SELECT jsonb_build_array(to_jsonb(p),i.paid_at,(SELECT balance_cents FROM users WHERE id=i.user_id))::text FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id WHERE p.id=$1`, f.payment).Scan(&final); e != nil || final != original {
				t.Fatal("original facts changed", e)
			}
		})
	}
}
func TestOrderExternalRefundRejectReleasesOnly(t *testing.T) {
	f := newOrderRefundFixture(t, false)
	pending := f.create(t)
	r, e := f.review(t, pending, false, "order-refund-reject")
	if e != nil {
		t.Fatal(e)
	}
	if r.Refund.OrderReversal.Status != "released" {
		t.Fatal(r)
	}
	c, e := f.db.ExternalRefundOrderContext(WithExternalRefundSession(t.Context(), "order-refund-request"), f.actor, f.invoice)
	if e != nil || !c.Eligible {
		t.Fatal(c, e)
	}
	if _, e = f.db.ReviewOrder(t.Context(), f.actor, f.order, "approved", "normal approval after release", "normal-review-after-refund-reject", c.OrderVersion); e != nil {
		t.Fatal(e)
	}
}
func TestOrderExternalRefundSnapshotAndAuditRollback(t *testing.T) {
	f := newOrderRefundFixture(t, false)
	ctx := t.Context()
	pending := f.create(t)
	for _, q := range []string{`UPDATE external_refund_order_services SET snapshot='{}' WHERE refund_id=$1`, `DELETE FROM external_refund_order_stock_reservations WHERE order_id=(SELECT order_id FROM external_refund_order_holds WHERE refund_id=$1)`, `UPDATE external_refunds SET status='confirmed',version=version+1,reviewed_by=2,confirmed_at=now() WHERE id=$1`, `INSERT INTO external_refund_order_stock_releases(refund_id,product_id,units) SELECT refund_id,product_id,2 FROM external_refund_order_services WHERE refund_id=$1 LIMIT 1`} {
		if _, e := f.db.Pool.Exec(ctx, q, pending.Refund.ID); e == nil {
			t.Fatalf("direct snapshot/final bypass: %s", q)
		}
	}
	_, e := f.db.Pool.Exec(ctx, `CREATE FUNCTION fail_order_refund_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='external_refund.order_reversed' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$;CREATE TRIGGER fail_order_refund_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION fail_order_refund_audit()`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.review(t, pending, true, "order-refund-audit-failure"); e == nil {
		t.Fatal("audit failure committed")
	}
	var state string
	var stock, release, receipts int
	if e = f.db.Pool.QueryRow(ctx, `SELECT h.status,p.stock,(SELECT count(*) FROM external_refund_order_stock_releases),(SELECT count(*) FROM external_refund_operations WHERE idempotency_key='order-refund-audit-failure') FROM external_refund_order_holds h CROSS JOIN products p WHERE h.refund_id=$1 AND p.id=$2`, pending.Refund.ID, f.product).Scan(&state, &stock, &release, &receipts); e != nil || state != "pending" || stock != 10 || release != 0 || receipts != 0 {
		t.Fatal(state, stock, release, receipts, e)
	}
}

func orderRefundWaitForBlocker(t *testing.T, db *Store, pid int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if e := db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected a real PostgreSQL row/advisory lock wait")
}
func TestOrderExternalRefundDeliveryAndItemWaits(t *testing.T) {
	for _, kind := range []string{"service", "job", "item"} {
		t.Run(kind, func(t *testing.T) {
			f := newOrderRefundFixture(t, false)
			ctx := t.Context()
			tx, e := f.db.Pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback(ctx)
			var pid int32
			if e = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			query := map[string]string{"service": `UPDATE services SET installed_os='{"id":"delivered"}' WHERE order_id=$1`, "job": `INSERT INTO provision_jobs(service_id,action,idempotency_key,status) SELECT id,'create','historical-job-'||id,'failed' FROM services WHERE order_id=$1`, "item": `UPDATE order_items SET product_name='changed after checkout' WHERE order_id=$1`}[kind]
			if _, e = tx.Exec(ctx, query, f.order); e != nil {
				t.Fatal(e)
			}
			result := make(chan error, 1)
			go func() {
				_, e := f.db.CreateExternalRefund(WithExternalRefundSession(ctx, "order-refund-request"), f.actor, f.invoice, f.in, "order-refund-race-writer-first")
				result <- e
			}()
			orderRefundWaitForBlocker(t, f.db, pid)
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			select {
			case e = <-result:
				if conflictCode(t, e) != "external_refund_delivery_started" {
					t.Fatal(e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("refund remained blocked")
			}
			var holds int
			if e = f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM external_refund_order_holds`).Scan(&holds); e != nil || holds != 0 {
				t.Fatal(holds, e)
			}
		})
	}
}
func TestOrderExternalRefundHoldWinsDeliveryWait(t *testing.T) {
	for _, kind := range []string{"service", "job", "item"} {
		t.Run(kind, func(t *testing.T) {
			f := newOrderRefundFixture(t, false)
			ctx := t.Context()
			_, e := f.db.Pool.Exec(ctx, `CREATE FUNCTION pause_order_refund_create() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='external_refund.created' THEN PERFORM pg_advisory_xact_lock(154001); END IF; RETURN NEW; END $$;CREATE TRIGGER pause_order_refund_create BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION pause_order_refund_create()`)
			if e != nil {
				t.Fatal(e)
			}
			blocker, e := f.db.Pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Rollback(ctx)
			var pid int32
			if e = blocker.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(154001)`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			refund := make(chan error, 1)
			go func() {
				_, e := f.db.CreateExternalRefund(WithExternalRefundSession(ctx, "order-refund-request"), f.actor, f.invoice, f.in, "order-refund-race-hold-first")
				refund <- e
			}()
			orderRefundWaitForBlocker(t, f.db, pid)
			var refundPID int32
			if e = f.db.Pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) LIMIT 1`, pid).Scan(&refundPID); e != nil {
				t.Fatal(e)
			}
			query := map[string]string{"service": `UPDATE services SET installed_os='{"id":"delivered"}' WHERE order_id=$1`, "job": `INSERT INTO provision_jobs(service_id,action,idempotency_key) SELECT id,'create','racing-job-'||id FROM services WHERE order_id=$1`, "item": `UPDATE order_items SET product_name='racing change' WHERE order_id=$1`}[kind]
			delivery := make(chan error, 1)
			go func() { _, e := f.db.Pool.Exec(ctx, query, f.order); delivery <- e }()
			orderRefundWaitForBlocker(t, f.db, refundPID)
			if e = blocker.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			select {
			case e = <-refund:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("refund remained blocked")
			}
			select {
			case e = <-delivery:
				if e == nil {
					t.Fatal("delivery mutation bypassed committed hold")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("delivery remained blocked")
			}
		})
	}
}
func TestOrderExternalRefundHistoricalChangesCannotBeErased(t *testing.T) {
	for _, column := range []string{"installed_os", "domain"} {
		t.Run(column, func(t *testing.T) {
			f := newOrderRefundFixture(t, false)
			ctx := t.Context()
			var service int64
			var original json.RawMessage
			if e := f.db.Pool.QueryRow(ctx, `SELECT id,to_jsonb(s) FROM services s WHERE order_id=$1 ORDER BY id LIMIT 1`, f.order).Scan(&service, &original); e != nil {
				t.Fatal(e)
			}
			if column == "installed_os" {
				if _, e := f.db.Pool.Exec(ctx, `UPDATE services SET installed_os='{"id":"os"}' WHERE id=$1`, service); e != nil {
					t.Fatal(e)
				}
				if _, e := f.db.Pool.Exec(ctx, `UPDATE services SET installed_os='{}' WHERE id=$1`, service); e != nil {
					t.Fatal(e)
				}
			} else {
				var raw map[string]any
				if e := json.Unmarshal(original, &raw); e != nil {
					t.Fatal(e)
				}
				if _, e := f.db.Pool.Exec(ctx, `UPDATE services SET domain='mutated-domain' WHERE id=$1`, service); e != nil {
					t.Fatal(e)
				}
				if _, e := f.db.Pool.Exec(ctx, `UPDATE services SET domain=$2 WHERE id=$1`, service, raw["domain"]); e != nil {
					t.Fatal(e)
				}
			}
			c, e := f.db.ExternalRefundOrderContext(WithExternalRefundSession(ctx, "order-refund-request"), f.actor, f.invoice)
			if e != nil || c.Eligible || c.ReasonCode != "external_refund_delivery_started" {
				t.Fatal(c, e)
			}
		})
	}
}
func TestOrderExternalRefundStockMutationCannotBeForged(t *testing.T) {
	f := newOrderRefundFixture(t, false)
	ctx := t.Context()
	pending := f.create(t)
	tx, e := f.db.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = lockExternalRefundOrder(ctx, tx, f.invoice); e != nil {
		t.Fatal(e)
	}
	if e = applyExternalRefundOrder(ctx, tx, f.reviewer, pending.Refund.ID, f.invoice); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `UPDATE external_refunds SET status='confirmed',version=version+1,reviewed_by=$2,review_note='forged stock test',confirmed_at=now() WHERE id=$1`, pending.Refund.ID, f.reviewer); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `UPDATE products SET stock=stock-2 WHERE id=$1`, f.product); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e == nil {
		t.Fatal("stock release fact committed without its actual stock result")
	}
	// A new direct-SQL order without the reserve fact never becomes eligible.
	var order, item, invoice int64
	if e = f.db.Pool.QueryRow(ctx, `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash,review_status) VALUES('FAKE-STOCK',$1,2400,'fake-stock-key','fake','pending') RETURNING id`, f.customer).Scan(&order); e != nil {
		t.Fatal(e)
	}
	if e = f.db.Pool.QueryRow(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'fake','monthly',2,1000,200,2400) RETURNING id`, order, f.product).Scan(&item); e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.Pool.Exec(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,unit_number) SELECT $1,$2,$3,$4,'fake','fake-stock-'||n,'monthly',n FROM generate_series(1,2) n`, f.customer, order, item, f.product); e != nil {
		t.Fatal(e)
	}
	if e = f.db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,order_id,amount_cents) VALUES('FAKE-STOCK',$1,$2,2400) RETURNING id`, f.customer, order).Scan(&invoice); e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.CreatePaymentIntent(ctx, f.customer, invoice, "mock", "fake-stock-payment", "", "fake-stock-payment-key"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.ProcessPaymentWebhook(ctx, "mock", "fake-stock-event", "fake-stock-payment", invoice, 2400, "succeeded", []byte(`{"signed_fixture":true}`), "fake-stock-transaction"); e != nil {
		t.Fatal(e)
	}
	c, e := f.db.ExternalRefundOrderContext(WithExternalRefundSession(ctx, "order-refund-request"), f.actor, invoice)
	if e != nil || c.Eligible {
		t.Fatal(c, e)
	}
}
func TestOrderCheckoutStockReservationRollback(t *testing.T) {
	f := newOrderRefundFixture(t, false)
	ctx := t.Context()
	tx, e := f.db.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	// The reserve fact runs its real subtraction; a later business/audit failure
	// must roll back both the units and every checkout row.
	_, e = f.db.persistCheckoutLines(ctx, tx, f.customer, "rollback-checkout", "rollback-hash", []checkoutLine{{ProductID: f.product, Name: "Order refund", Cycle: "monthly", Qty: 1, UnitPrice: 1000, UnitSetup: 200, OptionIDs: []int64{}, OptionQuantities: map[int64]int64{}, Snapshot: []OptionSnapshot{}, PurchaseSnapshot: []PurchaseFieldSnapshot{}}}, CheckoutResult{TotalCents: 1200}, []RenewalBenefit{{}}, true, 0, checkoutOptions{GenerateInvoice: true, ActorID: f.actor, Source: "rollback-test"})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	var stock, orders, reservations int
	if e = f.db.Pool.QueryRow(ctx, `SELECT stock,(SELECT count(*) FROM orders),(SELECT count(*) FROM external_refund_order_stock_reservations) FROM products WHERE id=$1`, f.product).Scan(&stock, &orders, &reservations); e != nil || stock != 10 || orders != 1 || reservations != 1 {
		t.Fatal(stock, orders, reservations, e)
	}
}

func TestOrderExternalRefundSnapshotSurvivesConnectionTimezone(t *testing.T) {
	f := newOrderRefundFixture(t, false)
	pending := f.create(t)
	ctx := t.Context()
	for _, zone := range []string{"UTC", "Pacific/Honolulu"} {
		cfg := f.db.Pool.Config()
		cfg.ConnConfig.RuntimeParams["timezone"] = zone
		next, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		previous := f.db.Pool
		f.db.Pool = next
		previous.Close()
		c, err := f.db.ExternalRefundOrderContext(WithExternalRefundSession(ctx, "order-refund-request"), f.actor, f.invoice)
		if err != nil || c.OrderReversal == nil || c.OrderReversal.Status != "pending" {
			t.Fatal(c, err)
		}
	}
	applied, err := f.review(t, pending, true, "order-refund-zone-confirm")
	if err != nil || applied.Refund.OrderReversal.Status != "applied" {
		t.Fatal(applied, err)
	}
}

func TestOrderExternalRefundReverseLockGuardsFailWithoutWaiting(t *testing.T) {
	f := newOrderRefundFixture(t, false)
	ctx := t.Context()
	pending := f.create(t)
	for _, kind := range []string{"reservation-parent", "snapshot-service", "snapshot-hold"} {
		t.Run(kind, func(t *testing.T) {
			blocker, e := f.db.Pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Rollback(ctx)
			queries := map[string]string{"reservation-parent": `SELECT id FROM orders WHERE id=$1 FOR UPDATE`, "snapshot-service": `SELECT id FROM services WHERE order_id=$1 ORDER BY id FOR UPDATE`, "snapshot-hold": `SELECT refund_id FROM external_refund_order_holds WHERE order_id=$1 FOR UPDATE`}
			if _, e = blocker.Exec(ctx, queries[kind], f.order); e != nil {
				t.Fatal(e)
			}
			result := make(chan error, 1)
			go func() {
				var e error
				if kind == "reservation-parent" {
					_, e = f.db.Pool.Exec(ctx, `INSERT INTO external_refund_order_stock_reservations(order_item_id,order_id,product_id,units,item_snapshot,service_ids) SELECT id,order_id,product_id,quantity,'{}','[]' FROM order_items WHERE order_id=$1`, f.order)
				} else {
					_, e = f.db.Pool.Exec(ctx, `INSERT INTO external_refund_order_services SELECT * FROM external_refund_order_services WHERE refund_id=$1`, pending.Refund.ID)
				}
				result <- e
			}()
			select {
			case e = <-result:
				if e == nil {
					t.Fatal("direct reverse-lock mutation accepted")
				}
			case <-time.After(2 * time.Second):
				_ = blocker.Rollback(ctx)
				t.Fatal("reverse-lock trigger blocked instead of failing immediately")
			}
		})
	}
}
