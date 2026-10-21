package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

type serviceTransferFixture struct {
	adminRenewalFixture
	target       int64
	targetCookie string
}

func serviceTransferGrants(t *testing.T, f adminRenewalFixture) {
	t.Helper()
	contentRole(t, f.db, f.actor, "services.view", "customers.view", "services.transfer")
}

func newServiceTransferFixture(t *testing.T, manual bool) serviceTransferFixture {
	t.Helper()
	f := managementFixture(t, manual)
	target, cookie := user(t, f.db, f.h, "service-transfer-recipient", "customer")
	serviceTransferGrants(t, f)
	return serviceTransferFixture{f, target, cookie}
}

func serviceTransferInput(t *testing.T, f serviceTransferFixture, source, target, generation int64) map[string]any {
	t.Helper()
	return map[string]any{
		"expected_version": f.version(t), "expected_owner_generation": generation,
		"source_customer_id": source, "target_customer_id": target,
		"credential_handover_confirmed": true,
		"reason":                        "按双方工单核对后转移服务归属",
		"handover_evidence":             "已在实际资源侧核对并完成旧访问凭据轮换与接收方交接；本字段不含密码或控制台链接",
	}
}

func serviceTransferCopy(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for key, value := range in {
		out[key] = value
	}
	return out
}

func serviceTransferFacts(t *testing.T, f serviceTransferFixture) string {
	t.Helper()
	var facts string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_array(
 to_jsonb(s),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM service_ownership_transfers x WHERE x.service_id=s.id),
 (SELECT count(*) FROM audit_events),(SELECT count(*) FROM mutation_operations),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM notification_deliveries x),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM notification_events x))::text
 FROM services s WHERE s.id=$1`, f.sid).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	return serviceTransferPreservedFacts(t, f) + facts
}

// Ownership is a new fact. Neither a preview nor the transfer may rewrite the
// original customer's purchases, receipts, wallet, attribution or contract.
func serviceTransferPreservedFacts(t *testing.T, f serviceTransferFixture) string {
	t.Helper()
	var facts string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_array(
 to_jsonb(s)-ARRAY['user_id','auto_renew','auto_renew_error','renewal_generation','version','updated_at'],
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM users x),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM orders x),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM order_items x),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM invoices x),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM invoice_items x),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM traffic_orders x),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM traffic_products x),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM balance_ledger x),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM payment_transactions x),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM credit_repayments x),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM affiliate_commission_entries x),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.invoice_id),'[]') FROM invoice_affiliate_attributions x),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM provision_jobs x),
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM manual_resource_bindings x),
 (SELECT to_jsonb(x) FROM service_renewal_pricing x WHERE x.service_id=s.id),
 (SELECT to_jsonb(x) FROM service_term_management x WHERE x.service_id=s.id))::text
 FROM services s WHERE s.id=$1`, f.sid).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	return facts
}

func serviceTransferUnchanged(t *testing.T, f serviceTransferFixture, before string) {
	t.Helper()
	if after := serviceTransferFacts(t, f); after != before {
		t.Fatal("read, rejected request or receipt replay changed ownership or historical facts", before, after)
	}
}

func serviceTransferQuote(t *testing.T, f serviceTransferFixture, in map[string]any) map[string]any {
	t.Helper()
	before := serviceTransferFacts(t, f)
	w := request(f.h, "POST", adminRenewalPath(f.sid)+"/transfer-quotes", in, f.staff, "")
	expect(t, w, 200)
	q := data(t, w)
	if q["service_id"] != float64(f.sid) || q["source_customer_id"] != float64(in["source_customer_id"].(int64)) || q["target_customer_id"] != float64(in["target_customer_id"].(int64)) || q["quote_hash"] == nil || q["quote_hash"] == "" || q["expires_at"] == nil {
		t.Fatal("ownership preview omitted its exact source, recipient or confirmation window", w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("private ownership preview was cacheable", w.Header())
	}
	expires, err := time.Parse(time.RFC3339Nano, q["expires_at"].(string))
	if err != nil || expires.Unix()%300 != 0 || expires.After(time.Now().Add(301*time.Second)) {
		t.Fatal("ownership preview did not bind the next five-minute database window", q, err)
	}
	serviceTransferUnchanged(t, f, before)
	body := serviceTransferCopy(in)
	body["quote_hash"] = q["quote_hash"]
	return body
}

func serviceTransferApply(t *testing.T, f serviceTransferFixture, body map[string]any, key string) map[string]any {
	t.Helper()
	w := request(f.h, "POST", adminRenewalPath(f.sid)+"/transfers", body, f.staff, key)
	expect(t, w, 201)
	return data(t, w)
}

func TestAdminServiceTransferQuoteAndPreservedFinancialContract(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual=%t", manual), func(t *testing.T) {
			f := newServiceTransferFixture(t, manual)
			managementBenefit(t, f.adminRenewalFixture)
			contentRole(t, f.db, f.actor, "services.view", "services.pricing")
			managementSetPricing(t, f.adminRenewalFixture, "fixed", 700, "transfer-retain-fixed-price")
			serviceTransferGrants(t, f.adminRenewalFixture)
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET auto_renew=true,auto_renew_error='old owner diagnostic' WHERE id=$1", f.sid); err != nil {
				t.Fatal(err)
			}
			var generation int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT renewal_generation FROM services WHERE id=$1", f.sid).Scan(&generation); err != nil {
				t.Fatal(err)
			}
			body := serviceTransferQuote(t, f, serviceTransferInput(t, f, f.uid, f.target, 0))
			before, version := serviceTransferPreservedFacts(t, f), f.version(t)
			out := serviceTransferApply(t, f, body, "service-transfer-preserve-all")
			if out["source_customer_id"] != float64(f.uid) || out["target_customer_id"] != float64(f.target) || out["owner_generation"] != float64(1) || out["version"] != float64(version+1) || out["auto_renew"] != false || out["replayed"] == true {
				t.Fatal("transfer omitted original identities or failed one ownership CAS", out)
			}
			var owner, newGeneration, audits, receipts, facts int64
			var enabled bool
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT user_id,auto_renew,renewal_generation,
 (SELECT count(*) FROM service_ownership_transfers WHERE service_id=s.id),
 (SELECT count(*) FROM audit_events WHERE actor_id=$2 AND action='service.ownership.transferred' AND entity_type='service' AND entity_id=s.id::text),
 (SELECT count(*) FROM mutation_operations WHERE actor_id=$2 AND scope='admin:service.transfer' AND idempotency_key='service-transfer-preserve-all')
 FROM services s WHERE s.id=$1`, f.sid, f.actor).Scan(&owner, &enabled, &newGeneration, &facts, &audits, &receipts); err != nil || owner != f.target || enabled || newGeneration != generation+1 || facts != 1 || audits != 1 || receipts != 1 {
				t.Fatal("transfer did not atomically close old automatic authority and record provenance", owner, enabled, newGeneration, facts, audits, receipts, err)
			}
			if after := serviceTransferPreservedFacts(t, f); after != before {
				t.Fatal("ownership transfer rewrote historical money, delivery or pricing", before, after)
			}
			expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""), 404)
			expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.targetCookie, ""), 200)
			replayedFacts := serviceTransferFacts(t, f)
			w := request(f.h, "POST", adminRenewalPath(f.sid)+"/transfers", body, f.staff, "service-transfer-preserve-all")
			expect(t, w, 200)
			if data(t, w)["replayed"] != true || data(t, w)["owner_generation"] != float64(1) {
				t.Fatal("original transfer did not recover its historical receipt", w.Body.String())
			}
			serviceTransferUnchanged(t, f, replayedFacts)
			contentRole(t, f.db, f.actor, "services.view", "customers.view", "services.transfer", "services.renew", "invoices.view")
			w = request(f.h, "POST", adminRenewalPath(f.sid)+"/renewal-quotes", map[string]any{"expected_version": f.version(t)}, f.staff, "")
			expect(t, w, 200)
			q := data(t, w)
			if q["customer_id"] != float64(f.target) || q["amount_cents"] != float64(700) {
				t.Fatal("new customer lost the transferred fixed renewal contract", q)
			}
			w = request(f.h, "POST", adminRenewalPath(f.sid)+"/renewals", renewalCreateBody(q), f.staff, "transfer-new-owner-renewal")
			expect(t, w, 201)
			if data(t, w)["customer_id"] != float64(f.target) {
				t.Fatal("new renewal remained billed to the old customer", w.Body.String())
			}
		})
	}
}

func TestAdminServiceTransferStrictInputAndPermissions(t *testing.T) {
	f := newServiceTransferFixture(t, false)
	in := serviceTransferInput(t, f, f.uid, f.target, 0)
	body := serviceTransferQuote(t, f, in)
	path, preview := adminRenewalPath(f.sid)+"/transfers", adminRenewalPath(f.sid)+"/transfer-quotes"
	before := serviceTransferFacts(t, f)
	for _, cookie := range []string{"", f.customer, f.targetCookie, f.other} {
		expect(t, request(f.h, "POST", path, body, cookie, "transfer-wrong-audience"), 401)
	}
	expect(t, request(f.h, "POST", path, body, f.staff, ""), 400)
	for field := range body {
		candidate := serviceTransferCopy(body)
		delete(candidate, field)
		expect(t, request(f.h, "POST", path, candidate, f.staff, "transfer-missing-field"), 400)
	}
	for _, change := range []struct {
		field string
		value any
	}{
		{"expected_version", 0}, {"expected_owner_generation", -1}, {"source_customer_id", 0}, {"target_customer_id", 0},
		{"credential_handover_confirmed", false}, {"reason", " "}, {"reason", strings.Repeat("中", 501)},
		{"handover_evidence", " "}, {"handover_evidence", strings.Repeat("中", 501)}, {"quote_hash", "wrong"},
		{"auto_renew", true}, {"status", "active"}, {"price_cents", 0},
	} {
		candidate := serviceTransferCopy(body)
		candidate[change.field] = change.value
		expect(t, request(f.h, "POST", path, candidate, f.staff, "transfer-invalid-input"), 400)
	}
	for _, grants := range [][]string{
		{"services.view", "customers.view", "services.manage"},
		{"services.transfer", "customers.view"}, {"services.transfer", "services.view"},
	} {
		contentRole(t, f.db, f.actor, grants...)
		expect(t, request(f.h, "POST", preview, in, f.staff, ""), 403)
		expect(t, request(f.h, "POST", path, body, f.staff, "transfer-missing-permission"), 403)
	}
	serviceTransferGrants(t, f.adminRenewalFixture)
	serviceTransferUnchanged(t, f, before)
}

func TestAdminServiceTransferBothCustomerScopeAndActiveIdentity(t *testing.T) {
	for _, scenario := range []string{"source_outside_scope", "target_outside_scope", "disabled_source", "disabled_target", "administrator_target", "missing_target", "same_customer"} {
		t.Run(scenario, func(t *testing.T) {
			f := newServiceTransferFixture(t, false)
			in := serviceTransferInput(t, f, f.uid, f.target, 0)
			body := serviceTransferQuote(t, f, in)
			status := 404
			switch scenario {
			case "source_outside_scope", "target_outside_scope":
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor); err != nil {
					t.Fatal(err)
				}
				allowed := f.uid
				if scenario == "source_outside_scope" {
					allowed = f.target
				}
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", allowed, f.actor); err != nil {
					t.Fatal(err)
				}
			case "disabled_source", "disabled_target":
				status = 409
				id := f.uid
				if scenario == "disabled_target" {
					id = f.target
				}
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", id); err != nil {
					t.Fatal(err)
				}
			case "administrator_target":
				in["target_customer_id"], body["target_customer_id"] = f.actor, f.actor
			case "missing_target":
				in["target_customer_id"], body["target_customer_id"] = int64(999999999), int64(999999999)
			case "same_customer":
				in["target_customer_id"], body["target_customer_id"] = f.uid, f.uid
				status = 400
			}
			before := serviceTransferFacts(t, f)
			expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/transfer-quotes", in, f.staff, ""), status)
			expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/transfers", body, f.staff, "transfer-rejected-customer"), status)
			serviceTransferUnchanged(t, f, before)
		})
	}
}

func TestAdminServiceTransferBusinessFencesAndFreshRecheck(t *testing.T) {
	for _, scenario := range []string{"unpaid_renewal", "configuration_change", "released_inventory", "import_review", "terminated", "cancelled", "pending_job", "running_job", "waiting_manual_job", "waiting_verification_job", "failed_external_unconfirmed", "supplier_paid_renewal_missing_job", "supplier_paid_renewal_failed_job", "supplier_paid_renewal_cancelled_job", "traffic_unpaid", "traffic_paid_waiting_manual", "traffic_fulfilled", "traffic_refunded", "traffic_cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			f := newServiceTransferFixture(t, false)
			in := serviceTransferInput(t, f, f.uid, f.target, 0)
			body := serviceTransferQuote(t, f, in)
			var err error
			switch scenario {
			case "unpaid_renewal":
				contentRole(t, f.db, f.actor, "services.view", "invoices.view", "services.renew")
				f.createRenewal(t, "transfer-fenced-unpaid-renewal")
				serviceTransferGrants(t, f.adminRenewalFixture)
			case "configuration_change":
				f.submit(t, f.quote(t, f.large), "transfer-fenced-configuration")
			case "released_inventory":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET inventory_released=true WHERE id=$1", f.sid)
			case "import_review":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET import_review_required=true WHERE id=$1", f.sid)
			case "terminated", "cancelled":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET status=$2 WHERE id=$1", f.sid, scenario)
			case "traffic_unpaid", "traffic_paid_waiting_manual", "traffic_fulfilled", "traffic_refunded", "traffic_cancelled":
				// Exercise durable states through the real purchase, settlement,
				// verification and refund routes; a paid packet has no provision job.
				created := request(f.h, "POST", "/api/v1/admin/traffic-products", map[string]any{
					"name": "服务转移围栏流量包", "capacity_gb": 100, "price_cents": 500,
					"active": true, "stock": 3, "applicable_product_ids": []int64{f.pid},
				}, f.root, "transfer-traffic-product")
				expect(t, created, 201)
				product := int64(data(t, created)["id"].(float64))
				purchase := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/traffic-orders", f.sid), map[string]any{
					"traffic_product_id": product, "expected_version": 1,
				}, f.customer, "transfer-traffic-purchase")
				expect(t, purchase, 201)
				order := int64(data(t, purchase)["id"].(float64))
				invoice := int64(data(t, purchase)["invoice_id"].(float64))
				if scenario == "traffic_cancelled" {
					expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/traffic-orders/%d/cancel", order), map[string]any{
						"expected_version": 1,
					}, f.root, "transfer-traffic-cancel"), 200)
				} else if scenario != "traffic_unpaid" {
					expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "transfer-traffic-pay"), 201)
					if scenario == "traffic_fulfilled" {
						expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/traffic-orders/%d/delivery-verification", order), map[string]any{
							"expected_version": 2, "note": "已在隔离资源端核验流量交付", "acknowledged": true,
						}, f.root, "transfer-traffic-verify"), 200)
					} else if scenario == "traffic_refunded" {
						expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice), map[string]any{}, f.root, "transfer-traffic-refund"), 201)
					}
				}
				if scenario == "traffic_fulfilled" || scenario == "traffic_refunded" || scenario == "traffic_cancelled" {
					body = serviceTransferQuote(t, f, serviceTransferInput(t, f, f.uid, f.target, 0))
					before := serviceTransferPreservedFacts(t, f)
					out := serviceTransferApply(t, f, body, "transfer-resolved-traffic")
					if out["target_customer_id"] != float64(f.target) || out["owner_generation"] != float64(1) || serviceTransferPreservedFacts(t, f) != before {
						t.Fatal("resolved traffic blocked transfer or rewrote historical purchases, payments or stock", out)
					}
					var serviceOwner, orderOwner, invoiceOwner int64
					if err := f.db.Pool.QueryRow(t.Context(), `SELECT s.user_id,t.user_id,i.user_id FROM services s
 JOIN traffic_orders t ON t.service_id=s.id JOIN invoices i ON i.id=t.invoice_id WHERE t.id=$1`, order).
						Scan(&serviceOwner, &orderOwner, &invoiceOwner); err != nil || serviceOwner != f.target || orderOwner != f.uid || invoiceOwner != f.uid {
						t.Fatal("transfer moved resolved traffic financial ownership", serviceOwner, orderOwner, invoiceOwner, err)
					}
					return
				}
			case "supplier_paid_renewal_missing_job", "supplier_paid_renewal_failed_job", "supplier_paid_renewal_cancelled_job":
				// A synthetic paid supplier renewal has no completed delivery.
				// Missing and terminal unsuccessful jobs must retain the original
				// customer until this already collected entitlement is resolved.
				if _, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode='supplier' WHERE id=$1", f.sid); err != nil {
					t.Fatal(err)
				}
				var invoice int64
				if err = f.db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,idempotency_key)
 VALUES('TRANSFER-PAID-SUPPLIER-RENEWAL',$1,'paid',1000,$2) RETURNING id`, f.uid, scenario).Scan(&invoice); err != nil {
					t.Fatal(err)
				}
				if _, err = f.db.Pool.Exec(t.Context(), `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id)
 VALUES($1,'Unresolved paid supplier renewal','renew',1000,$2)`, invoice, f.sid); err != nil {
					t.Fatal(err)
				}
				if scenario != "supplier_paid_renewal_missing_job" {
					state := "failed"
					if scenario == "supplier_paid_renewal_cancelled_job" {
						state = "cancelled"
					}
					_, err = f.db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,request_payload)
 VALUES($1,'renew',$2,$3,jsonb_build_object('supplier_id',1,'account_version',1,'local_invoice_id',$4::bigint,
 'upstream_id',42,'product_id',7,'cycle','monthly','currency','CNY','cost_cents',1000,'maximum_cost_cents',1000,
 'previous_due','2026-10-21T00:00:00Z'))`, f.sid, fmt.Sprintf("supplier-renew-%d", invoice), state, invoice)
				}
			default:
				state := strings.TrimSuffix(scenario, "_job")
				if scenario == "failed_external_unconfirmed" {
					state = "failed"
				}
				_, err = f.db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,external_started_at)
 VALUES($1,'sync',$2,$3,CASE WHEN $4::boolean THEN clock_timestamp() ELSE NULL END)`, f.sid, "transfer-fenced-"+scenario, state, scenario == "failed_external_unconfirmed")
			}
			if err != nil {
				t.Fatal(err)
			}
			before := serviceTransferFacts(t, f)
			// Fixture mutations can leave the same service version; both endpoints
			// still have to evaluate the actual business state, not trust a quote.
			in["expected_version"] = f.version(t)
			preview := request(f.h, "POST", adminRenewalPath(f.sid)+"/transfer-quotes", in, f.staff, "")
			expect(t, preview, 409)
			if strings.HasPrefix(scenario, "supplier_paid_renewal_") && !strings.Contains(preview.Body.String(), "supplier_renewal_pending") {
				t.Fatal("paid supplier renewal was rejected without preserving its unresolved entitlement", preview.Body.String())
			}
			if scenario == "traffic_paid_waiting_manual" && !strings.Contains(preview.Body.String(), "service_traffic_delivery_pending") {
				t.Fatal("paid traffic was rejected without preserving its pending delivery", preview.Body.String())
			}
			expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/transfers", body, f.staff, "transfer-business-fenced"), 409)
			serviceTransferUnchanged(t, f, before)
		})
	}
}

func TestAdminServiceTransferVerifiedExternalFailureRequiresCurrentAudit(t *testing.T) {
	for _, scenario := range []string{"verified_not_applied", "result_without_audit", "audit_without_result", "audit_before_new_attempt"} {
		t.Run(scenario, func(t *testing.T) {
			f := newServiceTransferFixture(t, false)
			in := serviceTransferInput(t, f, f.uid, f.target, 0)
			body := serviceTransferQuote(t, f, in)
			// This SQL fixture models the durable output of reconciliation.
			// It neither calls nor proves an actual upstream verification.
			tx, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			var job int64
			if err = tx.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,attempts,
 external_started_at,result,completed_at,updated_at)
 VALUES($1,'sync','transfer-synthetic-verified-external-failure','failed',1,now()-interval '1 minute',
 CASE WHEN $2::boolean THEN '{}'::jsonb ELSE '{"verification_outcome":"not_applied"}'::jsonb END,now(),now()) RETURNING id`, f.sid, scenario == "audit_without_result").Scan(&job); err != nil {
				t.Fatal(err)
			}
			if scenario != "result_without_audit" {
				if err = store.Audit(t.Context(), tx, f.actor, "supplier.delivery.verified", "service", f.sid,
					map[string]any{"job_id": job, "outcome": "not_applied", "absence_acknowledged": true, "note": "synthetic isolated reconciliation evidence"}); err != nil {
					t.Fatal(err)
				}
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if scenario == "audit_before_new_attempt" {
				if _, err = f.db.Pool.Exec(t.Context(), `UPDATE provision_jobs SET attempts=attempts+1,
 updated_at=(SELECT max(a.created_at)+interval '1 microsecond' FROM audit_events a
 WHERE a.action='supplier.delivery.verified' AND a.entity_type='service' AND a.entity_id=$2::bigint::text
 AND a.payload->>'job_id'=$1::bigint::text) WHERE id=$1`, job, f.sid); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "verified_not_applied" {
				body = serviceTransferQuote(t, f, in)
				before := serviceTransferPreservedFacts(t, f)
				out := serviceTransferApply(t, f, body, "transfer-verified-external-failure")
				if out["target_customer_id"] != float64(f.target) || out["owner_generation"] != float64(1) || serviceTransferPreservedFacts(t, f) != before {
					t.Fatal("current verified nonapplication did not permit an ownership-only transition", out)
				}
				return
			}
			before := serviceTransferFacts(t, f)
			for _, operation := range []struct {
				path string
				body map[string]any
			}{
				{"/transfer-quotes", in}, {"/transfers", body},
			} {
				w := request(f.h, "POST", adminRenewalPath(f.sid)+operation.path, operation.body, f.staff, "transfer-unproven-external-failure")
				expect(t, w, 409)
				if !strings.Contains(w.Body.String(), "service_external_operation_pending") {
					t.Fatal("unproven or stale nonapplication evidence bypassed the external-operation fence", w.Body.String())
				}
				serviceTransferUnchanged(t, f, before)
			}
		})
	}
}

func TestAdminServiceTransferQuoteVersionGenerationAndIntentBinding(t *testing.T) {
	f := newServiceTransferFixture(t, false)
	in := serviceTransferInput(t, f, f.uid, f.target, 0)
	body := serviceTransferQuote(t, f, in)
	path := adminRenewalPath(f.sid) + "/transfers"
	before := serviceTransferFacts(t, f)
	for _, change := range []struct {
		field string
		value any
	}{
		{"reason", "未经本次预览的新原因"}, {"handover_evidence", "另一个未预览的交接声明"},
		{"expected_owner_generation", 1}, {"quote_hash", strings.Repeat("a", 64)},
	} {
		candidate := serviceTransferCopy(body)
		candidate[change.field] = change.value
		expect(t, request(f.h, "POST", path, candidate, f.staff, "transfer-bound-intent"), 409)
		serviceTransferUnchanged(t, f, before)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET version=version+1 WHERE id=$1", f.sid); err != nil {
		t.Fatal(err)
	}
	before = serviceTransferFacts(t, f)
	expect(t, request(f.h, "POST", path, body, f.staff, "transfer-stale-version"), 409)
	serviceTransferUnchanged(t, f, before)
	var rowLockCount int
	gate, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback(context.Background())
	if err = gate.QueryRow(t.Context(), "SELECT count(*) FROM (SELECT id FROM services WHERE id=$1 FOR UPDATE) s", f.sid).Scan(&rowLockCount); err != nil || rowLockCount != 1 {
		t.Fatal(err, rowLockCount)
	}
	in["expected_version"] = f.version(t)
	expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/transfer-quotes", in, f.staff, ""), 409)
}

func TestAdminServiceTransferConcurrentReplayAndCAS(t *testing.T) {
	for _, sameKey := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_key=%t", sameKey), func(t *testing.T) {
			f := newServiceTransferFixture(t, false)
			body := serviceTransferQuote(t, f, serviceTransferInput(t, f, f.uid, f.target, 0))
			before, version := serviceTransferPreservedFacts(t, f), f.version(t)
			start, done := make(chan struct{}), make(chan *httptest.ResponseRecorder, 2)
			for n := range 2 {
				key := "transfer-concurrent-same"
				if !sameKey {
					key = fmt.Sprintf("transfer-concurrent-%d", n)
				}
				go func(key string) {
					<-start
					done <- request(f.h, "POST", adminRenewalPath(f.sid)+"/transfers", body, f.staff, key)
				}(key)
			}
			close(start)
			created, replayed, conflicted := 0, 0, 0
			for range 2 {
				w := awaitBalanceResponse(t, done)
				switch w.Code {
				case 201:
					created++
				case 200:
					replayed++
				case 409:
					conflicted++
				default:
					t.Fatal("ownership concurrency returned an unexpected result", w.Code, w.Body.String())
				}
			}
			if created != 1 || (sameKey && replayed != 1) || (!sameKey && conflicted != 1) || f.version(t) != version+1 || serviceTransferPreservedFacts(t, f) != before {
				t.Fatal("ownership contention did not leave one immutable transition", created, replayed, conflicted)
			}
			if sameKey {
				body["reason"] = "不同的转移意图"
				facts := serviceTransferFacts(t, f)
				expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/transfers", body, f.staff, "transfer-concurrent-same"), 409)
				serviceTransferUnchanged(t, f, facts)
			}
		})
	}
}

func TestAdminServiceTransferRoundTripDoesNotReviveOldGeneration(t *testing.T) {
	f := newServiceTransferFixture(t, false)
	forward := serviceTransferQuote(t, f, serviceTransferInput(t, f, f.uid, f.target, 0))
	serviceTransferApply(t, f, forward, "transfer-round-trip-forward")
	backward := serviceTransferQuote(t, f, serviceTransferInput(t, f, f.target, f.uid, 1))
	out := serviceTransferApply(t, f, backward, "transfer-round-trip-backward")
	if out["owner_generation"] != float64(2) || out["target_customer_id"] != float64(f.uid) {
		t.Fatal("round trip erased the intervening ownership generation", out)
	}
	before := serviceTransferFacts(t, f)
	w := request(f.h, "POST", adminRenewalPath(f.sid)+"/transfers", forward, f.staff, "transfer-round-trip-forward")
	expect(t, w, 200)
	if data(t, w)["owner_generation"] != float64(1) || data(t, w)["replayed"] != true {
		t.Fatal("old transfer replay was mistaken for the current owner projection", w.Body.String())
	}
	serviceTransferUnchanged(t, f, before)
	stale := serviceTransferCopy(forward)
	stale["expected_version"] = f.version(t)
	expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/transfers", stale, f.staff, "transfer-round-trip-old-generation"), 409)
	serviceTransferUnchanged(t, f, before)
	expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.targetCookie, ""), 404)
}

func TestAdminServiceTransferTargetCannotReadPreviousCustomerDetails(t *testing.T) {
	f := newServiceTransferFixture(t, false)
	const secret = "previous-customer-private-purchase-and-job-marker"
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE order_items SET purchase_fields=jsonb_build_array(jsonb_build_object('key','old-contact','label','原客户资料','value',$2::text,'encrypted',false)) WHERE order_id=$1`, f.orderID, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,request_fingerprint,request_payload,result,last_error,completed_at)
 VALUES($1,'reinstall','transfer-old-owner-private-reinstall','succeeded',$3,
 jsonb_build_object('id','legacy-os','name',$2::text,'group','legacy-group','login','root'),
 jsonb_build_object('old_customer',$2::text),$2,clock_timestamp())`, f.sid, secret, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	body := serviceTransferQuote(t, f, serviceTransferInput(t, f, f.uid, f.target, 0))
	serviceTransferApply(t, f, body, "transfer-private-history")
	for _, suffix := range []string{"", "/activity"} {
		w := request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d%s", f.sid, suffix), nil, f.targetCookie, "")
		expect(t, w, 200)
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("new owner received the previous customer's purchase or job payload", w.Body.String())
		}
	}
	expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d/activity", f.sid), nil, f.customer, ""), 404)
}

func TestAdminServiceTransferUnknownCommitRecovery(t *testing.T) {
	f := newServiceTransferFixture(t, false)
	body := serviceTransferQuote(t, f, serviceTransferInput(t, f, f.uid, f.target, 0))
	var commits atomic.Int32
	cfg := f.db.Pool.Config().Copy()
	cfg.ConnConfig.BuildFrontend = func(reader io.Reader, writer io.Writer) *pgproto3.Frontend {
		return pgproto3.NewFrontend(&uploadCommitAckReader{reader: reader, count: &commits, dropAt: 1}, writer)
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	path := adminRenewalPath(f.sid) + "/transfers"
	w := request(renewalBatchHTTPWithPool(f.db, pool), "POST", path, body, f.staff, "transfer-lost-commit")
	if commits.Load() < 1 || (w.Code != 200 && w.Code != 201 && w.Code != 500 && w.Code != 503) {
		t.Fatal("test did not lose a real transfer COMMIT acknowledgement", commits.Load(), w.Code, w.Body.String())
	}
	before := serviceTransferFacts(t, f)
	w = request(f.h, "POST", path, body, f.staff, "transfer-lost-commit")
	expect(t, w, 200)
	if data(t, w)["replayed"] != true || data(t, w)["target_customer_id"] != float64(f.target) {
		t.Fatal("lost COMMIT acknowledgement did not recover the committed ownership receipt", w.Body.String())
	}
	serviceTransferUnchanged(t, f, before)
}

func TestAdminServiceTransferAuditAndReceiptRollback(t *testing.T) {
	f := newServiceTransferFixture(t, false)
	body := serviceTransferQuote(t, f, serviceTransferInput(t, f, f.uid, f.target, 0))
	before := serviceTransferFacts(t, f)
	for _, table := range []string{"audit_events", "mutation_operations", "service_ownership_transfers"} {
		if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_service_transfer_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated transfer durability failure'; END $$;
 CREATE TRIGGER reject_service_transfer_write BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_service_transfer_write()`); err != nil {
			t.Fatal(err)
		}
		expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/transfers", body, f.staff, "transfer-durability-rollback"), 500)
		serviceTransferUnchanged(t, f, before)
		if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER reject_service_transfer_write ON "+table+"; DROP FUNCTION reject_service_transfer_write()"); err != nil {
			t.Fatal(err)
		}
	}
	serviceTransferApply(t, f, body, "transfer-durability-rollback")
}

func TestAdminServiceTransferWaitingCredentialRevocationAndExpiry(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		for _, natural := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/natural=%t", credential, natural), func(t *testing.T) {
				f := newServiceTransferFixture(t, false)
				body := serviceTransferQuote(t, f, serviceTransferInput(t, f, f.uid, f.target, 0))
				secret, table, column, hash := adminRenewalTestCredential(t, f.adminRenewalFixture, credential)
				gate, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer gate.Rollback(context.Background())
				if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(741098551)"); err != nil {
					t.Fatal(err)
				}
				if _, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION wait_service_transfer_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='service.ownership.transferred' THEN PERFORM pg_advisory_xact_lock(741098551); END IF; RETURN NEW; END $$;
 CREATE TRIGGER wait_service_transfer_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_service_transfer_audit()`); err != nil {
					t.Fatal(err)
				}
				if natural {
					if _, err = f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()+interval '1200 milliseconds' WHERE "+column+"=$1", hash); err != nil {
						t.Fatal(err)
					}
				}
				before := serviceTransferFacts(t, f)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					done <- managementCredentialRequest(f.adminRenewalFixture, "POST", adminRenewalPath(f.sid)+"/transfers", body, "transfer-late-credential", secret)
				}()
				waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
				if natural {
					waitAdminRenewalCredentialExpiry(t, f.db, table, column, hash)
				} else {
					revokeCtx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
					if credential == "session" {
						_, err = f.db.Pool.Exec(revokeCtx, "DELETE FROM sessions WHERE token_hash=$1", hash)
					} else {
						_, err = f.db.Pool.Exec(revokeCtx, "UPDATE api_keys SET revoked_at=clock_timestamp() WHERE secret_hash=$1", hash)
					}
					cancel()
					if err != nil {
						t.Fatal("transfer business wait prematurely locked its original credential", err)
					}
				}
				if err = gate.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				expect(t, awaitBalanceResponse(t, done), 401)
				serviceTransferUnchanged(t, f, before)
			})
		}
	}
}

func TestAdminServiceTransferWaitingAuthorityChange(t *testing.T) {
	for _, operation := range []string{"preview", "apply"} {
		for _, authority := range []string{"role", "source_scope", "target_scope"} {
			t.Run(operation+"/"+authority, func(t *testing.T) {
				f := newServiceTransferFixture(t, false)
				body := serviceTransferQuote(t, f, serviceTransferInput(t, f, f.uid, f.target, 0))
				path := adminRenewalPath(f.sid) + "/transfers"
				if operation == "preview" {
					delete(body, "quote_hash")
					path = adminRenewalPath(f.sid) + "/transfer-quotes"
				}
				if authority != "role" {
					allowed := f.uid
					if authority == "source_scope" {
						allowed = f.target
					}
					if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", allowed, f.actor); err != nil {
						t.Fatal(err)
					}
				}
				gate, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer gate.Rollback(context.Background())
				if authority == "role" {
					_, err = gate.Exec(t.Context(), "SELECT id FROM admin_roles WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1) FOR UPDATE", f.actor)
				} else {
					_, err = gate.Exec(t.Context(), "SELECT id FROM administrators WHERE id=$1 FOR UPDATE", f.actor)
				}
				if err != nil {
					t.Fatal(err)
				}
				before := serviceTransferFacts(t, f)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					done <- request(f.h, "POST", path, body, f.staff, "transfer-authority-wait")
				}()
				waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
				status := 403
				if authority == "role" {
					_, err = gate.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view','customers.view']::text[] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", f.actor)
				} else {
					_, err = gate.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor)
					status = 404
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = gate.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				expect(t, awaitBalanceResponse(t, done), status)
				serviceTransferUnchanged(t, f, before)
			})
		}
	}
}
