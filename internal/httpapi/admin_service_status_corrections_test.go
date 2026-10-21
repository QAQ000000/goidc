package httpapi

import (
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

func statusCorrectionGrants(t *testing.T, f adminRenewalFixture) {
	t.Helper()
	contentRole(t, f.db, f.actor, "services.view", "services.status.correct", "invoices.view", "services.renew", "services.renew.pay_balance")
}

// Source facts are produced by the normal lifecycle, including the original
// worker/manual completion audit. Only the final projection drift is artificial.
func statusCorrectionFixture(t *testing.T, manual bool, target string) (adminRenewalFixture, int64) {
	t.Helper()
	f := managementFixture(t, manual)
	statusCorrectionGrants(t, f)
	job := statusCorrectionLifecycle(t, f, manual, "suspend")
	if target == "active" {
		job = statusCorrectionLifecycle(t, f, manual, "resume")
	}
	drift := "active"
	if target == "active" {
		drift = "suspended"
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET status=$2,version=version+1 WHERE id=$1", f.sid, drift); err != nil {
		t.Fatal(err)
	}
	return f, job
}

func statusCorrectionLifecycle(t *testing.T, f adminRenewalFixture, manual bool, action string) int64 {
	t.Helper()
	w := request(f.h, "POST", adminRenewalPath(f.sid)+"/actions", map[string]any{"action": action}, f.root, "status-source-"+action)
	expect(t, w, 202)
	id := int64(data(t, w)["job_id"].(float64))
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil || job.ID != id {
		t.Fatal("lifecycle fixture did not claim its original task", job, id, err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	if manual {
		expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/delivery", store.ManualCompletion{JobID: job.ID, Note: "已核实实际资源动作完成"}, f.root, ""), 200)
	}
	return id
}

func statusCorrectionInput(t *testing.T, f adminRenewalFixture, job int64, target string) map[string]any {
	t.Helper()
	return map[string]any{"expected_version": f.version(t), "source_job_id": job, "target_status": target,
		"reason": "工单核对确认本地状态投影错误", "evidence": "已核对原生命周期任务与实际资源状态"}
}

func statusCorrectionQuote(t *testing.T, f adminRenewalFixture, job int64, target string) map[string]any {
	t.Helper()
	before := managementFacts(t, f)
	w := request(f.h, "POST", adminRenewalPath(f.sid)+"/status-correction-quotes", statusCorrectionInput(t, f, job, target), f.staff, "")
	expect(t, w, 200)
	q := data(t, w)
	if q["service_id"] != float64(f.sid) || q["customer_id"] != float64(f.uid) || q["source_job_id"] != float64(job) || q["target_status"] != target || q["quote_hash"] == "" || q["quote_hash"] == nil || q["expires_at"] == nil || q["source_completed_at"] == nil {
		t.Fatal("preview omitted its bound service, completion proof or expiry", w.Body.String())
	}
	managementUnchanged(t, f, before)
	in := statusCorrectionInput(t, f, job, target)
	in["quote_hash"] = q["quote_hash"]
	return in
}

// A correction may change the local status/version and its own
// audit/receipt; all business, dispatch, delivery and notification facts persist.
func statusCorrectionPreservedFacts(t *testing.T, f adminRenewalFixture) string {
	t.Helper()
	var facts string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_array(
 to_jsonb(s)-ARRAY['status','version','updated_at'],to_jsonb(u),
 (SELECT coalesce(jsonb_agg(to_jsonb(i) ORDER BY id),'[]') FROM invoices i WHERE user_id=s.user_id),
 (SELECT coalesce(jsonb_agg(to_jsonb(l) ORDER BY id),'[]') FROM balance_ledger l WHERE user_id=s.user_id),
 (SELECT coalesce(jsonb_agg(to_jsonb(j) ORDER BY id),'[]') FROM provision_jobs j WHERE service_id=s.id),
 (SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY id),'[]') FROM notification_events e WHERE user_id=s.user_id),
 (SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY id),'[]') FROM service_renewal_effects e WHERE service_id=s.id),
 (SELECT to_jsonb(p) FROM service_renewal_pricing p WHERE service_id=s.id),
 (SELECT to_jsonb(m) FROM service_term_management m WHERE service_id=s.id))::text
 FROM services s JOIN users u ON u.id=s.user_id WHERE s.id=$1`, f.sid).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	return facts
}

func TestAdminServiceStatusCorrectionProofAndPreservedFacts(t *testing.T) {
	for _, manual := range []bool{false, true} {
		for _, target := range []string{"active", "suspended"} {
			t.Run(fmt.Sprintf("manual=%t/target=%s", manual, target), func(t *testing.T) {
				f, job := statusCorrectionFixture(t, manual, target)
				body := statusCorrectionQuote(t, f, job, target)
				before, version := statusCorrectionPreservedFacts(t, f), f.version(t)
				var audits, receipts int64
				if err := f.db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM audit_events),(SELECT count(*) FROM mutation_operations)").Scan(&audits, &receipts); err != nil {
					t.Fatal(err)
				}
				w := request(f.h, "POST", adminRenewalPath(f.sid)+"/status-corrections", body, f.staff, "status-correction-positive")
				expect(t, w, 200)
				out := data(t, w)
				if out["status"] != target || out["service_version"] != float64(version+1) || out["source_job_id"] != float64(job) || out["replayed"] == true {
					t.Fatal("correction did not preserve its exact source and single CAS transition", w.Body.String())
				}
				if after := statusCorrectionPreservedFacts(t, f); after != before {
					t.Fatal("projection correction changed delivery, money, price, date, dispatch or notifications", before, after)
				}
				var status string
				var newAudits, newReceipts, attributed int64
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM audit_events),
 (SELECT count(*) FROM mutation_operations),(SELECT count(*) FROM audit_events
 WHERE action='service.status.corrected' AND entity_type='service' AND entity_id=$2 AND actor_id=$3
 AND payload->>'source_job_id'=$4) FROM services WHERE id=$1`, f.sid, fmt.Sprint(f.sid), f.actor, fmt.Sprint(job)).Scan(&status, &newAudits, &newReceipts, &attributed); err != nil || status != target || newAudits != audits+1 || newReceipts != receipts+1 || attributed != 1 {
					t.Fatal("service/audit/unified receipt were not atomically attributed", status, newAudits, newReceipts, attributed, err)
				}
				facts := managementFacts(t, f)
				w = request(f.h, "POST", adminRenewalPath(f.sid)+"/status-corrections", body, f.staff, "status-correction-positive")
				expect(t, w, 200)
				if data(t, w)["replayed"] != true {
					t.Fatal("same original request was not replayed", w.Body.String())
				}
				managementUnchanged(t, f, facts)
			})
		}
	}
	t.Run("one_off_without_due_date", func(t *testing.T) {
		f := managementFixture(t, false)
		statusCorrectionGrants(t, f)
		product := productInput("status-one-off", nil)
		product.BillingCycle, product.ProvisionMode = "once", "simulation"
		pid := managedProductID(t, f.h, f.root, product)
		version, _ := cart(t, f.h, f.customer)
		expect(t, request(f.h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": pid, "quantity": 1, "billing_cycle": "once"}, f.customer, ""), 200)
		cartBody := data(t, request(f.h, "GET", "/api/v1/cart", nil, f.customer, ""))
		items := cartBody["items"].([]any)
		item := int64(items[len(items)-1].(map[string]any)["id"].(float64))
		w := request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(cartBody["version"].(float64)), ItemIDs: []int64{item}}, f.customer, "status-one-off-order")
		expect(t, w, 201)
		order := data(t, w)
		expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(order["invoice_id"].(float64))), map[string]any{}, f.customer, "status-one-off-pay"), 201)
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1", int64(order["order_id"].(float64))).Scan(&f.sid); err != nil {
			t.Fatal(err)
		}
		created, err := f.db.ClaimProvisionJob(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err = f.db.ProcessProvisionJob(t.Context(), *created); err != nil {
			t.Fatal(err)
		}
		statusCorrectionLifecycle(t, f, false, "suspend")
		job := statusCorrectionLifecycle(t, f, false, "resume")
		if _, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET status='suspended',version=version+1 WHERE id=$1", f.sid); err != nil {
			t.Fatal(err)
		}
		body := statusCorrectionQuote(t, f, job, "active")
		before := statusCorrectionPreservedFacts(t, f)
		expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/status-corrections", body, f.staff, "status-one-off-correction"), 200)
		var valid bool
		if err = f.db.Pool.QueryRow(t.Context(), "SELECT billing_cycle='once' AND next_due_at IS NULL AND status='active' FROM services WHERE id=$1", f.sid).Scan(&valid); err != nil || !valid || statusCorrectionPreservedFacts(t, f) != before {
			t.Fatal("one-off correction invented an expiry or changed unrelated facts", valid, err)
		}
	})
}

func TestAdminServiceStatusCorrectionStrictInputsPermissionsAndScope(t *testing.T) {
	f, job := statusCorrectionFixture(t, false, "suspended")
	body := statusCorrectionQuote(t, f, job, "suspended")
	path, preview := adminRenewalPath(f.sid)+"/status-corrections", adminRenewalPath(f.sid)+"/status-correction-quotes"
	before := managementFacts(t, f)
	for _, cookie := range []string{"", f.customer, f.other} {
		expect(t, request(f.h, "POST", path, body, cookie, "status-wrong-audience"), 401)
	}
	expect(t, request(f.h, "POST", path, body, f.staff, ""), 400)
	for _, field := range []string{"expected_version", "source_job_id", "target_status", "reason", "evidence", "quote_hash"} {
		value := body[field]
		delete(body, field)
		expect(t, request(f.h, "POST", path, body, f.staff, "status-missing-field"), 400)
		body[field] = value
	}
	body["user_id"] = f.uid
	expect(t, request(f.h, "POST", path, body, f.staff, "status-unknown-field"), 400)
	delete(body, "user_id")
	for _, field := range []string{"reason", "evidence"} {
		value := body[field]
		body[field] = strings.Repeat("过", 2100)
		expect(t, request(f.h, "POST", path, body, f.staff, "status-overlong-field"), 400)
		body[field] = value
	}
	for _, target := range []string{"terminated", "queued", "anything"} {
		in := statusCorrectionInput(t, f, job, target)
		expect(t, request(f.h, "POST", preview, in, f.staff, ""), 400)
	}
	for _, grants := range [][]string{{"services.view", "services.manage"}, {"services.status.correct"}} {
		contentRole(t, f.db, f.actor, grants...)
		expect(t, request(f.h, "POST", path, body, f.staff, "status-no-specific-grant"), 403)
		expect(t, request(f.h, "POST", preview, statusCorrectionInput(t, f, job, "suspended"), f.staff, ""), 403)
	}
	statusCorrectionGrants(t, f)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path, body, f.staff, "status-out-of-scope"), 404)
	managementUnchanged(t, f, before)
}

func TestAdminServiceStatusCorrectionSourceAndLifecycleFences(t *testing.T) {
	for _, scenario := range []string{"create_source", "missing_source", "missing_audit", "mutated_audit", "mutated_result", "manual_customer_actor", "newer_attempt", "newer_failed_attempt", "supplier", "nokvm", "terminated", "cancelled", "same_status"} {
		t.Run(scenario, func(t *testing.T) {
			f, job := statusCorrectionFixture(t, scenario == "manual_customer_actor", "suspended")
			body := statusCorrectionQuote(t, f, job, "suspended")
			var err error
			switch scenario {
			case "create_source":
				err = f.db.Pool.QueryRow(t.Context(), "SELECT id FROM provision_jobs WHERE service_id=$1 AND action='create'", f.sid).Scan(&job)
			case "missing_source":
				job += 1000000
			case "missing_audit":
				_, err = f.db.Pool.Exec(t.Context(), "DELETE FROM audit_events WHERE entity_type='service' AND entity_id=$1 AND action='service.simulation.completed'", fmt.Sprint(f.sid))
			case "mutated_audit":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE audit_events SET payload=payload||'{\"fixture_changed\":true}'::jsonb WHERE entity_type='service' AND entity_id=$1 AND action='service.simulation.completed' AND payload->>'job_id'=$2", fmt.Sprint(f.sid), fmt.Sprint(job))
			case "mutated_result":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET result=result||'{\"fixture_changed\":true}'::jsonb WHERE id=$1", job)
			case "manual_customer_actor":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE audit_events SET actor_id=$3 WHERE entity_type='service' AND entity_id=$1 AND action='service.manual.completed' AND payload->>'job_id'=$2", fmt.Sprint(f.sid), fmt.Sprint(job), f.uid)
			case "newer_attempt":
				expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/actions", map[string]any{"action": "suspend"}, f.root, "status-newer-attempt"), 202)
			case "newer_failed_attempt":
				expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/actions", map[string]any{"action": "suspend"}, f.root, "status-newer-attempt"), 202)
				// Failure is produced by the real worker after the fixture moves
				// the projection to a state where this later action is invalid.
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET status='suspended' WHERE id=$1", f.sid)
				if err == nil {
					pending, claimErr := f.db.ClaimProvisionJob(t.Context())
					if claimErr != nil {
						t.Fatal(claimErr)
					}
					if processErr := f.db.ProcessProvisionJob(t.Context(), *pending); processErr != nil {
						t.Fatal(processErr)
					}
					var state string
					if err = f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", pending.ID).Scan(&state); err == nil && state != "failed" {
						t.Fatal("fixture did not produce a genuine later failed lifecycle", state)
					}
					if err == nil {
						_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET status='active' WHERE id=$1", f.sid)
					}
				}
			case "supplier", "nokvm":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode=$2 WHERE id=$1", f.sid, scenario)
			case "terminated", "cancelled":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET status=$2 WHERE id=$1", f.sid, scenario)
			case "same_status":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET status='suspended' WHERE id=$1", f.sid)
			}
			if err != nil {
				t.Fatal(err)
			}
			body["source_job_id"] = job
			before := managementFacts(t, f)
			if scenario != "mutated_audit" && scenario != "mutated_result" {
				expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/status-correction-quotes", statusCorrectionInput(t, f, job, "suspended"), f.staff, ""), 409)
			}
			// Applying a previously valid preview must re-read all source facts.
			expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/status-corrections", body, f.staff, "status-source-invalidated"), 409)
			managementUnchanged(t, f, before)
		})
	}
	t.Run("later_read_only_sync_preserves_source", func(t *testing.T) {
		f, job := statusCorrectionFixture(t, false, "suspended")
		statusCorrectionLifecycle(t, f, false, "sync")
		body := statusCorrectionQuote(t, f, job, "suspended")
		expect(t, request(f.h, "POST", adminRenewalPath(f.sid)+"/status-corrections", body, f.staff, "status-after-sync"), 200)
	})
}

func TestAdminServiceStatusCorrectionBusinessHolds(t *testing.T) {
	for _, scenario := range []string{"unpaid_renewal", "configuration_change", "disabled_customer", "released_inventory", "system_billing", "system_expiry", "recovery_job", "expired_activation"} {
		t.Run(scenario, func(t *testing.T) {
			target := "suspended"
			if scenario == "expired_activation" {
				target = "active"
			}
			f, job := statusCorrectionFixture(t, false, target)
			body := statusCorrectionQuote(t, f, job, target)
			var err error
			switch scenario {
			case "unpaid_renewal":
				f.createRenewal(t, "status-unpaid-renewal")
			case "configuration_change":
				f.submit(t, f.quote(t, f.large), "status-configuration-pending")
			case "disabled_customer":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", f.uid)
			case "released_inventory":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET inventory_released=true WHERE id=$1", f.sid)
			case "system_billing", "system_expiry", "recovery_job":
				key := map[string]string{"system_billing": "system_billing_suspension_job_id", "system_expiry": "system_expiry_suspension_job_id", "recovery_job": "supplier_renewal_recovery_job_id"}[scenario]
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET resource_info=resource_info||jsonb_build_object($2::text,$3::bigint) WHERE id=$1", f.sid, key, job)
			case "expired_activation":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=clock_timestamp()-interval '1 day' WHERE id=$1", f.sid)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := managementFacts(t, f)
			w := request(f.h, "POST", adminRenewalPath(f.sid)+"/status-correction-quotes", statusCorrectionInput(t, f, job, target), f.staff, "")
			if w.Code != 409 && !(scenario == "disabled_customer" && w.Code == 404) {
				t.Fatal("business fence accepted a correction", scenario, w.Code, w.Body.String())
			}
			w = request(f.h, "POST", adminRenewalPath(f.sid)+"/status-corrections", body, f.staff, "status-business-hold")
			if w.Code != 409 && !(scenario == "disabled_customer" && w.Code == 404) {
				t.Fatal("apply did not recheck its business fence", scenario, w.Code, w.Body.String())
			}
			managementUnchanged(t, f, before)
		})
	}
}

func TestAdminServiceStatusCorrectionReplayAndQuoteBinding(t *testing.T) {
	t.Run("concurrent_and_intent_binding", func(t *testing.T) {
		f, job := statusCorrectionFixture(t, false, "suspended")
		body := statusCorrectionQuote(t, f, job, "suspended")
		path := adminRenewalPath(f.sid) + "/status-corrections"
		before, version := statusCorrectionPreservedFacts(t, f), f.version(t)
		start, done := make(chan struct{}), make(chan *httptest.ResponseRecorder, 2)
		for range 2 {
			go func() { <-start; done <- request(f.h, "POST", path, body, f.staff, "status-concurrent") }()
		}
		close(start)
		for range 2 {
			expect(t, awaitBalanceResponse(t, done), 200)
		}
		if f.version(t) != version+1 || statusCorrectionPreservedFacts(t, f) != before {
			t.Fatal("concurrent correction changed more than one local projection")
		}
		facts := managementFacts(t, f)
		body["reason"] = "不同的纠错申请原因"
		expect(t, request(f.h, "POST", path, body, f.staff, "status-concurrent"), 409)
		managementUnchanged(t, f, facts)
	})
	t.Run("different_keys_share_one_CAS", func(t *testing.T) {
		f, job := statusCorrectionFixture(t, false, "suspended")
		body := statusCorrectionQuote(t, f, job, "suspended")
		before, version := statusCorrectionPreservedFacts(t, f), f.version(t)
		start, done := make(chan struct{}), make(chan *httptest.ResponseRecorder, 2)
		for _, key := range []string{"status-CAS-first", "status-CAS-second"} {
			go func(key string) {
				<-start
				done <- request(f.h, "POST", adminRenewalPath(f.sid)+"/status-corrections", body, f.staff, key)
			}(key)
		}
		close(start)
		succeeded, conflicted := 0, 0
		for range 2 {
			w := awaitBalanceResponse(t, done)
			switch w.Code {
			case 200:
				succeeded++
			case 409:
				conflicted++
			default:
				t.Fatal("cross-key CAS returned an unexpected result", w.Code, w.Body.String())
			}
		}
		if succeeded != 1 || conflicted != 1 || f.version(t) != version+1 || statusCorrectionPreservedFacts(t, f) != before {
			t.Fatal("different keys did not compete for exactly one CAS transition", succeeded, conflicted)
		}
	})
	t.Run("stale_version_and_changed_evidence", func(t *testing.T) {
		f, job := statusCorrectionFixture(t, false, "suspended")
		body := statusCorrectionQuote(t, f, job, "suspended")
		path := adminRenewalPath(f.sid) + "/status-corrections"
		before := managementFacts(t, f)
		body["evidence"] = "另一个尚未预览的证据内容"
		expect(t, request(f.h, "POST", path, body, f.staff, "status-hash-changed"), 409)
		managementUnchanged(t, f, before)
		body = statusCorrectionQuote(t, f, job, "suspended")
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET version=version+1 WHERE id=$1", f.sid); err != nil {
			t.Fatal(err)
		}
		before = managementFacts(t, f)
		expect(t, request(f.h, "POST", path, body, f.staff, "status-version-changed"), 409)
		managementUnchanged(t, f, before)
	})
	t.Run("lost_commit_ack", func(t *testing.T) {
		f, job := statusCorrectionFixture(t, false, "suspended")
		body := statusCorrectionQuote(t, f, job, "suspended")
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
		path := adminRenewalPath(f.sid) + "/status-corrections"
		w := request(renewalBatchHTTPWithPool(f.db, pool), "POST", path, body, f.staff, "status-lost-commit")
		if commits.Load() < 1 || (w.Code != 200 && w.Code != 500 && w.Code != 503) {
			t.Fatal("fixture did not lose a real PostgreSQL COMMIT acknowledgement", commits.Load(), w.Code, w.Body.String())
		}
		facts := managementFacts(t, f)
		w = request(f.h, "POST", path, body, f.staff, "status-lost-commit")
		expect(t, w, 200)
		if data(t, w)["replayed"] != true {
			t.Fatal("lost acknowledgement did not recover from unified receipt", w.Body.String())
		}
		managementUnchanged(t, f, facts)
	})
}

func TestAdminServiceStatusCorrectionAuditAndReceiptRollback(t *testing.T) {
	f, job := statusCorrectionFixture(t, false, "suspended")
	body := statusCorrectionQuote(t, f, job, "suspended")
	path := adminRenewalPath(f.sid) + "/status-corrections"
	before := managementFacts(t, f)
	for _, table := range []string{"audit_events", "mutation_operations"} {
		if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_status_correction_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated status correction durability failure'; END $$;
 CREATE TRIGGER reject_status_correction_write BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_status_correction_write()`); err != nil {
			t.Fatal(err)
		}
		expect(t, request(f.h, "POST", path, body, f.staff, "status-rollback"), 500)
		managementUnchanged(t, f, before)
		if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER reject_status_correction_write ON "+table+"; DROP FUNCTION reject_status_correction_write()"); err != nil {
			t.Fatal(err)
		}
	}
	expect(t, request(f.h, "POST", path, body, f.staff, "status-rollback"), 200)
}

func TestAdminServiceStatusCorrectionOriginalCredentialRevocationAndExpiry(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		for _, natural := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/natural=%t", credential, natural), func(t *testing.T) {
				f, job := statusCorrectionFixture(t, false, "suspended")
				body := statusCorrectionQuote(t, f, job, "suspended")
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
					done <- managementCredentialRequest(f, "POST", adminRenewalPath(f.sid)+"/status-corrections", body, "status-late-credential", secret)
				}()
				waitUploadBlockedBy(t, f.db, blocker)
				if natural {
					waitAdminRenewalCredentialExpiry(t, f.db, table, column, hash)
				} else if _, err = f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()-interval '1 second' WHERE "+column+"=$1", hash); err != nil {
					t.Fatal(err)
				}
				if err = gate.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				expect(t, awaitBalanceResponse(t, done), 401)
				managementUnchanged(t, f, before)
			})
		}
	}
	for _, scenario := range []struct {
		name, credential, operation, wait string
	}{
		{"source_audit_preview_session", "session", "preview", "source_audit"},
		{"source_audit_apply_api_key", "api_key", "apply", "source_audit"},
		{"late_audit_insert_session", "session", "apply", "audit_insert"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, job := statusCorrectionFixture(t, false, "suspended")
			body := statusCorrectionQuote(t, f, job, "suspended")
			path := adminRenewalPath(f.sid) + "/status-corrections"
			if scenario.operation == "preview" {
				delete(body, "quote_hash")
				path = adminRenewalPath(f.sid) + "/status-correction-quotes"
			}
			secret, table, column, hash := adminRenewalTestCredential(t, f, scenario.credential)
			gate, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(t.Context())
			if scenario.wait == "source_audit" {
				_, err = gate.Exec(t.Context(), "SELECT id FROM audit_events WHERE entity_type='service' AND entity_id=$1 AND action='service.simulation.completed' AND payload->>'job_id'=$2 FOR UPDATE", fmt.Sprint(f.sid), fmt.Sprint(job))
			} else {
				_, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(741098549)")
				if err == nil {
					_, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION wait_status_correction_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='service.status.corrected' THEN PERFORM pg_advisory_xact_lock(741098549); END IF; RETURN NEW; END $$;
 CREATE TRIGGER wait_status_correction_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_status_correction_audit()`)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()+interval '1200 milliseconds' WHERE "+column+"=$1", hash); err != nil {
				t.Fatal(err)
			}
			before := managementFacts(t, f)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- managementCredentialRequest(f, "POST", path, body, "status-late-source-or-audit", secret)
			}()
			waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
			waitAdminRenewalCredentialExpiry(t, f.db, table, column, hash)
			if err = gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, done), 401)
			managementUnchanged(t, f, before)
		})
	}
	for _, wait := range []string{"role", "scope"} {
		t.Run("authority_"+wait+"_changed_while_waiting", func(t *testing.T) {
			f, job := statusCorrectionFixture(t, false, "suspended")
			body := statusCorrectionQuote(t, f, job, "suspended")
			gate, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(t.Context())
			if wait == "role" {
				_, err = gate.Exec(t.Context(), "SELECT id FROM admin_roles WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1) FOR UPDATE", f.actor)
			} else {
				_, err = gate.Exec(t.Context(), "SELECT id FROM administrators WHERE id=$1 FOR UPDATE", f.actor)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := managementFacts(t, f)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- request(f.h, "POST", adminRenewalPath(f.sid)+"/status-corrections", body, f.staff, "status-authority-wait")
			}()
			waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
			status := 403
			if wait == "role" {
				_, err = gate.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view']::text[] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", f.actor)
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
			managementUnchanged(t, f, before)
		})
	}
	for _, operation := range []string{"preview", "apply"} {
		t.Run("entitlement_expires_during_"+operation+"_wait", func(t *testing.T) {
			f, job := statusCorrectionFixture(t, false, "active")
			// Keep the original credential valid. Only the entitlement clock
			// crosses its boundary after initial eligibility was established.
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=clock_timestamp()+interval '1200 milliseconds' WHERE id=$1", f.sid); err != nil {
				t.Fatal(err)
			}
			body := statusCorrectionQuote(t, f, job, "active")
			path := adminRenewalPath(f.sid) + "/status-corrections"
			gate, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(t.Context())
			if operation == "preview" {
				delete(body, "quote_hash")
				path = adminRenewalPath(f.sid) + "/status-correction-quotes"
				_, err = gate.Exec(t.Context(), "SELECT id FROM audit_events WHERE entity_type='service' AND entity_id=$1 AND action='service.simulation.completed' AND payload->>'job_id'=$2 FOR UPDATE", fmt.Sprint(f.sid), fmt.Sprint(job))
			} else {
				_, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(741098549)")
				if err == nil {
					_, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION wait_status_entitlement_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='service.status.corrected' THEN PERFORM pg_advisory_xact_lock(741098549); END IF; RETURN NEW; END $$;
 CREATE TRIGGER wait_status_entitlement_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_status_entitlement_audit()`)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := managementFacts(t, f)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- request(f.h, "POST", path, body, f.staff, "status-entitlement-late")
			}()
			waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
			// This bounded wait follows the actual PostgreSQL entitlement clock.
			if _, err = f.db.Pool.Exec(t.Context(), "SELECT pg_sleep(greatest(0,extract(epoch FROM (next_due_at-clock_timestamp())))+0.05) FROM services WHERE id=$1", f.sid); err != nil {
				t.Fatal(err)
			}
			if err = gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			w := awaitBalanceResponse(t, done)
			expect(t, w, 409)
			if !strings.Contains(w.Body.String(), "service_entitlement_expired") {
				t.Fatal("late rejection did not identify expired entitlement", w.Body.String())
			}
			managementUnchanged(t, f, before)
		})
	}
}
