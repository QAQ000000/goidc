package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/g09fixture"
	"github.com/codex/zjmfgo/internal/notification"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This finite regression uses a source httptest API and two freshly built real
// cmd/worker processes. Only those processes claim/process/complete jobs. It is
// neither a sealed release candidate nor the G09 600-task/capacity acceptance.
// Run exclusively after every other database test has ended:
//
//	ZJMFGO_G09_SOURCE_WORKER_SMOKE=1 REQUIRE_INTEGRATION=1 scripts/go.sh test \
//	  ./internal/httpapi -run '^TestG09SourceWorkerLoopbackSmoke$' -count=1 -timeout=10m -v
func TestG09SourceWorkerLoopbackSmoke(t *testing.T) {
	if os.Getenv("ZJMFGO_G09_SOURCE_WORKER_SMOKE") != "1" {
		t.Skip("opt-in source API + real cmd/worker loopback smoke; no capacity proof")
	}
	rawDSN := os.Getenv("ZJMFGO_TEST_DATABASE_URL")
	parsed, err := g09SourceWorkerDBConfig(rawDSN)
	if err != nil {
		t.Fatal("explicit smoke requires URL-form loopback PostgreSQL _test DSN without options")
	}
	// Read-only preflight occurs before testServer may create its random schema.
	preflightCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	parsed.MaxConns = 1
	parsed.ConnConfig.ConnectTimeout = 5 * time.Second
	preflight, err := pgxpool.NewWithConfig(preflightCtx, parsed)
	if err != nil {
		t.Fatal("isolated PostgreSQL preflight unavailable")
	}
	var version int
	var database string
	err = preflight.QueryRow(preflightCtx, "SELECT current_setting('server_version_num')::int,current_database()").Scan(&version, &database)
	preflight.Close()
	if err != nil || version < 180000 || version >= 190000 || database != parsed.ConnConfig.Database {
		t.Fatal("source worker smoke requires PostgreSQL 18")
	}
	private := t.TempDir()
	if err = os.Chmod(private, 0700); err != nil {
		t.Fatal("private run directory unavailable")
	}
	binary := filepath.Join(private, "worker")
	build := exec.CommandContext(t.Context(), "./scripts/go.sh", "build", "-o", binary, "./cmd/worker")
	build.Dir = "../.."
	if _, err = build.CombinedOutput(); err != nil {
		t.Fatal("source cmd/worker build failed")
	}
	binaryBytes, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal("cannot hash source worker binary")
	}
	binaryHash := g09SourceHash(string(binaryBytes))
	var schema string
	// Registered before testServer's own DROP cleanup, so this read-only check
	// runs after the API store has closed and the owned schema was removed.
	t.Cleanup(func() {
		if schema == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pool, err := pgxpool.NewWithConfig(ctx, parsed.Copy())
		if err != nil {
			t.Error("schema cleanup verification unavailable")
			return
		}
		defer pool.Close()
		var removed bool
		if err = pool.QueryRow(ctx, "SELECT NOT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname=$1)", schema).Scan(&removed); err != nil || !removed {
			t.Error("owned source worker schema was not removed")
		}
	})
	db, handler := testServer(t)
	schema = db.Pool.Config().ConnConfig.RuntimeParams["search_path"]
	scopedDSN, err := g09SourceWorkerScopedDSN(rawDSN, schema)
	if err != nil {
		t.Fatal("worker DSN missing exact isolated schema")
	}
	var actualSchema string
	if err = db.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&actualSchema); err != nil || actualSchema != schema {
		t.Fatal("API store is outside isolated schema")
	}
	uid, customer := user(t, db, handler, "g09-worker-customer", "customer")
	staffID, staff := user(t, db, handler, "g09-worker-staff", "super_admin")
	const recipient = "g09-worker-staff@example.test"
	const phone = "+999000000001"
	const from = "g09-worker-fixture@example.test"
	const token = "g09-loopback-sms-only"
	const mailInstance = "g09-worker-mail"
	const smsInstance = "g09-worker-sms"
	runID := "g09-worker-" + strings.TrimPrefix(schema, "zjmfgo_test_")
	// Fixture-only identity/catalog/template setup, before any worker starts.
	// The customer remains unverified: payment and provision notifications have
	// no recipient; this finite cohort uses the ticket source alone for mail/SMS.
	if _, err = db.Pool.Exec(t.Context(), `UPDATE administrators SET email_verified=true,phone=$2 WHERE id=$1`, staffID, phone); err != nil {
		t.Fatal("staff identity seed failed")
	}
	// Changing phone resets verification through the normal database trigger.
	if _, err = db.Pool.Exec(t.Context(), `UPDATE administrators SET phone_verified=true WHERE id=$1`, staffID); err != nil {
		t.Fatal("staff verification seed failed")
	}
	if _, err = db.Pool.Exec(t.Context(), `UPDATE channel_instances SET enabled=false WHERE category IN ('mail','sms'); UPDATE notification_templates SET active=false`); err != nil {
		t.Fatal("finite channel/template setup failed")
	}
	if _, err = db.Pool.Exec(t.Context(), `UPDATE notification_templates SET active=true,subject=$1,body=$2 WHERE kind='ticket_customer_created'`, runID+" {{ticket_no}}", runID+" {{ticket_no}}\n{{body}}\n{{ticket_url}}"); err != nil {
		t.Fatal("ticket template setup failed")
	}
	pid := product(t, db, 10)
	if _, err = db.Pool.Exec(t.Context(), "UPDATE products SET price_cents=1000,setup_fee_cents=0 WHERE id=$1", pid); err != nil {
		t.Fatal("catalog seed failed")
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE product_prices SET price_cents=1000,setup_fee_cents=0 WHERE product_id=$1 AND billing_cycle='monthly'", pid); err != nil {
		t.Fatal("catalog cycle seed failed")
	}
	var department int64
	if err = db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES('G09 worker smoke','g09-worker') RETURNING id").Scan(&department); err != nil {
		t.Fatal("department seed failed")
	}
	journalPath := filepath.Join(private, "acceptances.private.jsonl")
	journal, err := g09fixture.NewJournal(journalPath)
	if err != nil {
		t.Fatal("private journal creation failed")
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Error("journal close failed")
		}
	})
	smtp, err := g09fixture.StartSMTP(journal)
	if err != nil {
		t.Fatal("loopback SMTP sink unavailable")
	}
	t.Cleanup(func() { smtp.Close() })
	smtpHost, smtpPortText, err := net.SplitHostPort(smtp.Addr())
	if err != nil || smtpHost != "127.0.0.1" {
		t.Fatal("SMTP sink is outside IPv4 loopback")
	}
	smtpPort, err := strconv.Atoi(smtpPortText)
	if err != nil {
		t.Fatal("invalid SMTP loopback port")
	}
	smsHandler, err := g09fixture.SMSHandler(journal, token)
	if err != nil {
		t.Fatal("SMS sink unavailable")
	}
	smsServer := g09SourceLoopbackServer(t, smsHandler)
	apiServer := g09SourceLoopbackServer(t, handler)
	transport := &http.Transport{Proxy: nil}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(client.CloseIdleConnections)
	call := func(method, path, cookie string, body any, key string, status int) map[string]any {
		t.Helper()
		return g09SourceHTTP(t, client, apiServer.URL, method, path, cookie, body, key, status)
	}
	for cookie, id := range map[string]int64{customer: uid, staff: staffID} {
		if got := call("GET", "/api/v1/me", cookie, nil, "", 200); g09SourceID(t, got, "id") != id {
			t.Fatal("real source session identity mismatch")
		}
	}
	// Channels use the real API, including production encryption of SMS secrets.
	// SMTP has no AUTH credentials; its unused at-rest secret satisfies the
	// channel editor's secret-field requirement without enabling SMTP AUTH.
	for _, channel := range []map[string]any{
		{"id": mailInstance, "category": "mail", "implementation_id": "smtp", "display_name": "G09 loopback SMTP", "enabled": true, "secret": token, "config": map[string]any{"host": smtpHost, "port": smtpPort, "from": from, "tls_mode": "none"}},
		{"id": smsInstance, "category": "sms", "implementation_id": "generic_http_json", "display_name": "G09 loopback SMS", "enabled": true, "secret": token, "config": map[string]any{"endpoint": smsServer.URL}},
	} {
		call("POST", "/api/v1/admin/channel-instances", staff, channel, "", 201)
	}
	preferences := call("GET", "/api/v1/me/ticket-notification-preferences", staff, nil, "", 200)
	call("PATCH", "/api/v1/me/ticket-notification-preferences", staff, map[string]any{"email_enabled": true, "sms_enabled": true, "expected_version": preferences["version"]}, "", 200)
	call("POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", uid), staff, map[string]any{"amount_cents": 10000}, runID+"-seed-balance", 201)
	cart := call("GET", "/api/v1/cart", customer, nil, "", 200)
	call("POST", "/api/v1/cart/items", customer, map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "billing_cycle": "monthly", "option_ids": []int64{}, "option_quantities": map[string]int{}, "purchase_fields": map[string]string{}}, runID+"-cart", 200)
	cart = call("GET", "/api/v1/cart", customer, nil, "", 200)
	items, ok := cart["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatal("purchase cart did not contain exactly one item")
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatal("invalid cart item")
	}
	order := call("POST", "/api/v1/orders", customer, map[string]any{"cart_version": cart["version"], "item_ids": []int64{g09SourceID(t, item, "id")}}, runID+"-order", 201)
	orderID, invoiceID := g09SourceID(t, order, "order_id"), g09SourceID(t, order, "invoice_id")
	invoice := call("GET", fmt.Sprintf("/api/v1/invoices/%d", invoiceID), customer, nil, "", 200)
	if g09SourceID(t, invoice, "id") != invoiceID || g09SourceID(t, invoice, "user_id") != uid || invoice["status"] != "unpaid" || invoice["amount_cents"] != float64(1000) {
		t.Fatal("purchase invoice identity/amount/status mismatch before payment")
	}
	call("POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), customer, map[string]any{"expected_version": g09SourceID(t, invoice, "version")}, runID+"-pay", 201)
	ticket := call("POST", "/api/v1/tickets", customer, map[string]any{"subject": runID, "body": "fixture public body", "priority": "normal", "department_id": department, "idempotency_key": runID + "-ticket"}, runID+"-ticket", 201)
	ticketID := g09SourceID(t, ticket, "id")
	var serviceID, jobID int64
	if err = db.Pool.QueryRow(t.Context(), `SELECT s.id,j.id FROM order_items oi JOIN services s ON s.order_item_id=oi.id JOIN provision_jobs j ON j.service_id=s.id AND j.action='create' WHERE oi.order_id=$1`, orderID).Scan(&serviceID, &jobID); err != nil {
		t.Fatal("purchase source-to-job mapping missing")
	}
	type delivery struct {
		ID, EventID, OutboxID                       int64
		Channel, Recipient, Subject, Body, Instance string
	}
	rows, err := db.Pool.Query(t.Context(), `SELECT d.id,e.id,coalesce(d.email_outbox_id,d.sms_outbox_id),d.channel,d.normalized_address,d.rendered_subject,d.rendered_body,d.channel_instance_id FROM ticket_notification_events e JOIN ticket_notification_deliveries d ON d.event_id=e.id WHERE e.ticket_id=$1 AND e.kind='customer_created' AND e.actor_id=$2 AND d.recipient_id=$3 AND d.outcome='queued' AND d.reason='' ORDER BY d.channel`, ticketID, uid, staffID)
	if err != nil {
		t.Fatal("ticket source mapping query failed")
	}
	var deliveries []delivery
	for rows.Next() {
		var d delivery
		if err = rows.Scan(&d.ID, &d.EventID, &d.OutboxID, &d.Channel, &d.Recipient, &d.Subject, &d.Body, &d.Instance); err != nil {
			rows.Close()
			t.Fatal("ticket source mapping invalid")
		}
		deliveries = append(deliveries, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(deliveries) != 2 || deliveries[0].Channel != "email" || deliveries[1].Channel != "sms" || deliveries[0].Recipient != recipient || deliveries[1].Recipient != phone || deliveries[0].Instance != mailInstance || deliveries[1].Instance != smsInstance || deliveries[0].EventID != deliveries[1].EventID {
		t.Fatal("pilot must produce exactly one mapped email and one mapped SMS for one ticket event")
	}
	var ticketNo string
	if err = db.Pool.QueryRow(t.Context(), "SELECT ticket_no FROM tickets WHERE id=$1", ticketID).Scan(&ticketNo); err != nil {
		t.Fatal("ticket number unavailable")
	}
	for _, d := range deliveries {
		if d.OutboxID < 1 || !strings.Contains(d.Body, runID) || !strings.Contains(d.Body, ticketNo) || !strings.Contains(d.Body, "fixture public body") || !strings.Contains(d.Body, fmt.Sprintf("/admin/tickets/%d", ticketID)) || strings.Contains(d.Body, "{{") {
			t.Fatal("ticket source payload not fully rendered")
		}
	}
	mailID, smsID := deliveries[0].OutboxID, deliveries[1].OutboxID
	// Freeze exactly these three pending tasks before starting any process.
	g09AssertSQL(t, db, `SELECT j.status='pending' AND j.attempts=0 AND j.locked_at IS NULL AND s.status='queued' AND s.provision_mode='simulation' AND NOT s.auto_renew FROM provision_jobs j JOIN services s ON s.id=j.service_id WHERE j.id=$1 AND s.id=$2`, jobID, serviceID)
	g09AssertSQL(t, db, `SELECT count(*)=2 AND bool_and(outcome='queued' AND recipient_id=$2 AND address_verified AND preference_enabled) FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.ticket_id=$1`, ticketID, staffID)
	g09AssertSQL(t, db, `SELECT m.status='pending' AND m.attempts=0 AND m.locked_at IS NULL AND m.recipient=$3 AND m.subject=$4 AND m.body=$5 AND m.kind='ticket_customer_created' AND m.channel_instance_id=$6 AND m.expires_at>now()+interval '6 days' AND s.status='pending' AND s.attempts=0 AND s.locked_at IS NULL AND s.recipient=$7 AND octet_length(s.body_encrypted)>0 AND s.kind='ticket_customer_created' AND s.channel_instance_id=$8 AND s.expires_at>now()+interval '6 days' FROM email_outbox m CROSS JOIN sms_outbox s WHERE m.id=$1 AND s.id=$2`, mailID, smsID, recipient, deliveries[0].Subject, deliveries[0].Body, mailInstance, phone, smsInstance)
	g09AssertSQL(t, db, `SELECT (SELECT count(*) FROM channel_instances WHERE enabled AND category='mail')=1 AND (SELECT count(*) FROM channel_instances WHERE enabled AND category='sms')=1 AND (SELECT count(*) FROM notification_templates WHERE active)=2 AND (SELECT count(*) FROM email_outbox)=1 AND (SELECT count(*) FROM sms_outbox)=1 AND (SELECT count(*) FROM provision_jobs)=1`)
	g09AssertSQL(t, db, `SELECT octet_length(secret_ciphertext)>0 AND position(convert_to($2,'UTF8') IN secret_ciphertext)=0 FROM channel_instances WHERE id=$1`, smsInstance, token)
	if len(journal.Snapshot()) != 0 || journal.Err() != nil {
		t.Fatal("protocol activity occurred before worker start")
	}
	if err = g09fixture.WritePrivateJSON(filepath.Join(private, "frozen-sources.private.json"), map[string]any{
		"mode": "source_api_real_cmd_worker", "customerId": uid, "orderId": orderID, "invoiceId": invoiceID,
		"serviceId": serviceID, "provisionJobId": jobID, "expectedProvisionStatus": "succeeded",
		"ticketId": ticketID, "ticketEventId": deliveries[0].EventID,
		"emailDeliveryId": deliveries[0].ID, "emailOutboxId": mailID,
		"smsDeliveryId": deliveries[1].ID, "smsOutboxId": smsID, "expectedOutboxStatus": "sent",
	}); err != nil {
		t.Fatal("finite source registry could not be frozen before workers")
	}
	workerStarted := time.Now().UTC()
	workers := []*g09SourceWorkerChild{
		g09StartSourceWorker(t, binary, private, scopedDSN, db.ProvisionEncryptionKey, 0),
		g09StartSourceWorker(t, binary, private, scopedDSN, db.ProvisionEncryptionKey, 1),
	}
	for _, worker := range workers {
		worker.waitReady(t)
	}
	// Bounded wait uses only explicit IDs; no Claim/Complete/Recover or SQL
	// status/lease/available_at rewrites substitute for the real worker loops.
	deadline := time.Now().Add(45 * time.Second)
	for {
		for _, worker := range workers {
			worker.assertRunning(t)
		}
		var done bool
		err = db.Pool.QueryRow(t.Context(), `SELECT (SELECT status='succeeded' FROM provision_jobs WHERE id=$1) AND (SELECT status='sent' FROM email_outbox WHERE id=$2) AND (SELECT status='sent' FROM sms_outbox WHERE id=$3)`, jobID, mailID, smsID).Scan(&done)
		if err != nil {
			t.Fatal("explicit task completion query failed")
		}
		if done {
			break
		}
		if time.Now().After(deadline) || journal.Err() != nil {
			t.Fatal("bounded source worker cohort did not reach all success states")
		}
		select {
		case <-t.Context().Done():
			t.Fatal("source worker smoke cancelled")
		case <-time.After(100 * time.Millisecond):
		}
	}
	for _, worker := range workers {
		worker.stop(t)
		if !worker.cleanTERM {
			t.Fatal("source worker did not complete clean TERM/Wait")
		}
	}
	workerEnded := time.Now().UTC()
	service := call("GET", fmt.Sprintf("/api/v1/services/%d", serviceID), customer, nil, "", 200)
	if g09SourceID(t, service, "id") != serviceID || service["status"] != "active" || service["provision_mode"] != "simulation" {
		t.Fatal("customer source API does not reflect real worker activation")
	}
	// Entity and accounting conservation, including the per-attempt SMS record.
	g09AssertSQL(t, db, `SELECT j.status='succeeded' AND j.attempts=1 AND j.completed_at IS NOT NULL AND j.locked_at IS NULL AND j.last_error='' AND j.result->>'mode'='simulation' AND s.status='active' AND s.provision_mode='simulation' AND s.upstream_id='local-'||s.id::text AND s.next_due_at>now() AND NOT s.inventory_released AND (SELECT count(*) FROM audit_events WHERE entity_type='service' AND entity_id=s.id::text AND action='service.simulation.completed' AND (payload->>'job_id')::bigint=j.id)=1 FROM provision_jobs j JOIN services s ON s.id=j.service_id WHERE j.id=$1 AND s.id=$2`, jobID, serviceID)
	g09AssertSQL(t, db, `SELECT i.status='paid' AND i.amount_cents=1000 AND i.paid_at IS NOT NULL AND i.user_id=$2 AND o.status='paid' AND o.total_cents=1000 AND o.review_status='approved' AND (SELECT count(*) FROM balance_ledger l WHERE l.user_id=$2 AND l.reference_type='invoice' AND l.reference_id=i.id AND l.entry_type='payment' AND l.delta_cents=-1000)=1 FROM invoices i JOIN orders o ON o.id=i.order_id WHERE i.id=$1 AND o.id=$3`, invoiceID, uid, orderID)
	g09AssertSQL(t, db, `SELECT u.balance_cents=9000 AND u.balance_cents=(SELECT sum(delta_cents) FROM balance_ledger WHERE user_id=u.id) AND (SELECT count(*) FROM balance_ledger WHERE user_id=u.id)=2 AND NOT EXISTS(SELECT 1 FROM customer_credit_accounts WHERE user_id=u.id AND (limit_cents<>0 OR used_cents<>0)) AND NOT EXISTS(SELECT 1 FROM payment_transactions WHERE user_id=u.id) AND p.stock=9 FROM users u CROSS JOIN products p WHERE u.id=$1 AND p.id=$2`, uid, pid)
	g09AssertSQL(t, db, `SELECT count(*)=1 AND bool_and(t.user_id=$2 AND t.department_id=$3 AND (SELECT count(*) FROM ticket_mutation_receipts WHERE ticket_id=t.id AND actor_id=$2 AND operation='create')=1 AND (SELECT count(*) FROM ticket_notification_events WHERE ticket_id=t.id AND actor_id=$2 AND kind='customer_created')=1) FROM tickets t WHERE t.id=$1`, ticketID, uid, department)
	g09AssertSQL(t, db, `SELECT m.status='sent' AND m.attempts=1 AND m.sent_at IS NOT NULL AND m.locked_at IS NULL AND m.last_error='' AND s.status='sent' AND s.attempts=1 AND s.sent_at IS NOT NULL AND s.locked_at IS NULL AND s.last_error='' AND octet_length(s.body_encrypted)=0 AND (SELECT count(*) FROM sms_delivery_evidence e WHERE e.outbox_id=s.id AND e.attempts=s.attempts AND e.provider='generic_http_json' AND e.outcome='accepted' AND e.http_status=200 AND NOT e.response_truncated AND octet_length(e.response_encrypted)>0 AND e.locked_at IS NOT NULL)=1 FROM email_outbox m CROSS JOIN sms_outbox s WHERE m.id=$1 AND s.id=$2`, mailID, smsID)
	var smsLockedAt time.Time
	var smsResponse []byte
	if err = db.Pool.QueryRow(t.Context(), "SELECT locked_at,response_encrypted FROM sms_delivery_evidence WHERE outbox_id=$1 AND attempts=1", smsID).Scan(&smsLockedAt, &smsResponse); err != nil {
		t.Fatal("SMS per-attempt evidence unavailable")
	}
	responseDomain := fmt.Sprintf("sms:response:%d:1:%s", smsID, smsLockedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"))
	openedResponse, err := openSMSEvidence(db.ProvisionEncryptionKey, smsResponse, responseDomain)
	if err != nil || string(openedResponse) != `{"accepted":true}` {
		t.Fatal("SMS evidence does not retain the exact loopback response")
	}
	// No global queue-zero claim: only the frozen source ID set is required to
	// reach succeeded/sent. Real cmd/worker schedulers are never disabled/reset.
	accepted := journal.Snapshot()
	if journal.Err() != nil || len(accepted) != 2 {
		t.Fatal("durable protocol journal missing or duplicated finite source acceptances")
	}
	wantMail := g09SourceSMTPHash(from, deliveries[0].Recipient, deliveries[0].Subject, deliveries[0].Body)
	wantSMS := g09SourceHash(deliveries[1].Body)
	matched := map[string]g09fixture.Acceptance{}
	for i, record := range accepted {
		if record.Sequence != uint64(i+1) || record.AcceptedAt.Before(workerStarted) || record.AcceptedAt.After(workerEnded) {
			t.Fatal("protocol acceptance outside worker interval or invalid sequence")
		}
		var address, messageHash string
		switch record.Channel {
		case "email":
			address, messageHash = recipient, wantMail
		case "sms":
			address, messageHash = phone, wantSMS
		default:
			t.Fatal("unexpected protocol channel")
		}
		if _, duplicate := matched[record.Channel]; duplicate || record.RecipientHash != g09SourceHash(address) || record.MessageHash != messageHash {
			t.Fatal("protocol acceptance does not match the source delivery payload/identity")
		}
		matched[record.Channel] = record
	}
	file, err := os.Open(journalPath)
	if err != nil {
		t.Fatal("durable journal unavailable")
	}
	var durable []g09fixture.Acceptance
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record g09fixture.Acceptance
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			file.Close()
			t.Fatal("invalid durable journal record")
		}
		durable = append(durable, record)
	}
	err = scanner.Err()
	closeErr := file.Close()
	if err != nil || closeErr != nil || !reflect.DeepEqual(durable, accepted) {
		t.Fatal("journal file and acceptance snapshot differ")
	}
	type mappedAcceptance struct {
		TicketID, EventID, DeliveryID, OutboxID int64
		Acceptance                              g09fixture.Acceptance
	}
	mapping := make([]mappedAcceptance, 0, len(deliveries))
	for _, d := range deliveries {
		mapping = append(mapping, mappedAcceptance{ticketID, d.EventID, d.ID, d.OutboxID, matched[d.Channel]})
	}
	if err = g09fixture.WritePrivateJSON(filepath.Join(private, "source-worker-result.private.json"), map[string]any{
		"schemaVersion": 1, "mode": "source_api_real_cmd_worker", "candidateVerified": false, "capacityVerified": false,
		"workerBinarySHA256": binaryHash, "workerPIDs": []int{workers[0].cmd.Process.Pid, workers[1].cmd.Process.Pid}, "workersExitedViaTERM": true,
		"customerId": uid, "orderId": orderID, "invoiceId": invoiceID, "serviceId": serviceID, "provisionJobId": jobID,
		"notificationSources": mapping, "smsResponseEvidenceVerified": true,
	}); err != nil {
		t.Fatal("private source evidence write failed")
	}
	t.Logf("PASS: source HTTP purchase/ticket -> real cmd/worker x2 -> simulation job %d succeeded + email %d/SMS %d sent; SQL source mapping and durable protocol hashes agree; TERM/Wait complete; candidate/600-task/capacity/minute-tick stability unverified", jobID, mailID, smsID)
}

func g09SourceWorkerDBConfig(raw string) (*pgxpool.Config, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Fragment != "" {
		return nil, fmt.Errorf("invalid test DSN")
	}
	c, err := pgxpool.ParseConfig(raw)
	if err != nil || !strings.HasSuffix(c.ConnConfig.Database, "_test") || c.ConnConfig.RuntimeParams["options"] != "" {
		return nil, fmt.Errorf("invalid test database")
	}
	for _, host := range append([]string{c.ConnConfig.Host}, g09FallbackHosts(c)...) {
		if host != "127.0.0.1" && host != "::1" && host != "localhost" {
			return nil, fmt.Errorf("test database must be loopback")
		}
	}
	return c, nil
}

func g09SourceWorkerScopedDSN(raw, schema string) (string, error) {
	if !strings.HasPrefix(schema, "zjmfgo_test_") || len(schema) != len("zjmfgo_test_")+16 {
		return "", fmt.Errorf("invalid isolated schema")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(schema, "zjmfgo_test_")); err != nil {
		return "", fmt.Errorf("invalid isolated schema")
	}
	if _, err := g09SourceWorkerDBConfig(raw); err != nil {
		return "", err
	}
	u, _ := url.Parse(raw)
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	scoped := u.String()
	c, err := g09SourceWorkerDBConfig(scoped)
	if err != nil || c.ConnConfig.RuntimeParams["search_path"] != schema {
		return "", fmt.Errorf("isolated search_path unavailable")
	}
	return scoped, nil
}

func g09SourceLoopbackServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	// httptest binds 127.0.0.1 by default. Refuse a future default change.
	addr, ok := server.Listener.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.Equal(net.ParseIP("127.0.0.1")) {
		server.Listener.Close()
		t.Fatal("source HTTP listener is outside IPv4 loopback")
	}
	server.Config.ReadHeaderTimeout = 5 * time.Second
	server.Config.ReadTimeout = 10 * time.Second
	server.Config.WriteTimeout = 10 * time.Second
	server.Config.IdleTimeout = 5 * time.Second
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func g09SourceHTTP(t *testing.T, client *http.Client, origin, method, path, cookie string, body any, key string, status int) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal("invalid source request body")
	}
	req, err := http.NewRequestWithContext(t.Context(), method, origin+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("source request construction failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:3088")
	req.Header.Set("X-Requested-With", "zjmfgo")
	req.Header.Set("Cookie", cookie)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal("source loopback HTTP request failed")
	}
	defer res.Body.Close()
	response, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(response) > 1<<20 || res.StatusCode != status {
		t.Fatalf("source %s %s returned unexpected status/size (got %d, want %d)", method, path, res.StatusCode, status)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if json.Unmarshal(response, &envelope) != nil || envelope.Data == nil {
		t.Fatal("source HTTP response data invalid")
	}
	return envelope.Data
}

func g09SourceID(t *testing.T, data map[string]any, key string) int64 {
	t.Helper()
	n, ok := data[key].(float64)
	if !ok || n < 1 || n > 1<<53 || n != float64(int64(n)) {
		t.Fatal("source response identifier/version invalid")
	}
	return int64(n)
}

func g09SourceHash(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// The SMTP fixture hashes DotReader's newline-canonicalized MIME DATA, while
// SMS hashes the parsed message. This expected frame is derived from the frozen
// source outbox fields, not from the sink's observed record.
func g09SourceSMTPHash(from, recipient, subject, body string) string {
	frame := "From: " + (&mail.Address{Address: from}).String() + "\n" +
		"To: " + recipient + "\n" +
		"Subject: " + mime.QEncoding.Encode("UTF-8", subject) + "\n" +
		"MIME-Version: 1.0\nContent-Type: text/plain; charset=UTF-8\n\n" +
		strings.ReplaceAll(body, "\r\n", "\n") + "\n"
	return g09SourceHash(frame)
}

type g09SourceWorkerChild struct {
	cmd       *exec.Cmd
	done      chan error
	logPath   string
	log       *os.File
	stopped   bool
	cleanTERM bool
}

func g09StartSourceWorker(t *testing.T, binary, dir, dsn string, key []byte, slot int) *g09SourceWorkerChild {
	t.Helper()
	return g09StartSourceWorkerOrigin(t, binary, dir, dsn, key, slot, "http://localhost:3088")
}

func g09StartSourceWorkerOrigin(t *testing.T, binary, dir, dsn string, key []byte, slot int, origin string) *g09SourceWorkerChild {
	t.Helper()
	c := &g09SourceWorkerChild{done: make(chan error, 1), logPath: filepath.Join(dir, fmt.Sprintf("worker-%d.private.jsonl", slot))}
	var err error
	c.log, err = os.OpenFile(c.logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("private worker log creation failed")
	}
	c.cmd = exec.Command(binary)
	c.cmd.Dir = dir // No project .env, credentials, proxies or inherited channel config.
	c.cmd.Env = []string{"DATABASE_URL=" + dsn, "PUBLIC_ORIGIN=" + origin, "PROVISION_ENCRYPTION_KEY=" + hex.EncodeToString(key), "PAYMENT_MOCK_ENABLED=false", "SMTP_HOST=127.0.0.1", "SMTP_PORT=1", "SMTP_FROM=g09-disabled@example.test", "SMTP_TLS_MODE=none"}
	c.cmd.Stdout, c.cmd.Stderr = c.log, c.log
	if err = c.cmd.Start(); err != nil {
		c.log.Close()
		t.Fatal("source worker start failed")
	}
	go func() { c.done <- c.cmd.Wait() }()
	t.Cleanup(func() {
		if !c.stopped {
			c.stop(t)
		}
		if err := c.log.Close(); err != nil {
			t.Error("private worker log close failed")
		}
	})
	// Linux evidence guards the exact child we own before any signal is sent.
	proc := fmt.Sprintf("/proc/%d", c.cmd.Process.Pid)
	exe, exeErr := os.Readlink(proc + "/exe")
	cwd, cwdErr := os.Readlink(proc + "/cwd")
	if exeErr != nil || cwdErr != nil || exe != binary || cwd != dir {
		t.Fatal("worker PID executable/working-directory mismatch")
	}
	return c
}

func (c *g09SourceWorkerChild) assertRunning(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
		c.stopped = true
		t.Fatal("real cmd/worker exited before bounded cohort finished; private log retained until cleanup")
	default:
	}
}

func (c *g09SourceWorkerChild) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		c.assertRunning(t)
		raw, err := os.ReadFile(c.logPath)
		if err != nil {
			t.Fatal("source worker readiness log unavailable")
		}
		if bytes.Contains(raw, []byte(`"msg":"provision worker started"`)) {
			return
		}
		select {
		case <-t.Context().Done():
			t.Fatal("source worker readiness cancelled")
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("source worker readiness timeout")
}

func (c *g09SourceWorkerChild) stop(t *testing.T) {
	t.Helper()
	if c.stopped {
		return
	}
	signalErr := c.cmd.Process.Signal(syscall.SIGTERM)
	if signalErr != nil {
		t.Error("owned worker TERM failed")
	}
	select {
	case err := <-c.done:
		c.stopped = true
		c.cleanTERM = signalErr == nil && err == nil
		if err != nil {
			t.Error("owned worker did not exit cleanly after TERM")
		}
	case <-time.After(10 * time.Second):
		_ = c.cmd.Process.Kill()
		<-c.done
		c.stopped = true
		t.Error("owned worker required KILL after TERM timeout")
	}
	if c.cmd.Process.Signal(syscall.Signal(0)) == nil {
		t.Error("owned worker remains alive after Wait")
	}
}

// Pure guards and actual protocol fingerprint checks are safe without a DSN.
// The opt-in integration above is deliberately not executed by these tests.
func TestG09SourceWorkerGuards(t *testing.T) {
	t.Setenv("PGOPTIONS", "")
	t.Setenv("PGSERVICE", "")
	const base = "postgres://fixture:fixture@127.0.0.1:5432/fixture_test?sslmode=disable"
	const schema = "zjmfgo_test_0123456789abcdef"
	for _, raw := range []string{
		base,
		"postgresql://fixture:fixture@[::1]:5432/fixture_test?sslmode=disable",
	} {
		scoped, err := g09SourceWorkerScopedDSN(raw, schema)
		if err != nil {
			t.Fatal("valid isolated DSN rejected")
		}
		c, err := g09SourceWorkerDBConfig(scoped)
		if err != nil || c.ConnConfig.RuntimeParams["search_path"] != schema {
			t.Fatal("worker DSN did not retain exact schema")
		}
	}
	for _, raw := range []string{
		"", "host=127.0.0.1 dbname=fixture_test", "postgres://fixture@db.example.test/fixture_test", "postgres://fixture@127.0.0.1/business",
		base + "&host=127.0.0.1,db.example.test", base + "&options=-csearch_path%3Dpublic", base + "#fragment",
	} {
		if _, err := g09SourceWorkerDBConfig(raw); err == nil {
			t.Fatal("unsafe worker DSN accepted")
		}
	}
	for _, invalid := range []string{"public", schema + ",public", "zjmfgo_test_zzzzzzzzzzzzzzzz", "zjmfgo_test_0123456789abcde"} {
		if _, err := g09SourceWorkerScopedDSN(base, invalid); err == nil {
			t.Fatal("unsafe worker schema accepted")
		}
	}
	private := t.TempDir()
	if err := os.Chmod(private, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := g09fixture.NewJournal(filepath.Join(private, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	smtp, err := g09fixture.StartSMTP(journal)
	if err != nil {
		t.Fatal(err)
	}
	defer smtp.Close()
	host, portText, _ := net.SplitHostPort(smtp.Addr())
	port, _ := strconv.Atoi(portText)
	const from = "fixture@example.test"
	provider := notification.SMTPConfig{Host: host, Port: port, From: from, TLSMode: "none"}
	for i, body := range []string{"ascii body\nsecond line", "中文正文\n.dot escaped\r\nlast line\n"} {
		job := store.EmailJob{Recipient: "staff@example.test", Subject: "G09 工单 " + strconv.Itoa(i), Body: body}
		if err = provider.Send(job); err != nil {
			t.Fatal("loopback SMTP fingerprint send failed")
		}
		observed := journal.Snapshot()
		if len(observed) != i+1 || observed[i].MessageHash != g09SourceSMTPHash(from, job.Recipient, job.Subject, job.Body) || observed[i].RecipientHash != g09SourceHash(job.Recipient) || journal.Err() != nil {
			t.Fatal("source-to-SMTP fingerprint differs from actual transport")
		}
	}
}
