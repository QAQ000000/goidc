package httpapi

import (
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

func managementFixture(t *testing.T, manual bool) adminRenewalFixture {
	t.Helper()
	f := newAdminRenewalFixture(t, manual)
	contentRole(t, f.db, f.actor, "services.view", "invoices.view", "services.renew", "services.renew.pay_balance", "services.pricing", "services.renew.override", "services.term.correct", "services.term.compensate")
	return f
}

// Retain the original contract and a partly consumed benefit. A manual price
// must not accidentally apply that benefit twice, spend it, or destroy it.
func managementBenefit(t *testing.T, f adminRenewalFixture) {
	t.Helper()
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE services SET renewal_discount_percent=20,
 renewal_fixed_discount_cents=0,renewal_promo_code='MANAGEMENT-RETAINED',renewal_discount_limit=2,
 renewal_discounts_used=1,version=version+1 WHERE id=$1`, f.sid); err != nil {
		t.Fatal(err)
	}
}

func managementFacts(t *testing.T, f adminRenewalFixture) string {
	t.Helper()
	var out string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_array(to_jsonb(s),
 (SELECT to_jsonb(p) FROM service_renewal_pricing p WHERE p.service_id=s.id),
 (SELECT to_jsonb(m) FROM service_term_management m WHERE m.service_id=s.id),
 u.balance_cents,
 (SELECT coalesce(jsonb_agg(to_jsonb(i) ORDER BY i.id),'[]'::jsonb) FROM invoices i WHERE i.user_id=u.id),
 (SELECT coalesce(jsonb_agg(to_jsonb(l) ORDER BY l.id),'[]'::jsonb) FROM balance_ledger l WHERE l.user_id=u.id),
 (SELECT count(*) FROM mutation_operations),(SELECT count(*) FROM audit_events),
 (SELECT count(*) FROM service_renewal_effects),(SELECT count(*) FROM service_renewal_reversals),
 (SELECT count(*) FROM notification_events),(SELECT count(*) FROM provision_jobs))::text
 FROM services s JOIN users u ON u.id=s.user_id WHERE s.id=$1`, f.sid).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func managementUnchanged(t *testing.T, f adminRenewalFixture, before string) {
	t.Helper()
	if after := managementFacts(t, f); after != before {
		t.Fatal("read, replay or rejected management command changed durable facts", before, after)
	}
}

func managementPricingInput(t *testing.T, f adminRenewalFixture, mode string, amount int64) map[string]any {
	t.Helper()
	in := map[string]any{"expected_version": f.version(t), "mode": mode}
	if mode == "fixed" {
		in["fixed_amount_cents"] = amount
	}
	return in
}

func managementPricingQuote(t *testing.T, f adminRenewalFixture, mode string, amount int64) map[string]any {
	t.Helper()
	w := request(f.h, "POST", adminRenewalPath(f.sid)+"/renewal-pricing/preview", managementPricingInput(t, f, mode, amount), f.staff, "")
	expect(t, w, 200)
	q := data(t, w)
	if q["quote_hash"] == nil || q["quote_hash"] == "" || q["service_id"] != float64(f.sid) || q["mode"] != mode {
		t.Fatal("pricing preview omitted the service, mode or confirmation hash", w.Body.String())
	}
	return q
}

func managementPricingBody(q map[string]any) map[string]any {
	in := map[string]any{"expected_version": q["service_version"], "mode": q["mode"], "quote_hash": q["quote_hash"], "reason": "客户工单确认调整续费价格"}
	if q["mode"] == "fixed" {
		in["fixed_amount_cents"] = q["fixed_amount_cents"]
	}
	return in
}

func managementSetPricing(t *testing.T, f adminRenewalFixture, mode string, amount int64, key string) map[string]any {
	t.Helper()
	q := managementPricingQuote(t, f, mode, amount)
	w := request(f.h, "PUT", adminRenewalPath(f.sid)+"/renewal-pricing", managementPricingBody(q), f.staff, key)
	expect(t, w, 200)
	return data(t, w)
}

func managementFixedQuote(t *testing.T, f adminRenewalFixture, amount int64) map[string]any {
	t.Helper()
	w := request(f.h, "POST", adminRenewalPath(f.sid)+"/fixed-renewal-quotes", map[string]any{"expected_version": f.version(t), "amount_cents": amount}, f.staff, "")
	expect(t, w, 200)
	q := data(t, w)
	if q["amount_cents"] != float64(amount) || q["quote_hash"] == nil || q["quote_hash"] == "" {
		t.Fatal("one-off quotation did not preserve the exact confirmed amount", w.Body.String())
	}
	return q
}

func managementFixedBody(q map[string]any) map[string]any {
	return map[string]any{"expected_version": q["service_version"], "amount_cents": q["amount_cents"], "quote_hash": q["quote_hash"], "reason": "客户工单确认本次固定续费应付"}
}

func managementTermInput(t *testing.T, f adminRenewalFixture, kind string, due time.Time) map[string]any {
	t.Helper()
	return map[string]any{"expected_version": f.version(t), "kind": kind, "next_due_at": due.UTC().Format(time.RFC3339Nano), "reason": "客户工单确认期限核对结果"}
}

func managementTermQuote(t *testing.T, f adminRenewalFixture, kind string, due time.Time) map[string]any {
	t.Helper()
	w := request(f.h, "POST", adminRenewalPath(f.sid)+"/term-adjustment-quotes", managementTermInput(t, f, kind, due), f.staff, "")
	expect(t, w, 200)
	q := data(t, w)
	if q["quote_hash"] == nil || q["quote_hash"] == "" {
		t.Fatal("term preview omitted its confirmation hash", w.Body.String())
	}
	return q
}

func managementTermBody(t *testing.T, f adminRenewalFixture, kind string, due time.Time, q map[string]any) map[string]any {
	t.Helper()
	in := managementTermInput(t, f, kind, due)
	in["quote_hash"] = q["quote_hash"]
	return in
}

func managementMutation(t *testing.T, f adminRenewalFixture, action string) (method, path string, body map[string]any, success int) {
	t.Helper()
	switch action {
	case "pricing":
		q := managementPricingQuote(t, f, "fixed", 700)
		return "PUT", adminRenewalPath(f.sid) + "/renewal-pricing", managementPricingBody(q), 200
	case "fixed":
		q := managementFixedQuote(t, f, 700)
		return "POST", adminRenewalPath(f.sid) + "/fixed-renewals", managementFixedBody(q), 201
	case "term":
		due := f.snapshot(t).due.Add(48 * time.Hour)
		q := managementTermQuote(t, f, "compensation", due)
		return "POST", adminRenewalPath(f.sid) + "/term-adjustments", managementTermBody(t, f, "compensation", due, q), 200
	default:
		t.Fatal("invalid test action", action)
		return
	}
}

func managementCredentialRequest(f adminRenewalFixture, method, path string, body map[string]any, key, secret string) *httptest.ResponseRecorder {
	if secret != "" {
		return requestFrom(f.h, method, path, body, "192.0.2.1:1234", "", map[string]string{"Authorization": "Bearer " + secret, "Idempotency-Key": key})
	}
	return request(f.h, method, path, body, f.staff, key)
}

func TestAdminServiceManagementPricingModesAndRetainedBenefits(t *testing.T) {
	f := managementFixture(t, false)
	managementBenefit(t, f)
	before := f.snapshot(t)
	q := managementPricingQuote(t, f, "fixed", 700)
	if q["contract_base_cents"] != float64(2500) || q["contract_discount_cents"] != float64(500) || q["contract_amount_cents"] != float64(2000) || q["amount_cents"] != float64(700) {
		t.Fatal("preview lost the retained contract or discounted the fixed price", q)
	}
	f.unchanged(t, before)
	body := managementPricingBody(q)
	w := request(f.h, "PUT", adminRenewalPath(f.sid)+"/renewal-pricing", body, f.staff, "management-long-fixed")
	expect(t, w, 200)
	var amount, used, contract int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT p.fixed_amount_cents,s.recurring_price_cents,s.renewal_discounts_used
 FROM services s JOIN service_renewal_pricing p ON p.service_id=s.id WHERE s.id=$1`, f.sid).Scan(&amount, &contract, &used); err != nil || amount != 700 || contract != before.contractPrice || used != 1 {
		t.Fatal("fixed mode overwrote the original contract or consumed a benefit", amount, contract, used, err)
	}
	invoice := f.createRenewal(t, "management-long-fixed-invoice")
	if invoice["amount_cents"] != float64(700) {
		t.Fatal("ordinary renewal ignored fixed mode or discounted it twice", invoice)
	}
	expect(t, request(f.h, "POST", renewalPayPath(f.sid, invoice), renewalPayBody(invoice), f.staff, "management-long-fixed-pay"), 201)
	if after := f.snapshot(t); after.discountsUsed != 1 || after.contractPrice != before.contractPrice || after.balance != before.balance-700 {
		t.Fatal("fixed renewal consumed the retained benefit", before, after)
	}
	managementSetPricing(t, f, "fixed", 0, "management-long-zero")
	zeroVersion := f.version(t)
	managementSetPricing(t, f, "fixed", 0, "management-long-zero-noop")
	if f.version(t) != zeroVersion {
		t.Fatal("saving the same fixed amount changed the service version")
	}
	invoice = f.createRenewal(t, "management-long-zero-invoice")
	if invoice["status"] != "unpaid" || invoice["amount_cents"] != float64(0) {
		t.Fatal("long-term zero amount did not create an explicit unpaid invoice", invoice)
	}
	zeroCreated := f.snapshot(t)
	expect(t, request(f.h, "POST", renewalPayPath(f.sid, invoice), renewalPayBody(invoice), f.staff, "management-long-zero-pay"), 201)
	if after := f.snapshot(t); after.discountsUsed != 1 || after.contractPrice != before.contractPrice || after.balance != before.balance-700 || !after.due.Equal(zeroCreated.due.AddDate(0, 1, 0)) || after.effects != zeroCreated.effects+1 {
		t.Fatal("long-term zero settlement spent money or consumed a retained benefit", zeroCreated, after)
	}
	managementSetPricing(t, f, "contract", 0, "management-restore-contract")
	var fixedRows int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM service_renewal_pricing WHERE service_id=$1", f.sid).Scan(&fixedRows); err != nil || fixedRows != 0 {
		t.Fatal("contract restoration kept a fixed-price override", fixedRows, err)
	}
	quote := f.renewalQuote(t)
	if quote["amount_cents"] != float64(2000) || quote["discount_cents"] != float64(500) {
		t.Fatal("restoration did not recover the unspent original benefit", quote)
	}
	invoice = f.createRenewal(t, "management-restored-invoice")
	expect(t, request(f.h, "POST", renewalPayPath(f.sid, invoice), renewalPayBody(invoice), f.staff, "management-restored-pay"), 201)
	if after := f.snapshot(t); after.discountsUsed != 2 || after.contractPrice != before.contractPrice || after.balance != before.balance-2700 {
		t.Fatal("restored contract failed to consume exactly its remaining benefit", before, after)
	}
}

func TestAdminServiceManagementFixedRenewalSettlementRefundAndZero(t *testing.T) {
	for _, manual := range []bool{false, true} {
		for _, amount := range []int64{777, 0} {
			t.Run(fmt.Sprintf("manual=%t/amount=%d", manual, amount), func(t *testing.T) {
				f := managementFixture(t, manual)
				managementBenefit(t, f)
				before := f.snapshot(t)
				q := managementFixedQuote(t, f, amount)
				w := request(f.h, "POST", adminRenewalPath(f.sid)+"/fixed-renewals", managementFixedBody(q), f.staff, "management-single-fixed")
				expect(t, w, 201)
				invoice := data(t, w)
				if invoice["status"] != "unpaid" || invoice["amount_cents"] != float64(amount) {
					t.Fatal("single fixed renewal must create an exact unpaid invoice", invoice)
				}
				created := f.snapshot(t)
				if created.balance != before.balance || !created.due.Equal(before.due) || created.contractPrice != before.contractPrice || created.discountsUsed != before.discountsUsed || created.effects != before.effects || created.invoices != before.invoices+1 {
					t.Fatal("fixed creation spent money, changed terms or extended entitlement", before, created)
				}
				expect(t, request(f.h, "POST", renewalPayPath(f.sid, invoice), renewalPayBody(invoice), f.staff, "management-single-fixed-pay"), 201)
				paid := f.snapshot(t)
				if paid.balance != before.balance-amount || paid.contractPrice != before.contractPrice || paid.discountsUsed != before.discountsUsed || !paid.due.Equal(before.due.AddDate(0, 1, 0)) || paid.effects != before.effects+1 {
					t.Fatal("single fixed settlement duplicated discounts or changed future price", before, paid)
				}
				refundPath := fmt.Sprintf("/api/v1/admin/invoices/%d/refund", int64(invoice["invoice_id"].(float64)))
				refund := request(f.h, "POST", refundPath, map[string]any{}, f.root, "management-single-fixed-refund")
				expect(t, refund, 201)
				if data(t, refund)["amount_cents"] != float64(amount) {
					t.Fatal("fixed refund returned contract gross instead of actual paid amount", refund.Body.String())
				}
				refunded := f.snapshot(t)
				if refunded.balance != before.balance || refunded.contractPrice != before.contractPrice || refunded.discountsUsed != before.discountsUsed || !refunded.due.Equal(before.due) {
					t.Fatal("refund invented a discount restoration or failed the exact due inverse", before, refunded)
				}
				facts := managementFacts(t, f)
				expect(t, request(f.h, "POST", refundPath, map[string]any{}, f.root, "management-single-fixed-refund"), 200)
				managementUnchanged(t, f, facts)
				if q := f.renewalQuote(t); q["amount_cents"] != float64(2000) {
					t.Fatal("one-off price leaked into future contract renewal", q)
				}
			})
		}
	}
}

func TestAdminServiceManagementReadOnlyPreviewVersionsAndHashes(t *testing.T) {
	f := managementFixture(t, false)
	before := managementFacts(t, f)
	pricing := managementPricingQuote(t, f, "fixed", 700)
	fixed := managementFixedQuote(t, f, 700)
	due := f.snapshot(t).due.Add(24 * time.Hour)
	term := managementTermQuote(t, f, "compensation", due)
	expect(t, request(f.h, "GET", adminRenewalPath(f.sid)+"/management", nil, f.staff, ""), 200)
	managementUnchanged(t, f, before)
	commands := []struct {
		method, suffix string
		body           map[string]any
	}{
		{"PUT", "/renewal-pricing", managementPricingBody(pricing)},
		{"POST", "/fixed-renewals", managementFixedBody(fixed)},
		{"POST", "/term-adjustments", managementTermBody(t, f, "compensation", due, term)},
	}
	for _, command := range commands {
		original := command.body["quote_hash"]
		command.body["quote_hash"] = strings.Repeat("0", 64)
		expect(t, request(f.h, command.method, adminRenewalPath(f.sid)+command.suffix, command.body, f.staff, "management-tampered-hash"), 409)
		command.body["quote_hash"] = original
	}
	// Even an authentic confirmation from another operation cannot authorize
	// this operation when the service, version and amount happen to match.
	commands[0].body["quote_hash"] = fixed["quote_hash"]
	commands[1].body["quote_hash"] = pricing["quote_hash"]
	for _, command := range commands[:2] {
		expect(t, request(f.h, command.method, adminRenewalPath(f.sid)+command.suffix, command.body, f.staff, "management-cross-domain-hash"), 409)
	}
	commands[0].body["quote_hash"] = pricing["quote_hash"]
	commands[1].body["quote_hash"] = fixed["quote_hash"]
	managementUnchanged(t, f, before)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET version=version+1 WHERE id=$1", f.sid); err != nil {
		t.Fatal(err)
	}
	before = managementFacts(t, f)
	for _, command := range commands {
		expect(t, request(f.h, command.method, adminRenewalPath(f.sid)+command.suffix, command.body, f.staff, "management-stale-version"), 409)
	}
	managementUnchanged(t, f, before)
}

func TestAdminServiceManagementStrictInputsPermissionsAndScope(t *testing.T) {
	f := managementFixture(t, false)
	for _, action := range []string{"pricing", "fixed", "term"} {
		method, path, body, _ := managementMutation(t, f, action)
		before := managementFacts(t, f)
		for _, cookie := range []string{"", f.customer, f.other} {
			expect(t, request(f.h, method, path, body, cookie, "management-wrong-audience"), 401)
		}
		expect(t, request(f.h, method, path, body, f.staff, ""), 400)
		for _, field := range []string{"expected_version", "quote_hash", "reason"} {
			value := body[field]
			delete(body, field)
			expect(t, request(f.h, method, path, body, f.staff, "management-invalid-input"), 400)
			body[field] = value
		}
		body["user_id"] = f.uid
		expect(t, request(f.h, method, path, body, f.staff, "management-unknown-field"), 400)
		delete(body, "user_id")
		contentRole(t, f.db, f.actor, "services.view", "services.manage", "invoices.view", "services.renew", "services.renew.pay_balance")
		expect(t, request(f.h, method, path, body, f.staff, "management-no-specific-grant"), 403)
		contentRole(t, f.db, f.actor, "services.view", "invoices.view", "services.renew", "services.renew.pay_balance", "services.pricing", "services.renew.override", "services.term.correct", "services.term.compensate")
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor); err != nil {
			t.Fatal(err)
		}
		expect(t, request(f.h, method, path, body, f.staff, "management-out-of-scope"), 404)
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='all' WHERE id=$1", f.actor); err != nil {
			t.Fatal(err)
		}
		managementUnchanged(t, f, before)
	}
	before := managementFacts(t, f)
	for _, body := range []map[string]any{
		{"expected_version": f.version(t), "mode": "fixed"},
		{"expected_version": f.version(t), "mode": "fixed", "fixed_amount_cents": nil},
		{"expected_version": f.version(t), "mode": "fixed", "fixed_amount_cents": -1},
		{"expected_version": f.version(t), "mode": "fixed", "fixed_amount_cents": "700"},
		{"expected_version": f.version(t), "mode": "unknown"},
	} {
		expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/renewal-pricing/preview", body, f.staff, ""), 400)
	}
	for _, body := range []map[string]any{
		{"expected_version": f.version(t)}, {"expected_version": f.version(t), "amount_cents": nil},
		{"expected_version": f.version(t), "amount_cents": -1}, {"expected_version": f.version(t), "amount_cents": "0"},
	} {
		expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/fixed-renewal-quotes", body, f.staff, ""), 400)
	}
	managementUnchanged(t, f, before)
}

func TestAdminServiceManagementConcurrentReplayAndUnknownCommit(t *testing.T) {
	for _, action := range []string{"pricing", "fixed", "term"} {
		t.Run(action, func(t *testing.T) {
			f := managementFixture(t, false)
			method, path, body, success := managementMutation(t, f, action)
			start, done := make(chan struct{}), make(chan *httptest.ResponseRecorder, 2)
			for range 2 {
				go func() { <-start; done <- request(f.h, method, path, body, f.staff, "management-concurrent") }()
			}
			close(start)
			for range 2 {
				w := awaitBalanceResponse(t, done)
				if w.Code != success && w.Code != 200 {
					t.Fatal("same original intent did not serialize to one committed result", w.Code, w.Body.String())
				}
			}
			before := managementFacts(t, f)
			expect(t, request(f.h, method, path, body, f.staff, "management-concurrent"), 200)
			managementUnchanged(t, f, before)
		})
		t.Run(action+"/lost_commit_ack", func(t *testing.T) {
			f := managementFixture(t, false)
			method, path, body, _ := managementMutation(t, f, action)
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
			h := renewalBatchHTTPWithPool(f.db, pool)
			w := request(h, method, path, body, f.staff, "management-lost-commit")
			if commits.Load() < 1 || (w.Code != 200 && w.Code != 500 && w.Code != 503) {
				t.Fatal("fixture did not lose a real PostgreSQL COMMIT acknowledgement", commits.Load(), w.Code, w.Body.String())
			}
			before := managementFacts(t, f)
			expect(t, request(f.h, method, path, body, f.staff, "management-lost-commit"), 200)
			managementUnchanged(t, f, before)
		})
	}
}

func TestAdminServiceManagementAuditAndReceiptRollback(t *testing.T) {
	for _, action := range []string{"pricing", "fixed", "term"} {
		t.Run(action, func(t *testing.T) {
			f := managementFixture(t, false)
			method, path, body, success := managementMutation(t, f, action)
			before := managementFacts(t, f)
			for _, table := range []string{"audit_events", "mutation_operations"} {
				if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_management_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated management durability failure'; END $$;
 CREATE TRIGGER reject_management_write BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_management_write()`); err != nil {
					t.Fatal(err)
				}
				expect(t, request(f.h, method, path, body, f.staff, "management-rollback"), 500)
				managementUnchanged(t, f, before)
				if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER reject_management_write ON "+table+"; DROP FUNCTION reject_management_write()"); err != nil {
					t.Fatal(err)
				}
			}
			expect(t, request(f.h, method, path, body, f.staff, "management-rollback"), success)
		})
	}
}

func TestAdminServiceManagementOriginalCredentialRevocationAndExpiry(t *testing.T) {
	for _, action := range []string{"pricing", "fixed", "term"} {
		for _, credential := range []string{"session", "api_key"} {
			for _, natural := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/natural=%t", action, credential, natural), func(t *testing.T) {
					f := managementFixture(t, false)
					method, path, body, _ := managementMutation(t, f, action)
					secret, table, column, hash := adminRenewalTestCredential(t, f, credential)
					gate, err := f.db.Pool.Begin(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					defer gate.Rollback(t.Context())
					var blocker uint32
					if err = gate.QueryRow(t.Context(), "SELECT pg_backend_pid() FROM services WHERE id=$1 FOR UPDATE", f.sid).Scan(&blocker); err != nil {
						t.Fatal(err)
					}
					if natural {
						if _, err = f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()+interval '1200 milliseconds' WHERE "+column+"=$1", hash); err != nil {
							t.Fatal(err)
						}
					}
					before := managementFacts(t, f)
					done := make(chan *httptest.ResponseRecorder, 1)
					go func() {
						done <- managementCredentialRequest(f, method, path, body, "management-late-credential", secret)
					}()
					waitUploadBlockedBy(t, f.db, blocker)
					if natural {
						waitAdminRenewalCredentialExpiry(t, f.db, table, column, hash)
					} else {
						if _, err = f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()-interval '1 second' WHERE "+column+"=$1", hash); err != nil {
							t.Fatal(err)
						}
					}
					if err = gate.Commit(t.Context()); err != nil {
						t.Fatal(err)
					}
					expect(t, awaitBalanceResponse(t, done), 401)
					managementUnchanged(t, f, before)
				})
			}
		}
	}
}

func TestAdminServiceManagementTermAdjustmentFencesAndCompensation(t *testing.T) {
	t.Run("explicit_compensation_then_correction", func(t *testing.T) {
		f := managementFixture(t, true)
		managementBenefit(t, f)
		before := f.snapshot(t)
		due := before.due.Add(48 * time.Hour)
		q := managementTermQuote(t, f, "compensation", due)
		body := managementTermBody(t, f, "compensation", due, q)
		expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/term-adjustments", body, f.staff, "management-term-compensation"), 200)
		after := f.snapshot(t)
		if !after.due.Equal(due) || after.balance != before.balance || after.contractPrice != before.contractPrice || after.discountsUsed != before.discountsUsed || after.invoices != before.invoices || after.ledger != before.ledger || after.effects != before.effects || after.jobs != before.jobs || after.notifications != before.notifications {
			t.Fatal("free compensation generated money, consumed a benefit or dispatched lifecycle work", before, after)
		}
		facts := managementFacts(t, f)
		expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/term-adjustments", body, f.staff, "management-term-compensation"), 200)
		managementUnchanged(t, f, facts)
		for _, invalidDue := range []time.Time{due, due.Add(-time.Hour)} {
			expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/term-adjustment-quotes", managementTermInput(t, f, "compensation", invalidDue), f.staff, ""), 409)
		}
		corrected := before.due.Add(12 * time.Hour)
		q = managementTermQuote(t, f, "correction", corrected)
		expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/term-adjustments", managementTermBody(t, f, "correction", corrected, q), f.staff, "management-term-correction"), 200)
		if after = f.snapshot(t); !after.due.Equal(corrected) || after.balance != before.balance || after.discountsUsed != before.discountsUsed || after.contractPrice != before.contractPrice {
			t.Fatal("explicit correction did not preserve all unrelated contract facts", before, after)
		}
	})
	for _, scenario := range []string{"unpaid_renewal", "configuration_change", "lifecycle_pending", "terminated"} {
		t.Run(scenario, func(t *testing.T) {
			f := managementFixture(t, false)
			switch scenario {
			case "unpaid_renewal":
				f.createRenewal(t, "management-fence-original-renewal")
			case "configuration_change":
				f.submit(t, f.quote(t, f.large), "management-fence-configuration")
			case "lifecycle_pending":
				expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/actions", map[string]any{"action": "suspend"}, f.root, "management-fence-action"), 202)
			case "terminated":
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET status='terminated',version=version+1 WHERE id=$1", f.sid); err != nil {
					t.Fatal(err)
				}
			}
			before := managementFacts(t, f)
			in := managementTermInput(t, f, "compensation", f.snapshot(t).due.Add(24*time.Hour))
			expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/term-adjustment-quotes", in, f.staff, ""), 409)
			if scenario == "unpaid_renewal" || scenario == "configuration_change" {
				expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/renewal-pricing/preview", managementPricingInput(t, f, "fixed", 700), f.staff, ""), 409)
			}
			managementUnchanged(t, f, before)
		})
	}
}

func TestAdminServiceManagementSupplierSyncPreservesCompensation(t *testing.T) {
	f, service := activeSupplierRenewalFixture(t)
	var version int64
	var original time.Time
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT version,next_due_at FROM services WHERE id=$1", service).Scan(&version, &original); err != nil {
		t.Fatal(err)
	}
	due := original.Add(48 * time.Hour)
	path := adminRenewalPath(service)
	in := map[string]any{"expected_version": version, "kind": "compensation", "next_due_at": due.UTC().Format(time.RFC3339Nano), "reason": "客户工单确认本地补偿期限"}
	w := request(f.h, "POST", path+"/term-adjustment-quotes", in, f.root, "")
	expect(t, w, 200)
	in["quote_hash"] = data(t, w)["quote_hash"]
	expect(t, request(f.h, "POST", path+"/term-adjustments", in, f.root, "management-supplier-compensation"), 200)
	checkDue := func(want time.Time) {
		t.Helper()
		var got time.Time
		var status string
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at,status FROM services WHERE id=$1", service).Scan(&got, &status); err != nil || !got.Equal(want) || status != "active" {
			t.Fatal("supplier refresh replaced customer entitlement or changed lifecycle", got, want, status, err)
		}
	}
	checkDue(due)
	expect(t, request(f.h, "POST", path+"/actions", map[string]any{"action": "sync"}, f.root, "management-supplier-sync"), 202)
	job := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	checkDue(due)
	w = request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", service), map[string]any{}, f.customer, "management-supplier-renewal")
	expect(t, w, 201)
	invoice := int64(data(t, w)["invoice_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "management-supplier-payment"), 201)
	checkDue(due)
	job = f.claim(t)
	if job.Action != "renew" {
		t.Fatal("supplier payment failed to queue its original renewal", job)
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	checkDue(due.AddDate(0, 1, 0))
	var remoteDue time.Time
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT supplier_next_due_at FROM service_term_management WHERE service_id=$1", service).Scan(&remoteDue); err != nil || !remoteDue.Equal(time.Unix(1802678400, 0)) {
		t.Fatal("managed service lost its separately observed real upstream expiry", remoteDue, err)
	}
	expect(t, request(f.h, "POST", path+"/actions", map[string]any{"action": "sync"}, f.root, "management-supplier-sync-after-renew"), 202)
	job = f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	checkDue(due.AddDate(0, 1, 0))
}

func managementSupplierAdjustTerm(t *testing.T, f *supplierFixture, service int64, kind string, due time.Time, key string) {
	t.Helper()
	path := adminRenewalPath(service)
	w := request(f.h, "GET", path+"/management", nil, f.root, "")
	expect(t, w, 200)
	in := map[string]any{"expected_version": data(t, w)["service_version"], "kind": kind,
		"next_due_at": due.UTC().Format(time.RFC3339Nano), "reason": "客户工单确认独立管理期限"}
	w = request(f.h, "POST", path+"/term-adjustment-quotes", in, f.root, "")
	expect(t, w, 200)
	in["quote_hash"] = data(t, w)["quote_hash"]
	expect(t, request(f.h, "POST", path+"/term-adjustments", in, f.root, key), 200)
}

func TestAdminServiceManagementManagedAutoRenewalUsesCustomerDeadline(t *testing.T) {
	f, service := activeSupplierRenewalFixture(t)
	remoteBefore := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)
	remoteAfter := supplierRenewalClock(t, f, remoteBefore)
	customerBefore := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	managementSupplierAdjustTerm(t, f, service, "correction", customerBefore, "management-auto-customer-deadline")
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/services/%d/auto-renew", service), map[string]bool{"enabled": true}, f.customer, ""), 200)
	before := supplierExpiryMoneySnapshot(t, f.db, f.uid)
	x := &supplierRenewalStateFixture{f: f, sid: service}
	job := x.collect(t)
	x.check(t, "active", "", customerBefore, 0)
	paid, unpaid, total := autoRenewInvoiceStats(t, f.db, service)
	var effects int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM service_managed_renewal_effects WHERE service_id=$1", service).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	collected := supplierExpiryMoneySnapshot(t, f.db, f.uid)
	if paid != 1 || unpaid != 0 || total != 1 || collected.Balance != before.Balance-2500 || collected.Invoices != before.Invoices+1 || collected.Ledger != before.Ledger+1 || effects != 0 || f.renews.Load() != 0 || f.renewalPays.Load() != 0 {
		t.Fatal("managed automatic collection skipped the customer deadline or claimed unconfirmed entitlement", paid, unpaid, total, before, collected, effects)
	}
	supplierRenewalEvents(t, f, service, 0)
	if n, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 0 {
		t.Fatal("unconfirmed automatic renewal was collected again", n, err)
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	if state, _, lease := f.state(t, job); state != "succeeded" || lease != nil {
		t.Fatal("managed supplier renewal was not confirmed by the real worker path", state, lease)
	}
	customerAfter := customerBefore.AddDate(0, 1, 0)
	x.check(t, "active", "", customerAfter, 0)
	var observed, effectBefore, effectAfter time.Time
	var mode string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT t.mode,t.supplier_next_due_at,e.before_due,e.after_due,
 (SELECT count(*) FROM service_managed_renewal_effects WHERE service_id=$1)
 FROM service_term_management t JOIN service_managed_renewal_effects e ON e.service_id=t.service_id
 WHERE t.service_id=$1 AND e.job_id=$2`, service, job.ID).Scan(&mode, &observed, &effectBefore, &effectAfter, &effects); err != nil || mode != "managed" || !observed.Equal(remoteAfter) || !effectBefore.Equal(customerBefore) || !effectAfter.Equal(customerAfter) || effects != 1 {
		t.Fatal("confirmed renewal replaced the customer contract with the remote deadline or lost its unique effect", mode, observed, effectBefore, effectAfter, effects, err)
	}
	supplierRenewalEvents(t, f, service, 1)
	x.sync(t, "management-auto-confirmed-refresh")
	x.check(t, "active", "", customerAfter, 0)
	if n, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 0 {
		t.Fatal("confirmed managed renewal was collected twice", n, err)
	}
	if f.renews.Load() != 1 || f.renewalPays.Load() != 1 || f.actions.Load() != 0 || supplierExpiryMoneySnapshot(t, f.db, f.uid) != collected {
		t.Fatal("managed confirmation or refresh repeated local collection or upstream purchase")
	}
}

func TestAdminServiceManagementTermCorrectionInvalidatesDeferredRecovery(t *testing.T) {
	f, service := activeSupplierRenewalFixture(t)
	original := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	remoteAfter := supplierRenewalClock(t, f, original)
	managementSupplierAdjustTerm(t, f, service, "correction", original, "management-expired-customer-contract")
	x := &supplierRenewalStateFixture{f: f, sid: service}
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
		t.Fatal("managed expiry did not queue its real system suspension", n, err)
	}
	suspension := supplierExpiryJob(t, f.db, service)
	supplierExpirySource(t, f.db, suspension, original, true)
	if state, current, lease := f.state(t, suspension); state != "waiting_manual" || current != "active" || lease != nil {
		t.Fatal("unconfirmed expiry should not claim a successful suspension", state, current, lease)
	}
	f.remoteStatus.Store("Suspended")
	f.remoteSuspendReason.Store("due")
	input := store.SupplierVerificationInput{JobID: suspension.ID, ExpectedAttempts: suspension.Attempts, UpstreamID: 42,
		Outcome: "delivered", Note: "只读核验客户期限到期后人工在上游暂停"}
	expect(t, request(f.h, "POST", adminRenewalPath(service)+"/supplier-lifecycle/verify", input, f.root, ""), 200)
	x.check(t, "suspended", "due", original, 0)
	supplierExpiryMarker(t, f.db, service, suspension.ID)
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/services/%d/auto-renew", service), map[string]bool{"enabled": true}, f.customer, ""), 200)
	balance := autoRenewBalance(t, f.db, f.uid)
	renewal := x.collect(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *renewal); err != nil {
		t.Fatal(err)
	}
	if state, _, lease := f.state(t, renewal); state != "succeeded" || lease != nil {
		t.Fatal("real renewal did not establish confirmed deferred recovery", state, lease)
	}
	var marker int64
	var after time.Time
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT coalesce((s.resource_info->>'supplier_renewal_recovery_job_id')::bigint,0),e.after_due
 FROM services s JOIN service_managed_renewal_effects e ON e.service_id=s.id
 WHERE s.id=$1 AND e.job_id=$2`, service, renewal.ID).Scan(&marker, &after); err != nil || marker != renewal.ID || !after.After(time.Now()) {
		t.Fatal("confirmed-but-paused renewal did not create its real deferred marker and entitlement", marker, after, err)
	}
	x.check(t, "suspended", "due", after, 0)
	corrected := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	managementSupplierAdjustTerm(t, f, service, "correction", corrected, "management-invalidates-old-recovery")
	var observed, observedAt time.Time
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT coalesce((s.resource_info->>'supplier_renewal_recovery_job_id')::bigint,0),t.supplier_next_due_at,t.supplier_observed_at
 FROM services s JOIN service_term_management t ON t.service_id=s.id WHERE s.id=$1`, service).Scan(&marker, &observed, &observedAt); err != nil || marker != 0 || !observed.Equal(remoteAfter) {
		t.Fatal("manual correction retained stale recovery authority or discarded the actual remote observation", marker, observed, observedAt, err)
	}
	f.remoteStatus.Store("Active")
	f.remoteSuspendReason.Store("")
	// Historical callbacks omit nextduedate. They cannot erase the last real
	// deadline observation, refresh its timestamp, or consume recovery authority.
	x.callback(t, "Active", "")
	x.check(t, "suspended", "due", corrected, 0)
	var mode string
	var callbackDue, callbackObservedAt time.Time
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT mode,supplier_next_due_at,supplier_observed_at FROM service_term_management WHERE service_id=$1`, service).Scan(&mode, &callbackDue, &callbackObservedAt); err != nil || mode != "managed" || !callbackDue.Equal(observed) || !callbackObservedAt.Equal(observedAt) {
		t.Fatal("callback without a deadline cleared or refreshed a previous confirmed observation", mode, callbackDue, callbackObservedAt, err)
	}
	x.sync(t, "management-old-recovery-fresh-sync")
	x.check(t, "suspended", "due", corrected, 0)
	x.sync(t, "management-old-recovery-repeat-sync")
	x.check(t, "suspended", "due", corrected, 0)
	var effects int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT coalesce((s.resource_info->>'supplier_renewal_recovery_job_id')::bigint,0),t.mode,
 (SELECT count(*) FROM service_managed_renewal_effects WHERE service_id=s.id)
 FROM services s JOIN service_term_management t ON t.service_id=s.id WHERE s.id=$1`, service).Scan(&marker, &mode, &effects); err != nil || marker != 0 || mode != "managed" || effects != 1 {
		t.Fatal("fresh reads revived stale recovery authority or duplicated its immutable entitlement", marker, mode, effects, err)
	}
	supplierRenewalEvents(t, f, service, 1)
	if f.renews.Load() != 1 || f.renewalPays.Load() != 1 || f.actions.Load() != 0 || autoRenewBalance(t, f.db, f.uid) != balance-2500 {
		t.Fatal("term correction or read-only reconciliation repeated money or supplier writes")
	}
}
