package httpapi

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Every rejection starts with a genuine externally paid cart/admin order. SQL
// below is adversarial persisted-state injection, not evidence of a supplier
// checkout, resource creation, hybrid payment, or affiliate payout lifecycle.
// Each boundary must commit and pass its own proof before testing refusal.
func TestOrderExternalRefundHTTPBroadRejections(t *testing.T) {
	type boundary struct {
		name        string
		statements  []string
		proof       string
		application string
	}
	cases := []boundary{
		{name: "missing_member", statements: []string{`DELETE FROM services WHERE id=@service`}, proof: `(SELECT count(*) FROM services WHERE order_id=@order)=2 AND EXISTS(SELECT 1 FROM external_refund_delivery_history WHERE order_id=@order AND event='service_deleted')`},
		{name: "setup_mismatch", statements: []string{`UPDATE order_items SET setup_fee_cents=setup_fee_cents+1 WHERE id=(SELECT min(id) FROM order_items WHERE order_id=@order)`}, proof: `EXISTS(SELECT 1 FROM order_items WHERE order_id=@order AND total_cents<>(price_cents+setup_fee_cents)*quantity)`},
		{name: "promo_mismatch", statements: []string{`UPDATE orders SET discount_cents=discount_cents+1 WHERE id=@order`}, proof: `(SELECT discount_cents FROM orders WHERE id=@order)=521`},
		{name: "quantity_mismatch", statements: []string{`UPDATE order_items SET quantity=quantity+1 WHERE id=(SELECT min(id) FROM order_items WHERE order_id=@order)`}, proof: `(SELECT sum(quantity) FROM order_items WHERE order_id=@order)=4`},
		{name: "total_mismatch", statements: []string{`UPDATE orders SET total_cents=total_cents+1 WHERE id=@order`}, proof: `(SELECT total_cents FROM orders WHERE id=@order)=@amount+1`},
		{name: "pre_released_stock", statements: []string{`UPDATE services SET inventory_released=true WHERE id=@service`}, proof: `(SELECT inventory_released FROM services WHERE id=@service) AND (SELECT sum(stock) FROM products)=197`},
		{name: "order_history_cleared", statements: []string{`UPDATE orders SET review_status='approved' WHERE id=@order`, `UPDATE orders SET review_status='pending' WHERE id=@order`}, proof: `EXISTS(SELECT 1 FROM orders WHERE id=@order AND status='paid' AND review_status='pending') AND NOT EXISTS(SELECT 1 FROM provision_jobs) AND EXISTS(SELECT 1 FROM external_refund_delivery_history WHERE order_id=@order)`},
		{name: "order_legacy", statements: []string{`UPDATE orders SET legacy_source='isolated-matrix-import',legacy_id=987654 WHERE id=@order`}, proof: `EXISTS(SELECT 1 FROM orders WHERE id=@order AND legacy_source<>'' AND legacy_id=987654)`},
		{name: "order_source_only", statements: []string{`UPDATE orders SET legacy_source='isolated-matrix-import' WHERE id=@order`}, proof: `EXISTS(SELECT 1 FROM orders WHERE id=@order AND legacy_source<>'' AND legacy_id IS NULL)`},
		{name: "order_id_only", statements: []string{`UPDATE orders SET legacy_id=987654 WHERE id=@order`}, proof: `EXISTS(SELECT 1 FROM orders WHERE id=@order AND legacy_source='' AND legacy_id=987654)`},
		{name: "order_flag_clear", statements: []string{`UPDATE orders SET legacy_source='isolated-matrix-import',legacy_id=987654 WHERE id=@order`, `UPDATE orders SET legacy_source='',legacy_id=NULL WHERE id=@order`}, proof: `EXISTS(SELECT 1 FROM orders WHERE id=@order AND legacy_source='' AND legacy_id IS NULL) AND EXISTS(SELECT 1 FROM external_refund_delivery_history WHERE order_id=@order)`},
		{name: "invoice_legacy", statements: []string{`UPDATE invoices SET legacy_source='isolated-matrix-import',legacy_id=987654 WHERE id=@invoice`}, proof: `EXISTS(SELECT 1 FROM invoices WHERE id=@invoice AND legacy_source<>'' AND legacy_id=987654)`},
		{name: "invoice_flag_clear", statements: []string{`UPDATE invoices SET legacy_source='isolated-matrix-import',legacy_id=987654 WHERE id=@invoice`, `UPDATE invoices SET legacy_source='',legacy_id=NULL WHERE id=@invoice`}, proof: `EXISTS(SELECT 1 FROM invoices WHERE id=@invoice AND legacy_source='' AND legacy_id IS NULL) AND EXISTS(SELECT 1 FROM external_refund_delivery_history WHERE order_id=@order AND event='invoice_origin_changed')`},
		{name: "invoice_import_clear", statements: []string{`UPDATE invoices SET import_review_required=true WHERE id=@invoice`, `UPDATE invoices SET import_review_required=false WHERE id=@invoice`}, proof: `NOT(SELECT import_review_required FROM invoices WHERE id=@invoice) AND EXISTS(SELECT 1 FROM external_refund_delivery_history WHERE order_id=@order AND event='invoice_origin_changed')`},
		{name: "service_import_clear", statements: []string{`UPDATE services SET import_review_required=true WHERE id=@service`, `UPDATE services SET import_review_required=false WHERE id=@service`}, proof: `NOT(SELECT import_review_required FROM services WHERE id=@service) AND EXISTS(SELECT 1 FROM external_refund_delivery_history WHERE order_id=@order)`},
		{name: "service_legacy_clear", statements: []string{`UPDATE services SET legacy_source='isolated-matrix-import',legacy_id=987654 WHERE id=@service`, `UPDATE services SET legacy_source='',legacy_id=NULL WHERE id=@service`}, proof: `EXISTS(SELECT 1 FROM services WHERE id=@service AND legacy_source='' AND legacy_id IS NULL) AND EXISTS(SELECT 1 FROM external_refund_delivery_history WHERE order_id=@order)`},
		{name: "balance_payment_link", statements: []string{`WITH changed AS (UPDATE users SET balance_cents=balance_cents-100 WHERE id=@user RETURNING balance_cents) INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id,idempotency_key) SELECT @user,'payment',-100,balance_cents,'invoice',@invoice,'matrix-balance-payment' FROM changed`}, proof: `EXISTS(SELECT 1 FROM balance_ledger WHERE reference_type='invoice' AND reference_id=@invoice AND entry_type='payment') AND (SELECT balance_cents FROM users WHERE id=@user)=12245`},
		{name: "credit_link", statements: []string{`INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,status,expected_version,idempotency_key) VALUES(@invoice,@user,100,100,'outstanding',1,'matrix-credit-link')`, `UPDATE customer_credit_accounts SET used_cents=100 WHERE user_id=@user`}, proof: `EXISTS(SELECT 1 FROM credit_invoice_payments WHERE invoice_id=@invoice AND amount_cents=100) AND (SELECT used_cents FROM customer_credit_accounts WHERE user_id=@user)=100`},
		{name: "configuration_link", statements: []string{`INSERT INTO service_configuration_changes(service_id,user_id,product_revision,service_version,billing_cycle,due_at,old_recurring_cents,new_recurring_cents,amount_cents,credit_cents,old_snapshot,new_snapshot,option_ids,option_quantities) SELECT id,user_id,1,version,'monthly',now()+interval '1 month',2000,2100,100,0,'[]','[]','[]','{}' FROM services WHERE id=@service`}, proof: `EXISTS(SELECT 1 FROM service_configuration_changes WHERE service_id=@service AND status='quoted')`},
		{name: "cancellation_link", statements: []string{`INSERT INTO service_cancellation_requests(service_id,user_id,type,reason,idempotency_key) VALUES(@service,@user,'immediate','isolated persisted cancellation boundary','matrix-cancellation')`}, proof: `EXISTS(SELECT 1 FROM service_cancellation_requests WHERE service_id=@service AND status='pending')`},
		{name: "traffic_link", statements: []string{`WITH p AS (INSERT INTO traffic_products(name,capacity_gb,price_cents) VALUES('isolated-matrix-traffic',1,100) RETURNING id) INSERT INTO traffic_orders(order_no,user_id,traffic_product_id,service_id,invoice_id,product_name,capacity_gb,amount_cents) SELECT 'matrix-traffic',@user,id,@service,@invoice,'isolated-matrix-traffic',1,100 FROM p`}, proof: `EXISTS(SELECT 1 FROM traffic_orders WHERE invoice_id=@invoice AND service_id=@service)`},
		{name: "invoice_application_pending", application: "pending", proof: `EXISTS(SELECT 1 FROM invoice_applications a JOIN invoice_application_items x ON x.application_id=a.id WHERE x.source_invoice_id=@invoice AND a.status='pending')`},
		{name: "invoice_application_issued", application: "issued", proof: `EXISTS(SELECT 1 FROM invoice_applications a JOIN invoice_application_items x ON x.application_id=a.id WHERE x.source_invoice_id=@invoice AND a.status='issued')`},
	}
	for _, mode := range []string{"supplier", "nokvm", "simulation"} {
		cases = append(cases, boundary{name: "mode_" + mode, statements: []string{fmt.Sprintf(`UPDATE services SET provision_mode='%s' WHERE id=@service`, mode)}, proof: fmt.Sprintf(`(SELECT provision_mode FROM services WHERE id=@service)='%s'`, mode)})
	}
	for _, status := range []string{"pending", "failed", "cancelled"} {
		for _, deleted := range []bool{false, true} {
			name := "job_" + status
			statements := []string{fmt.Sprintf(`INSERT INTO provision_jobs(service_id,action,status,idempotency_key) VALUES(@service,'create','%s','matrix-job')`, status)}
			proof := fmt.Sprintf(`EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=@service AND status='%s')`, status)
			if deleted {
				name += "_deleted"
				statements = append(statements, `DELETE FROM provision_jobs WHERE service_id=@service`)
				proof = `NOT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=@service)`
			}
			proof += ` AND EXISTS(SELECT 1 FROM external_refund_delivery_history WHERE order_id=@order AND event='job_history')`
			cases = append(cases, boundary{name: name, statements: statements, proof: proof})
		}
	}
	for _, resource := range []struct{ name, set, reset, proof string }{
		{"upstream_id", `upstream_id='isolated-resource'`, `upstream_id=''`, `upstream_id=''`},
		{"username", `username='isolated-user'`, `username=''`, `username=''`},
		{"resource_info", `resource_info='{"test":true}'`, `resource_info='{}'`, `resource_info='{}'::jsonb`},
		{"installed_os", `installed_os='{"test":true}'`, `installed_os='{}'`, `installed_os='{}'::jsonb`},
		{"credentials", `credentials_encrypted=decode('01','hex')`, `credentials_encrypted=NULL`, `credentials_encrypted IS NULL`},
		{"next_due", `next_due_at=now()+interval '1 month'`, `next_due_at=NULL`, `next_due_at IS NULL`},
	} {
		cases = append(cases, boundary{name: "resource_reverted_" + resource.name, statements: []string{`UPDATE services SET ` + resource.set + ` WHERE id=@service`, `UPDATE services SET ` + resource.reset + ` WHERE id=@service`}, proof: `EXISTS(SELECT 1 FROM services WHERE id=@service AND ` + resource.proof + `) AND EXISTS(SELECT 1 FROM external_refund_delivery_history WHERE order_id=@order)`})
	}
	for _, status := range []string{"pending", "expired"} {
		cases = append(cases, boundary{name: "extra_intent_" + status, statements: []string{fmt.Sprintf(`INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status) VALUES('mock','matrix-extra-intent',@invoice,@user,@amount,'%s')`, status)}, proof: fmt.Sprintf(`EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=@invoice AND status='%s') AND (SELECT count(*) FROM payment_transactions WHERE invoice_id=@invoice)=2`, status)})
	}
	for _, kind := range []string{"renew", "upgrade"} {
		cases = append(cases, boundary{name: kind + "_invoice_link", statements: []string{fmt.Sprintf(`WITH i AS (INSERT INTO invoices(invoice_no,user_id,amount_cents) VALUES('matrix-service-link',@user,100) RETURNING id) INSERT INTO invoice_items(invoice_id,service_id,description,item_type,amount_cents) SELECT id,@service,'isolated service association','%s',100 FROM i`, kind)}, proof: fmt.Sprintf(`EXISTS(SELECT 1 FROM invoice_items WHERE service_id=@service AND invoice_id<>@invoice AND item_type='%s')`, kind)})
	}
	affiliateSetup := []string{
		`INSERT INTO affiliate_programs(name) VALUES('isolated-matrix-program')`,
		`INSERT INTO affiliate_rule_sets(program_id,version) SELECT id,1 FROM affiliate_programs WHERE name='isolated-matrix-program'`,
		`INSERT INTO affiliate_accounts(user_id,program_id,status) SELECT u.id,p.id,'approved' FROM users u CROSS JOIN affiliate_programs p WHERE u.username='order-refund-customer' AND p.name='isolated-matrix-program'`,
	}
	cases = append(cases, boundary{name: "turnover_without_commission", statements: append(append([]string{}, affiliateSetup...), `INSERT INTO affiliate_turnover_entries(invoice_id,account_id,rule_set_id,basis_cents,paid_at) SELECT @invoice,a.id,r.id,@amount,now() FROM affiliate_accounts a JOIN affiliate_rule_sets r ON r.program_id=a.program_id`), proof: `EXISTS(SELECT 1 FROM affiliate_turnover_entries WHERE invoice_id=@invoice) AND NOT EXISTS(SELECT 1 FROM affiliate_commission_entries WHERE invoice_id=@invoice)`})
	for _, status := range []string{"pending", "available", "reserved", "paid", "reversed", "clawback"} {
		bucket := status
		reversal := "NULL"
		if status == "reversed" {
			reversal = "now()"
		}
		if status == "clawback" {
			bucket = "paid"
			reversal = "now()"
		}
		insert := fmt.Sprintf(`INSERT INTO affiliate_commission_entries(account_id,referred_user_id,order_id,invoice_id,order_item_id,payment_transaction_id,product_id,event_type,status,amount_cents,basis_cents,rate_bps,available_at,rule_snapshot,source_snapshot,idempotency_key,%s_cents,reversal_at) SELECT a.id,@user,@order,@invoice,oi.id,@payment,oi.product_id,'first_purchase','%s',100,1000,1000,now(),'{}','{}','matrix-commission',100,%s FROM affiliate_accounts a CROSS JOIN order_items oi WHERE oi.id=(SELECT min(id) FROM order_items WHERE order_id=@order)`, bucket, status, reversal)
		cases = append(cases, boundary{name: "commission_" + status, statements: append(append([]string{}, affiliateSetup...), insert), proof: fmt.Sprintf(`EXISTS(SELECT 1 FROM affiliate_commission_entries WHERE invoice_id=@invoice AND status='%s' AND amount_cents=100 AND %s_cents=100)`, status, bucket)})
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrderExternalRefundHTTPFixture(t)
			mode := "cart"
			if index%2 == 1 {
				mode = "admin"
			}
			out := f.paid(t, mode, "matrix-"+tc.name)
			input := f.input(t, out, "matrix-proof-"+tc.name)
			var sid int64
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT min(id) FROM services WHERE order_id=$1`, out.order).Scan(&sid); err != nil {
				t.Fatal(err)
			}
			repl := strings.NewReplacer("@order", fmt.Sprint(out.order), "@invoice", fmt.Sprint(out.invoice), "@service", fmt.Sprint(sid), "@user", fmt.Sprint(f.uid), "@payment", fmt.Sprint(out.payment), "@amount", fmt.Sprint(out.amount))
			for _, statement := range tc.statements {
				tag, err := f.db.Pool.Exec(t.Context(), repl.Replace(statement))
				if err != nil || tag.RowsAffected() == 0 {
					t.Fatalf("boundary construction failed: rows=%d err=%v", tag.RowsAffected(), err)
				}
			}
			if tc.application != "" {
				orderExternalRefundApplicationBoundary(t, f, out, tc.application)
			}
			var proven bool
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT "+repl.Replace(tc.proof)).Scan(&proven); err != nil || !proven {
				t.Fatalf("boundary proof failed: proven=%v err=%v", proven, err)
			}
			before := orderExternalRefundBoundarySnapshot(t, f)
			c := f.context(t, out, f.root)
			if c["eligible"] != false || c["reason_code"] == "" {
				t.Errorf("constructed boundary remains eligible=%v reason=%v", c["eligible"], c["reason_code"])
			}
			// Refresh the complete current versions, including the missing-member case,
			// so an unrelated stale-version failure cannot masquerade as eligibility.
			input["expected_version"] = c["invoice_version"]
			input["expected_order_version"] = c["order_version"]
			versions := []map[string]any{}
			for _, raw := range c["services"].([]any) {
				s := raw.(map[string]any)
				versions = append(versions, map[string]any{"id": s["id"], "version": s["version"]})
			}
			input["expected_service_versions"] = versions
			w := request(f.h, "POST", orderExternalRefundPath(out.invoice), input, f.root, "matrix-create-"+tc.name)
			expect(t, w, 409)
			if code := errorData(t, w)["code"]; code == "version_conflict" {
				t.Fatal("refusal came from stale version rather than constructed boundary")
			}
			orderExternalRefundUnchanged(t, before, orderExternalRefundBoundarySnapshot(t, f))
			var residue int64
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM external_refunds)+(SELECT count(*) FROM external_refund_order_holds)+(SELECT count(*) FROM external_refund_order_stock_releases)+(SELECT count(*) FROM external_refund_payout_claims)`).Scan(&residue); err != nil || residue != 0 {
				t.Fatalf("refusal residue=%d err=%v", residue, err)
			}
			t.Logf("real %s fixture: boundary committed and proven; context denied; POST409; domain snapshot unchanged", mode)
		})
	}
}

func orderExternalRefundBoundarySnapshot(t *testing.T, f orderExternalRefundHTTPFixture) map[string]string {
	t.Helper()
	result := f.snapshot(t)
	for _, name := range []string{"invoice_profiles", "invoice_addresses", "invoice_applications", "invoice_application_items", "mutation_operations", "service_configuration_changes", "service_cancellation_requests", "traffic_orders", "traffic_products", "affiliate_programs", "affiliate_rule_sets", "affiliate_accounts", "affiliate_commission_events", "affiliate_withdrawals", "affiliate_withdrawal_allocations", "affiliate_clawbacks"} {
		var value string
		if err := f.db.Pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text COLLATE "C"),'[]')::text FROM `+pgx.Identifier{name}.Sanitize()+` r`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		result[name] = value
	}
	return result
}

func orderExternalRefundApplicationBoundary(t *testing.T, f orderExternalRefundHTTPFixture, out orderExternalRefundHTTPOrder, status string) {
	t.Helper()
	p := request(f.h, "POST", "/api/v1/invoice-profiles", store.InvoiceProfileInput{ProfileType: "company", Title: "Isolated matrix company", TaxNumber: "MATRIX-TEST"}, f.customer, "")
	expect(t, p, 201)
	a := request(f.h, "POST", "/api/v1/invoice-addresses", store.InvoiceAddressInput{Recipient: "Matrix customer", Phone: "13800000000", Address: "Isolated test address", IsDefault: true}, f.customer, "")
	expect(t, a, 201)
	w := request(f.h, "POST", "/api/v1/invoice-applications", store.InvoiceApplicationInput{ProfileID: int64(data(t, p)["id"].(float64)), AddressID: int64(data(t, a)["id"].(float64)), InvoiceIDs: []int64{out.invoice}}, f.customer, "matrix-application-create")
	expect(t, w, 201)
	if status == "issued" {
		id := int64(data(t, w)["application_id"].(float64))
		expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoice-applications/%d/review", id), store.InvoiceApplicationReview{Status: "issued", DocumentNumber: "MATRIX-ISSUED", ExpectedVersion: 1}, f.root, "matrix-application-issue"), 201)
	}
}

// Unlike mutable resource fields above, a paid service delivery snapshot cannot
// legally be changed and reverted. Prove that construction limit explicitly;
// this is an immutable-field test, not HTTP refund-refusal evidence.
func TestOrderExternalRefundDeliverySnapshotBoundaryImmutable(t *testing.T) {
	t.Helper()
	f := newOrderExternalRefundHTTPFixture(t)
	out := f.paid(t, "cart", "matrix-immutable-delivery")
	before := orderExternalRefundBoundarySnapshot(t, f)
	_, err := f.db.Pool.Exec(t.Context(), `UPDATE services SET delivery_snapshot='{"test":true}' WHERE id=(SELECT min(id) FROM services WHERE order_id=$1)`, out.order)
	var postgres *pgconn.PgError
	if !errors.As(err, &postgres) || postgres.Code != "P0001" || !strings.Contains(postgres.Message, "service delivery snapshot is immutable") {
		t.Fatalf("expected immutable snapshot construction rejection: %v", err)
	}
	orderExternalRefundUnchanged(t, before, orderExternalRefundBoundarySnapshot(t, f))
	if c := f.context(t, out, f.root); c["eligible"] != true {
		t.Fatalf("rolled back boundary unexpectedly changed eligibility: %v", c["reason_code"])
	}
	t.Log("delivery_snapshot edit/revert boundary cannot be legally constructed: database rejects edit atomically; original paid fixture remains eligible; no HTTP refusal claimed")
}
