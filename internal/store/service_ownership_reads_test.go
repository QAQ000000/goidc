package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/jackc/pgx/v5"
)

type ownershipReadFixture struct {
	notificationFixture
	service, order, invoice, job, change, cancellation, audit int64
	adminContext                                              context.Context
}

func newOwnershipReadFixture(t *testing.T, monitorProfile ...string) ownershipReadFixture {
	t.Helper()
	f := ownershipReadFixture{notificationFixture: notificationCenterFixture(t)}
	f.exec(t, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('ownership-read-admin',$1,clock_timestamp()+interval '1 hour')`, f.actor)
	f.adminContext = WithStorageCredential(f.ctx, "ownership-read-admin", false)
	purchase := financialPurchase(t, f.db, f.customer, "ownership-read-order", time.Now(), []int64{1000}, nil, nil, 1000)
	f.order, f.invoice = purchase.order, purchase.invoice
	f.exec(t, `UPDATE orders SET review_status='approved' WHERE id=$1`, f.order)
	f.exec(t, `UPDATE order_items SET purchase_fields='[{"key":"contact","value":"old-owner-private-purchase","encrypted":false}]' WHERE id=$1`, purchase.lines[0])
	mode, upstream, delivery := "simulation", "ownership-resource", []byte(`{}`)
	if len(monitorProfile) > 0 {
		mode, upstream = "nokvm", "42"
		delivery, _ = json.Marshal(map[string]string{"profile_id": monitorProfile[0]})
	}
	if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,next_due_at,provision_mode,upstream_id,resource_info,delivery_snapshot)
 VALUES($1,$2,$3,$4,'Transferred resource','ownership-current.example.test','monthly','active',clock_timestamp()+interval '30 days',$5,$6,
 '{"dedicated_ip":"203.0.113.9","power":"running","console_url":"https://old-owner-private-console.invalid","internal_note":"old-owner-private-resource"}',$7) RETURNING id`,
		f.customer, f.order, purchase.lines[0], purchase.products[0], mode, upstream, delivery).Scan(&f.service); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE services SET last_error='old-owner-private-service-error' WHERE id=$1`, f.service)
	if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO provision_jobs(service_id,action,status,idempotency_key,request_fingerprint,request_payload,result,last_error,completed_at)
 VALUES($1,'reinstall','succeeded','ownership-old-job',repeat('a',64),'{"id":1,"name":"old-owner-private-job","group":"old-owner-private-job-group","login":"root"}',
 '{"private":"old-owner-private-result","cost_cents":555}','old-owner-private-error',clock_timestamp()) RETURNING id`, f.service).Scan(&f.job); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO service_cancellation_requests(service_id,user_id,type,reason,status,idempotency_key,resolution_note)
 VALUES($1,$2,'immediate','old-owner-private-cancellation','withdrawn','ownership-old-cancel','old-owner-private-resolution') RETURNING id`, f.service, f.customer).Scan(&f.cancellation); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO service_configuration_changes(service_id,user_id,product_revision,service_version,billing_cycle,due_at,
 old_recurring_cents,new_recurring_cents,amount_cents,credit_cents,old_snapshot,new_snapshot,option_ids,option_quantities,status,invoice_id)
 SELECT id,user_id,1,version,billing_cycle,next_due_at,1000,1000,0,0,'[]','[{"label":"old-owner-private-configuration"}]','[]','{}','completed',$2 FROM services WHERE id=$1 RETURNING id`, f.service, f.invoice).Scan(&f.change); err != nil {
		t.Fatal(err)
	}
	f.audit = f.recordAudit(t, "service.pricing.updated")
	return f
}

func (f ownershipReadFixture) customerContext(customer int64) context.Context {
	return WithStorageCredential(f.ctx, fmt.Sprint("notification-session-", customer), false)
}

func (f ownershipReadFixture) recordAudit(t *testing.T, action string) int64 {
	t.Helper()
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		return Audit(f.ctx, tx, f.actor, action, "service", f.service, map[string]any{"private": "owner-private-audit"})
	}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := f.db.Pool.QueryRow(f.ctx, `SELECT max(id) FROM audit_events WHERE entity_type='service' AND entity_id=$1::bigint::text`, f.service).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f ownershipReadFixture) transferInput(t *testing.T, target int64) AdminServiceTransferInput {
	t.Helper()
	var version, owner, generation int64
	if err := f.db.Pool.QueryRow(f.ctx, `SELECT version,user_id,service_ownership_generation(id) FROM services WHERE id=$1`, f.service).Scan(&version, &owner, &generation); err != nil {
		t.Fatal(err)
	}
	in := AdminServiceTransferInput{ExpectedVersion: version, ExpectedOwnerGeneration: &generation, SourceCustomerID: owner, TargetCustomerID: target,
		CredentialHandoverConfirmed: true, Reason: "Verified transfer fixture", HandoverEvidence: "fixture://rotated-resource-access"}
	quote, err := f.db.QuoteAdminServiceTransfer(f.adminContext, f.actor, f.service, in)
	if err != nil {
		t.Fatal(err)
	}
	in.QuoteHash = quote.QuoteHash
	return in
}

func (f ownershipReadFixture) transfer(t *testing.T, target int64, key string) AdminServiceTransferResult {
	t.Helper()
	out, err := f.db.TransferAdminService(f.adminContext, f.actor, f.service, key, f.transferInput(t, target))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func ownershipReadJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func ownershipReadPageItems(t *testing.T, page map[string]any) []map[string]any {
	t.Helper()
	var result struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(ownershipReadJSON(t, page)), &result); err != nil {
		t.Fatal(err)
	}
	return result.Items
}

func TestServiceOwnershipCustomerReadPrivacyAndRoundTrip(t *testing.T) {
	f := newOwnershipReadFixture(t)
	before, err := f.db.ServiceAccountDetail(f.customerContext(f.customer), f.customer, f.service, false)
	if err != nil || !strings.Contains(ownershipReadJSON(t, before["purchase_fields"]), "old-owner-private-purchase") {
		t.Fatal("original owner lost its purchase fields", err)
	}
	if before["owner_generation"] != int64(0) || before["last_error"] != "old-owner-private-service-error" || len(before["jobs"].([]map[string]any)) != 1 {
		t.Fatal("generation zero changed the existing service or job response", before)
	}
	originalJob := before["jobs"].([]map[string]any)[0]
	if originalJob["action"] != "reinstall" || originalJob["last_error"] != "old-owner-private-error" || !strings.Contains(ownershipReadJSON(t, originalJob["request_payload"]), "old-owner-private-job") ||
		!strings.Contains(ownershipReadJSON(t, originalJob["result"]), "old-owner-private-result") {
		t.Fatal("generation zero lost existing job details", originalJob)
	}
	originalList, err := f.db.ServiceAccountList(f.ctx, ServiceAccountListFilter{Actor: f.customer, Limit: 20})
	if err != nil || !strings.Contains(ownershipReadJSON(t, originalList), "old-owner-private-service-error") {
		t.Fatal("generation zero service listing changed its error projection", err)
	}
	f.transfer(t, f.other, "ownership-read-first")
	if _, err = f.db.ServiceAccountDetail(f.customerContext(f.customer), f.customer, f.service, false); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("old owner retained service detail", err)
	}
	originalOrder, err := f.db.ReadCustomerOrder(f.ctx, f.customer, f.order)
	if err != nil || len(originalOrder["services"].([]map[string]any)) != 0 || !strings.Contains(ownershipReadJSON(t, originalOrder["items"]), "old-owner-private-purchase") {
		t.Fatal("transfer rewrote historical order ownership or followed the live resource", err)
	}
	assertCurrentGeneration := func(customer int64, generation int64) {
		t.Helper()
		detail, readErr := f.db.ServiceAccountDetail(f.customerContext(customer), customer, f.service, false)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if detail["owner_generation"] != generation || detail["order_id"] != nil || detail["order_item_id"] != nil || detail["external_refund_order_hold"] != nil ||
			len(detail["jobs"].([]map[string]any)) != 0 || len(detail["configuration_changes"].([]map[string]any)) != 0 || detail["cancellation"] != nil ||
			ownershipReadJSON(t, detail["purchase_fields"]) != "[]" || strings.Contains(ownershipReadJSON(t, detail), "old-owner-private") {
			t.Fatalf("new ownership generation exposed old customer history: %#v", detail)
		}
		if !strings.Contains(ownershipReadJSON(t, detail["resource_info"]), "203.0.113.9") || detail["recurring_price_cents"] != int64(1000) {
			t.Fatal("transfer discarded resource identity or the renewal contract")
		}
		list, listErr := f.db.ServiceAccountList(f.ctx, ServiceAccountListFilter{Actor: customer, Limit: 20})
		if listErr != nil || strings.Contains(ownershipReadJSON(t, list), "old-owner-private") {
			t.Fatal("service list exposed old customer data", listErr)
		}
		activity, activityErr := f.db.ServiceAccountActivity(f.customerContext(customer), ServiceActivityAccountFilter{Actor: customer, ServiceID: f.service, Limit: 100})
		if activityErr != nil {
			t.Fatal(activityErr)
		}
		for _, item := range ownershipReadPageItems(t, activity) {
			if item["id"] == fmt.Sprint("audit-", f.audit) || item["id"] == fmt.Sprint("job-", f.job) || strings.Contains(ownershipReadJSON(t, item), "handover_evidence") {
				t.Fatal("activity crossed the latest ownership floor", item)
			}
		}
	}
	assertCurrentGeneration(f.other, 1)
	// Even the new generation's successful/failed jobs expose only safe status
	// fields to customers; worker request/result payloads remain administrative.
	f.exec(t, `INSERT INTO provision_jobs(service_id,action,status,idempotency_key,request_fingerprint,request_payload,result,last_error)
 VALUES($1,'reinstall','succeeded','ownership-target-job',repeat('b',64),'{"id":2,"name":"target-private-request","group":"target-private-group","login":"root"}',
 '{"private":"target-private-result"}','target-private-error')`, f.service)
	f.recordAudit(t, "service.auto_renew.updated")
	detail, err := f.db.ServiceAccountDetail(f.customerContext(f.other), f.other, f.service, false)
	if err != nil || len(detail["jobs"].([]map[string]any)) != 1 || strings.Contains(ownershipReadJSON(t, detail), "target-private") {
		t.Fatal("customer jobs returned raw worker payloads", err)
	}
	if detail["jobs"].([]map[string]any)[0]["action"] != "reinstall" {
		t.Fatal("customer job projection changed the safe operation status")
	}
	f.transfer(t, f.customer, "ownership-read-return")
	assertCurrentGeneration(f.customer, 2)
}

func TestServiceOwnershipAdminScopedFinancialReadIsolation(t *testing.T) {
	f := newOwnershipReadFixture(t)
	var role, sourceAdmin, targetAdmin int64
	if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO admin_roles(name,permissions) VALUES('Ownership scoped reader',ARRAY['services.view','orders.view','customers.view']) RETURNING id`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	for i, actor := range []*int64{&sourceAdmin, &targetAdmin} {
		if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO administrators(username,email,password_hash,display_name,role,admin_role_id,customer_scope,sales_enabled)
 VALUES($1,$1||'@test.invalid','unused','Scoped reader','admin',$2,'owned',true) RETURNING id`, fmt.Sprint("ownership-reader-", i), role).Scan(actor); err != nil {
			t.Fatal(err)
		}
	}
	f.exec(t, `UPDATE users SET sales_owner_id=CASE WHEN id=$1 THEN $3::bigint ELSE $4::bigint END WHERE id IN ($1,$2)`, f.customer, f.other, sourceAdmin, targetAdmin)
	f.transfer(t, f.other, "ownership-read-scope")
	detail, err := f.db.ServiceAccountDetail(f.ctx, targetAdmin, f.service, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"order_id", "order_item_id", "order_no", "invoice_id", "invoice_no", "invoice_status", "paid_at", "first_price_cents", "setup_fee_cents"} {
		if detail[field] != nil {
			t.Fatalf("target-only administrator saw original financial %s: %#v", field, detail[field])
		}
	}
	if len(detail["jobs"].([]map[string]any)) != 0 || len(detail["configuration_changes"].([]map[string]any)) != 0 || detail["cancellation"] != nil {
		t.Fatal("target-only administrator inherited prior customer history")
	}
	for _, field := range []string{"purchase_fields", "jobs", "configuration_changes", "cancellation"} {
		if strings.Contains(ownershipReadJSON(t, detail[field]), "old-owner-private") {
			t.Fatalf("target-only administrator saw prior customer %s", field)
		}
	}
	order, err := f.db.ReadAdminOrder(f.ctx, sourceAdmin, f.order)
	if err != nil || len(order["services"].([]map[string]any)) != 0 {
		t.Fatal("source-only administrator followed a transferred service", err)
	}
	orders, err := f.db.OrderAccountList(f.ctx, OrderAccountListFilter{Actor: sourceAdmin, Admin: true, Limit: 20})
	if err != nil || strings.Contains(ownershipReadJSON(t, orders), "ownership-current.example.test") || strings.Contains(ownershipReadJSON(t, orders), "203.0.113.9") {
		t.Fatal("source order listing leaked live target resource identity", err)
	}
	superDetail, err := f.db.ServiceAccountDetail(f.ctx, f.actor, f.service, true)
	if err != nil || superDetail["order_id"] != f.order || superDetail["invoice_id"] != f.invoice || len(superDetail["jobs"].([]map[string]any)) != 1 {
		t.Fatal("full-scope administrator lost authorized financial history", err)
	}
}

func TestServiceOwnershipAuditHistoryRetainsEventOwner(t *testing.T) {
	f := newOwnershipReadFixture(t)
	f.transfer(t, f.other, "ownership-audit-first")
	targetAudit := f.recordAudit(t, "service.auto_renew.updated")
	f.transfer(t, f.customer, "ownership-audit-return")
	returnedAudit := f.recordAudit(t, "service.renewal.created")
	for _, tc := range []struct {
		customer int64
		present  []int64
		absent   []int64
	}{{f.customer, []int64{f.audit, returnedAudit}, []int64{targetAudit}}, {f.other, []int64{targetAudit}, []int64{f.audit, returnedAudit}}} {
		page, err := f.db.AdminAuditLogPage(f.ctx, url.Values{"customer_id": []string{fmt.Sprint(tc.customer)}}, f.actor, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		ids, transfers := map[string]bool{}, 0
		for _, item := range ownershipReadPageItems(t, page) {
			ids[fmt.Sprint(item["id"])] = true
			if item["action"] == "service.ownership.transferred" {
				transfers++
			}
		}
		for _, id := range tc.present {
			if !ids[fmt.Sprint(id)] {
				t.Fatalf("customer %d lost its own immutable event %d", tc.customer, id)
			}
		}
		for _, id := range tc.absent {
			if ids[fmt.Sprint(id)] {
				t.Fatalf("customer %d acquired another generation event %d", tc.customer, id)
			}
		}
		if transfers != 2 {
			t.Fatal("both parties must retain each transfer fact", transfers)
		}
	}
}

func TestServiceOwnershipDetailWaitRechecksOwner(t *testing.T) {
	f := newOwnershipReadFixture(t)
	in := f.transferInput(t, f.other)
	blocker, err := f.db.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(f.ctx)
	var blockerPID int32
	if err = blocker.QueryRow(f.ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(71910321)`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `CREATE FUNCTION ownership_read_audit_gate() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.action='service.ownership.transferred' THEN PERFORM pg_advisory_xact_lock(71910321); END IF; RETURN NEW; END $$;
 CREATE TRIGGER ownership_read_audit_gate BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION ownership_read_audit_gate()`)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	transferred := make(chan error, 1)
	go func() {
		_, transferErr := f.db.TransferAdminService(WithStorageCredential(ctx, "ownership-read-admin", false), f.actor, f.service, "ownership-read-lock", in)
		transferred <- transferErr
	}()
	waiter := func(blocking int32) int32 {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var pid int32
			waitErr := f.db.Pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) ORDER BY pid LIMIT 1`, blocking).Scan(&pid)
			if waitErr == nil {
				return pid
			}
			if !errors.Is(waitErr, pgx.ErrNoRows) {
				t.Fatal(waitErr)
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("expected real PostgreSQL ownership read wait was not observed")
		return 0
	}
	transferPID := waiter(blockerPID)
	read := make(chan error, 1)
	go func() {
		_, readErr := f.db.ServiceAccountDetail(WithStorageCredential(ctx, fmt.Sprint("notification-session-", f.customer), false), f.customer, f.service, false)
		read <- readErr
	}()
	waiter(transferPID)
	if err = blocker.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-transferred; err != nil {
		t.Fatal(err)
	}
	if err = <-read; !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("detail wait returned the prior owner's private projection", err)
	}
}

func TestServiceOwnershipRemoteReadRejectsReturnedOwner(t *testing.T) {
	f := newOwnershipReadFixture(t, "ownership-monitor")
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/virtual_monitoring/42":
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, `{"code":0,"data":[]}`)
		case "/api/virtual/42":
			fmt.Fprint(w, `{"code":0,"data":{"flow":{"code":0,"data":{"in":123,"out":456}},"flow_limit":1}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	defer unblock()
	adapter, err := provision.New(provision.Profile{ID: "ownership-monitor", Name: "Ownership read fixture", URL: remote.URL, Token: "fixture-token"})
	if err != nil {
		t.Fatal(err)
	}
	f.db.Provisioners = map[string]*provision.NOKVM{"ownership-monitor": adapter}
	ctx, cancel := context.WithTimeout(f.customerContext(f.customer), 10*time.Second)
	defer cancel()
	type readResult struct {
		data provision.Monitoring
		err  error
	}
	read := make(chan readResult, 1)
	go func() {
		data, readErr := f.db.ServiceMonitoring(ctx, f.customer, f.service, false, "1h")
		read <- readResult{data: data, err: readErr}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("monitoring request did not reach the supplier simulator", ctx.Err())
	}
	// Both transfers complete while the remote read is blocked. The owner ID,
	// profile and upstream resource return to their original values, but the
	// ownership generation must invalidate the response from the earlier tenure.
	f.transfer(t, f.other, "ownership-monitor-first")
	f.transfer(t, f.customer, "ownership-monitor-return")
	unblock()
	select {
	case result := <-read:
		var failure *Conflict
		if !errors.As(result.err, &failure) || failure.Code != "monitoring_resource_changed" {
			t.Fatal("remote response survived an ownership round trip", result.err)
		}
		if !result.data.Start.IsZero() || !result.data.End.IsZero() || len(result.data.Samples) != 0 || result.data.Usage != nil {
			t.Fatal("rejected remote read still returned prior ownership data", result.data)
		}
	case <-ctx.Done():
		t.Fatal("monitoring read did not finish after releasing the supplier", ctx.Err())
	}
}
