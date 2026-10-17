package httpapi

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/g09fixture"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This separate opt-in admits one purchase/create and one API-requested sync.
// It never calls store Claim/Process/Complete or rewrites jobs/services. It is
// source integration infrastructure, not 600-task or supplier capacity proof.
func TestG09SupplierWorkerLoopbackSmoke(t *testing.T) {
	if os.Getenv("ZJMFGO_G09_SUPPLIER_WORKER_SMOKE") != "1" {
		t.Skip("opt-in supplier source API + real cmd/worker; PostgreSQL execution remains separate")
	}
	rawDSN := os.Getenv("ZJMFGO_TEST_DATABASE_URL")
	parsed, err := g09SourceWorkerDBConfig(rawDSN)
	if err != nil {
		t.Fatal("supplier smoke requires explicit URL-form loopback PostgreSQL _test DSN without options")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	parsed.MaxConns = 1
	parsed.ConnConfig.ConnectTimeout = 5 * time.Second
	preflight, err := pgxpool.NewWithConfig(ctx, parsed)
	if err != nil {
		cancel()
		t.Fatal("isolated PostgreSQL preflight unavailable")
	}
	var version int
	var database string
	err = preflight.QueryRow(ctx, "SELECT current_setting('server_version_num')::int,current_database()").Scan(&version, &database)
	preflight.Close()
	cancel()
	if err != nil || version < 180000 || version >= 190000 || database != parsed.ConnConfig.Database {
		t.Fatal("supplier worker smoke requires PostgreSQL 18")
	}
	private := t.TempDir()
	if os.Chmod(private, 0700) != nil {
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
		t.Fatal("source worker binary unavailable")
	}
	var schema string
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
			t.Error("owned supplier worker schema was not removed")
		}
	})
	db, handler := testServer(t)
	schema = db.Pool.Config().ConnConfig.RuntimeParams["search_path"]
	scopedDSN, err := g09SourceWorkerScopedDSN(rawDSN, schema)
	if err != nil {
		t.Fatal("worker DSN missing exact isolated schema")
	}
	var actualSchema string
	if db.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&actualSchema) != nil || actualSchema != schema {
		t.Fatal("API store is outside isolated schema")
	}
	uid, customer := user(t, db, handler, "g09-supplier-customer", "customer")
	staffID, staff := user(t, db, handler, "g09-supplier-staff", "super_admin")
	// Fixture identities remain unverified; disable initial notification channels
	// and templates, so this supplier-only cohort has no mail/SMS destinations.
	if _, err = db.Pool.Exec(t.Context(), `UPDATE channel_instances SET enabled=false WHERE category IN ('mail','sms'); UPDATE notification_templates SET active=false`); err != nil {
		t.Fatal("supplier-only channel setup failed")
	}
	journalPath := filepath.Join(private, "supplier.private.jsonl")
	journal, err := g09fixture.NewJournal(journalPath)
	if err != nil {
		t.Fatal("private supplier journal unavailable")
	}
	t.Cleanup(func() {
		if journal.Close() != nil {
			t.Error("supplier journal close failed")
		}
	})
	apiServer := g09SourceLoopbackServer(t, handler)
	transport := &http.Transport{Proxy: nil}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(client.CloseIdleConnections)
	call := func(method, path, cookie string, body any, key string, status int) map[string]any {
		t.Helper()
		return g09SourceHTTP(t, client, apiServer.URL, method, path, cookie, body, key, status)
	}
	for cookie, id := range map[string]int64{customer: uid, staff: staffID} {
		if g09SourceID(t, call("GET", "/api/v1/me", cookie, nil, "", 200), "id") != id {
			t.Fatal("source session identity mismatch")
		}
	}
	const username = "g09-synthetic-user"
	const password = "g09-private-synthetic-password"
	const bearer = "g09-private-synthetic-bearer"
	config := g09fixture.SupplierConfig{Username: username, Password: password, Token: bearer, DownstreamURL: apiServer.URL, RequestBudget: 24, Resources: []g09fixture.SupplierResource{{LocalID: 1, HostID: 42, InvoiceID: 90}}, DownstreamTokens: map[int64]string{1: strings.Repeat("x", 32)}}
	metadata, err := g09fixture.NewSupplierSink(journal, config)
	if err != nil {
		t.Fatal("metadata sink unavailable")
	}
	gate := &g09SupplierWorkerGate{sink: metadata}
	supplierServer := g09SourceLoopbackServer(t, gate)
	account := call("POST", "/api/v1/admin/suppliers", staff, store.SupplierInput{Name: "G09 loopback", Provider: supplier.ProviderZJMF, BaseURL: supplierServer.URL, Username: username, Password: password, Active: enabled()}, "", 201)
	supplierID := g09SourceID(t, account, "id")
	// The catalog route returns an array, unlike g09SourceHTTP's object envelope.
	catalogRequest, err := http.NewRequestWithContext(t.Context(), "POST", apiServer.URL+fmt.Sprintf("/api/v1/admin/suppliers/%d/catalog", supplierID), strings.NewReader("{}"))
	if err != nil {
		t.Fatal("catalog request unavailable")
	}
	catalogRequest.Header.Set("Content-Type", "application/json")
	catalogRequest.Header.Set("Origin", "http://localhost:3088")
	catalogRequest.Header.Set("X-Requested-With", "zjmfgo")
	catalogRequest.Header.Set("Cookie", staff)
	catalogResponse, err := client.Do(catalogRequest)
	if err != nil {
		t.Fatal("catalog source request failed")
	}
	var catalog struct {
		Data []supplier.Product `json:"data"`
	}
	decodeErr := json.NewDecoder(http.MaxBytesReader(nil, catalogResponse.Body, 1<<20)).Decode(&catalog)
	catalogResponse.Body.Close()
	if catalogResponse.StatusCode != 200 || decodeErr != nil || len(catalog.Data) != 1 || catalog.Data[0].ID != 7 {
		t.Fatal("source catalog identity mismatch")
	}
	preview := call("GET", fmt.Sprintf("/api/v1/admin/suppliers/%d/products/7", supplierID), staff, nil, "", 200)
	revision, ok := preview["revision"].(string)
	if !ok || revision == "" || preview["supported"] != true {
		t.Fatal("supplier product preview unsupported")
	}
	stock := 10
	imported := call("POST", fmt.Sprintf("/api/v1/admin/suppliers/%d/import", supplierID), staff, store.SupplierImportInput{UpstreamProductID: 7, ExpectedSupplierVersion: 1, ExpectedUpstreamRevision: revision, Name: "G09 supplier VM", Slug: "g09-supplier-vm", BillingCycle: "monthly", PriceCents: 1000, Stock: &stock, MaximumCostCents: 100}, "", 201)
	pid := g09SourceID(t, imported, "id")
	publication := productInput("g09-supplier-vm", nil)
	publication.Name = "G09 supplier VM"
	publication.ProvisionMode = "supplier"
	publication.ExpectedRevision = 1
	publication.PriceCents = 1000
	publication.SetupFeeCents = 0
	publication.Stock = &stock
	publication.ExpectedStock = &stock
	call("PATCH", fmt.Sprintf("/api/v1/admin/products/%d", pid), staff, publication, "", 200)
	binding := call("GET", fmt.Sprintf("/api/v1/admin/products/%d/supplier-binding", pid), staff, nil, "", 200)
	bindingID := g09SourceID(t, binding, "id")
	if g09SourceID(t, binding, "supplier_id") != supplierID || g09SourceID(t, binding, "upstream_product_id") != 7 {
		t.Fatal("imported binding identity mismatch")
	}
	runID := "g09-supplier-" + strings.TrimPrefix(schema, "zjmfgo_test_")
	call("POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", uid), staff, map[string]any{"amount_cents": 10000}, runID+"-initial-credit", 201)
	cart := call("GET", "/api/v1/cart", customer, nil, "", 200)
	call("POST", "/api/v1/cart/items", customer, map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "billing_cycle": "monthly", "option_ids": []int64{}, "option_quantities": map[string]int{}, "purchase_fields": map[string]string{}}, runID+"-cart", 200)
	cart = call("GET", "/api/v1/cart", customer, nil, "", 200)
	items, ok := cart["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatal("source cart must contain exactly one item")
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatal("invalid source cart item")
	}
	order := call("POST", "/api/v1/orders", customer, map[string]any{"cart_version": cart["version"], "item_ids": []int64{g09SourceID(t, item, "id")}}, runID+"-order", 201)
	orderID, invoiceID := g09SourceID(t, order, "order_id"), g09SourceID(t, order, "invoice_id")
	invoice := call("GET", fmt.Sprintf("/api/v1/invoices/%d", invoiceID), customer, nil, "", 200)
	if g09SourceID(t, invoice, "user_id") != uid || invoice["status"] != "unpaid" || invoice["amount_cents"] != float64(1000) {
		t.Fatal("source invoice ownership/amount/status mismatch")
	}
	call("POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), customer, map[string]any{"expected_version": g09SourceID(t, invoice, "version")}, runID+"-pay", 201)
	var serviceID, createID int64
	if db.Pool.QueryRow(t.Context(), `SELECT s.id,j.id FROM services s JOIN order_items oi ON oi.id=s.order_item_id JOIN provision_jobs j ON j.service_id=s.id AND j.action='create' WHERE oi.order_id=$1`, orderID).Scan(&serviceID, &createID) != nil {
		t.Fatal("real purchase source-to-job mapping missing")
	}
	g09AssertSQL(t, db, `SELECT j.status='pending' AND j.attempts=0 AND j.locked_at IS NULL AND j.external_started_at IS NULL AND s.status='queued' AND s.provision_mode='supplier' AND s.upstream_id='' AND NOT s.auto_renew AND (s.delivery_snapshot->>'supplier_id')::bigint=$3 AND (s.delivery_snapshot->>'binding_id')::bigint=$4 AND (SELECT count(*) FROM provision_jobs)=1 AND (SELECT count(*) FROM email_outbox)=0 AND (SELECT count(*) FROM sms_outbox)=0 FROM services s JOIN provision_jobs j ON j.service_id=s.id WHERE s.id=$1 AND j.id=$2`, serviceID, createID, supplierID, bindingID)
	metadataRecords := journal.Snapshot()
	if metadata.Err() != nil || len(metadataRecords) == 0 || len(metadata.Snapshot()) != 0 {
		t.Fatal("metadata phase created a resource or lacked durable reads")
	}
	for _, record := range metadataRecords {
		if record.RecipientHash != g09SourceHash("0") {
			t.Fatal("metadata phase admitted an entity request")
		}
	}
	config.RequestBudget = 48
	config.Resources = []g09fixture.SupplierResource{{LocalID: serviceID, HostID: 42, InvoiceID: 90}}
	config.DownstreamTokens = map[int64]string{serviceID: g09SupplierWorkerToken(db.ProvisionEncryptionKey, serviceID)}
	sink, err := g09fixture.NewSupplierSink(journal, config)
	if err != nil || !gate.freeze(sink) {
		t.Fatal("supplier source freeze failed")
	}
	t.Logf("phase frozen: real supplier/import/checkout/pay source service=%d create=%d; %d durable metadata acceptances, zero admitted preparation resources; worker not started", serviceID, createID, len(metadataRecords))
	if g09fixture.WritePrivateJSON(filepath.Join(private, "frozen-supplier-sources.private.json"), map[string]any{"mode": "source_api_real_cmd_worker_supplier", "customerId": uid, "supplierId": supplierID, "bindingId": bindingID, "productId": pid, "orderId": orderID, "invoiceId": invoiceID, "serviceId": serviceID, "createJobId": createID, "upstreamHostId": 42, "upstreamInvoiceId": 90, "metadataAcceptances": len(metadataRecords), "resourceFrozenBeforeWorker": true}) != nil {
		t.Fatal("source freeze evidence unavailable")
	}
	workerStarted := time.Now().UTC()
	worker := g09StartSourceWorkerOrigin(t, binary, private, scopedDSN, db.ProvisionEncryptionKey, 0, apiServer.URL)
	worker.waitReady(t)
	wait := func(jobID int64) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			worker.assertRunning(t)
			var state string
			if db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", jobID).Scan(&state) != nil {
				t.Fatal("explicit supplier job unavailable")
			}
			if state == "succeeded" {
				return
			}
			if state == "failed" || state == "waiting_verification" || time.Now().After(deadline) || sink.Err() != nil || gate.err() != nil {
				t.Fatal("bounded supplier cohort did not complete; not a supplier capacity result")
			}
			select {
			case <-t.Context().Done():
				t.Fatal("supplier smoke cancelled")
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	wait(createID)
	createEnd := len(journal.Snapshot())
	// Normal 2-second scheduler stays enabled. A queued API sync excludes this
	// service from its periodic candidates; no scheduler/job clock is rewritten.
	syncResult := call("POST", fmt.Sprintf("/api/v1/admin/services/%d/actions", serviceID), staff, map[string]any{"action": "sync"}, runID+"-sync", 202)
	syncID := g09SourceID(t, syncResult, "job_id")
	if syncID == createID {
		t.Fatal("sync did not produce a distinct source task")
	}
	wait(syncID)
	worker.stop(t)
	workerEnded := time.Now().UTC()
	if !worker.cleanTERM {
		t.Fatal("supplier worker did not complete clean TERM/Wait")
	}
	var backgrounds int
	if db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE id NOT IN ($1,$2)", createID, syncID).Scan(&backgrounds) != nil {
		t.Fatal("background classification query failed")
	}
	if backgrounds != 0 {
		t.Fatal("normal scheduler admitted extra background tasks: this finite fixture did not prove its two-task budget; not a production defect")
	}
	service := call("GET", fmt.Sprintf("/api/v1/services/%d", serviceID), customer, nil, "", 200)
	if g09SourceID(t, service, "id") != serviceID || service["status"] != "active" || service["provision_mode"] != "supplier" || service["upstream_id"] != "42" {
		t.Fatal("source API does not reflect actual worker supplier activation")
	}
	g09AssertSQL(t, db, `SELECT count(*)=2 AND bool_and(status='succeeded' AND attempts=1 AND locked_at IS NULL AND completed_at IS NOT NULL AND external_started_at IS NOT NULL AND last_error='') FROM provision_jobs WHERE service_id=$1 AND id IN ($2,$3)`, serviceID, createID, syncID)
	g09AssertSQL(t, db, `SELECT (j.result->>'upstream_id')::bigint=42 AND (j.result->>'invoice_id')::bigint=90 AND (j.result->>'cost_cents')::bigint=100 AND j.result->>'currency'='CNY' AND s.upstream_id='42' AND s.status='active' AND s.domain=$3 AND s.next_due_at=to_timestamp(1795000000) AND NOT s.inventory_released AND NOT s.auto_renew AND a.running_job_id IS NULL AND a.active AND octet_length(a.credentials_encrypted)>0 AND (SELECT count(*) FROM audit_events WHERE entity_type='service' AND entity_id=s.id::text AND action='supplier.delivery.succeeded' AND (payload->>'job_id')::bigint IN ($2,$5) AND (payload->>'supplier_id')::bigint=a.id)=2 FROM services s JOIN provision_jobs j ON j.service_id=s.id CROSS JOIN supplier_accounts a WHERE s.id=$1 AND j.id=$2 AND a.id=$4`, serviceID, createID, fmt.Sprintf("g09-%d.test", serviceID), supplierID, syncID)
	g09AssertSQL(t, db, `SELECT i.status='paid' AND i.amount_cents=1000 AND i.paid_at IS NOT NULL AND i.user_id=$2 AND o.status='paid' AND o.total_cents=1000 AND o.review_status='approved' AND (SELECT count(*) FROM balance_ledger WHERE user_id=$2 AND reference_type='invoice' AND reference_id=i.id AND entry_type='payment' AND delta_cents=-1000)=1 FROM invoices i JOIN orders o ON o.id=i.order_id WHERE i.id=$1 AND o.id=$3`, invoiceID, uid, orderID)
	g09AssertSQL(t, db, `SELECT u.balance_cents=9000 AND u.balance_cents=(SELECT sum(delta_cents) FROM balance_ledger WHERE user_id=u.id) AND (SELECT count(*) FROM balance_ledger WHERE user_id=u.id)=2 AND NOT EXISTS(SELECT 1 FROM customer_credit_accounts WHERE user_id=u.id AND (limit_cents<>0 OR used_cents<>0)) AND NOT EXISTS(SELECT 1 FROM payment_transactions WHERE user_id=u.id) AND p.stock=9 AND (SELECT count(*) FROM email_outbox)=0 AND (SELECT count(*) FROM sms_outbox)=0 FROM users u CROSS JOIN products p WHERE u.id=$1 AND p.id=$2`, uid, pid)
	facts := sink.Snapshot()
	if sink.Err() != nil || gate.err() != nil || len(facts) != 1 || facts[0].LocalID != serviceID || facts[0].HostID != 42 || facts[0].InvoiceID != 90 || !facts[0].Paid {
		t.Fatal("supplier identity/payment facts are not conserved")
	}
	records := journal.Snapshot()
	g09VerifySupplierWorkerJournal(t, journalPath, records, metadataRecords, createEnd, serviceID, workerStarted, workerEnded)
	if g09fixture.WritePrivateJSON(filepath.Join(private, "supplier-worker-result.private.json"), map[string]any{"schemaVersion": 1, "mode": "source_api_real_cmd_worker_supplier", "candidateVerified": false, "capacityVerified": false, "workerBinarySHA256": g09SourceHash(string(binaryBytes)), "workerPID": worker.cmd.Process.Pid, "workerExitedViaTERM": true, "supplierId": supplierID, "bindingId": bindingID, "serviceId": serviceID, "createJobId": createID, "syncJobId": syncID, "createProtocolSequenceStart": len(metadataRecords) + 1, "createProtocolSequenceEnd": createEnd, "syncProtocolSequenceStart": createEnd + 1, "syncProtocolSequenceEnd": len(records), "backgroundJobs": backgrounds, "protocolHashesVerified": true}) != nil {
		t.Fatal("private supplier worker result unavailable")
	}
	t.Log("PASS: one real HTTP import/purchase -> real cmd/worker create + API sync -> loopback SupplierSink; source identities, accounting/stock, durable entity protocol hashes and TERM/Wait agree; candidate/600-task/capacity/real supplier unverified")
}

// Preparation uses the real compiled adapter against a bounded metadata-only
// sink with an unusable placeholder identity. Freeze waits for all preparation
// requests to finish and switches exactly once; unfrozen create is forbidden.
type g09SupplierWorkerGate struct {
	mu     sync.Mutex
	sink   *g09fixture.SupplierSink
	frozen bool
	failed error
}

func (g *g09SupplierWorkerGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.frozen && !((r.Method == "POST" && r.URL.Path == "/zjmf_api_login") || (r.Method == "GET" && (r.URL.Path == "/cart/all" || r.URL.Path == "/cart/get_product_config" || r.URL.Path == "/user_info"))) {
		g.failed = fmt.Errorf("unfrozen supplier mutation rejected")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if g.failed != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	g.sink.ServeHTTP(w, r)
}
func (g *g09SupplierWorkerGate) freeze(sink *g09fixture.SupplierSink) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if sink == nil || g.frozen || g.failed != nil {
		return false
	}
	g.sink, g.frozen = sink, true
	return true
}
func (g *g09SupplierWorkerGate) err() error { g.mu.Lock(); defer g.mu.Unlock(); return g.failed }

// Independently derived from the production callback token contract; raw
// tokens never enter logs, public artifacts or the durable protocol journal.
func g09SupplierWorkerToken(key []byte, id int64) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("supplier-service:" + strconv.FormatInt(id, 10)))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func g09VerifySupplierWorkerJournal(t *testing.T, path string, records, metadata []g09fixture.Acceptance, createEnd int, id int64, start, end time.Time) {
	t.Helper()
	if len(records) <= createEnd || createEnd <= len(metadata) || !reflect.DeepEqual(records[:len(metadata)], metadata) {
		t.Fatal("supplier journal phase boundaries invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal("durable supplier journal unavailable")
	}
	var durable []g09fixture.Acceptance
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record g09fixture.Acceptance
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			file.Close()
			t.Fatal("invalid durable supplier record")
		}
		durable = append(durable, record)
	}
	scanErr, closeErr := scanner.Err(), file.Close()
	if scanErr != nil || closeErr != nil || !reflect.DeepEqual(durable, records) {
		t.Fatal("supplier journal disk/snapshot mismatch")
	}
	ids := map[string]any{"hostid": []int64{42}, "invoiceid": int64(90)}
	response := func(status int) map[string]any { return map[string]any{"status": status, "data": ids} }
	invoice := func(status string) any {
		return map[string]any{"status": 200, "data": map[string]any{"detail": map[string]any{"id": 90, "subtotal": "1.00", "status": status}, "currency": map[string]any{"code": "CNY"}, "invoice_items": []any{map[string]any{"type": "host", "rel_id": 42, "amount": "1.00"}}}}
	}
	host := func(status string) any {
		return map[string]any{"status": 200, "data": map[string]any{"host_data": map[string]any{"id": 42, "productid": 7, "domainstatus": status, "domain": fmt.Sprintf("g09-%d.test", id), "nextduedate": 1795000000, "assignedips": []any{}}}}
	}
	expected := []struct {
		channel string
		body    any
		sync    bool
	}{
		{"supplier.cart/clear", map[string]any{"status": 200}, false},
		{"supplier.cart/add_to_shop", map[string]any{"status": 200}, false},
		{"supplier.cart/settle", response(200), false},
		{"supplier.get_invoices_detail", invoice("Unpaid"), false},
		{"supplier.host/header", host("Pending"), false},
		{"supplier.apply_credit", response(1001), false},
		{"supplier.get_invoices_detail", invoice("Paid"), false},
		{"supplier.host/header", host("Active"), false},
		{"supplier.host/header", host("Active"), true},
	}
	matched := 0
	for i, record := range records {
		if record.Sequence != uint64(i+1) {
			t.Fatal("supplier durable sequence invalid")
		}
		if i < len(metadata) {
			continue
		}
		if record.AcceptedAt.Before(start) || record.AcceptedAt.After(end) {
			t.Fatal("supplier protocol outside worker interval")
		}
		if record.RecipientHash == g09SourceHash("0") {
			switch record.Channel {
			case "supplier.login", "supplier.cart/get_product_config", "supplier.user_info":
			default:
				t.Fatal("unexpected worker metadata operation")
			}
			continue
		}
		if matched >= len(expected) || record.RecipientHash != g09SourceHash(strconv.FormatInt(id, 10)) {
			t.Fatal("unexpected or cross-service supplier acceptance")
		}
		want := expected[matched]
		raw, err := json.Marshal(want.body)
		if err != nil || record.Channel != want.channel || record.MessageHash != g09SourceHash(string(raw)) || (i >= createEnd) != want.sync {
			t.Fatal("source job-to-entity durable protocol response hash mismatch")
		}
		matched++
	}
	if matched != len(expected) {
		t.Fatal("supplier create/sync protocol incomplete or duplicated")
	}
}

// No DSN: exercise the new journal oracle and one-time gate against the actual
// compiled adapter over sockets, so a skipped PG test cannot mask a bad oracle.
func TestG09SupplierWorkerProtocolGuards(t *testing.T) {
	t.Run("compiled adapter and durable oracle", func(t *testing.T) {
		private := t.TempDir()
		if os.Chmod(private, 0700) != nil {
			t.Fatal("private directory unavailable")
		}
		path := filepath.Join(private, "supplier.private.jsonl")
		journal, err := g09fixture.NewJournal(path)
		if err != nil {
			t.Fatal("private journal unavailable")
		}
		t.Cleanup(func() { journal.Close() })
		key := []byte("01234567890123456789012345678901")
		config := g09fixture.SupplierConfig{Username: "g09-synthetic-user", Password: "g09-private-synthetic-password", Token: "g09-private-synthetic-bearer", DownstreamURL: "http://127.0.0.1:29000", Resources: []g09fixture.SupplierResource{{LocalID: 99, HostID: 42, InvoiceID: 90}}, DownstreamTokens: map[int64]string{99: g09SupplierWorkerToken(key, 99)}, RequestBudget: 48}
		initial, err := g09fixture.NewSupplierSink(journal, config)
		if err != nil {
			t.Fatal("metadata sink unavailable")
		}
		gate := &g09SupplierWorkerGate{sink: initial}
		server := g09SourceLoopbackServer(t, gate)
		adapter, err := supplier.New(supplier.Account{Provider: supplier.ProviderZJMF, BaseURL: server.URL, Username: config.Username, Password: config.Password})
		if err != nil {
			t.Fatal("compiled adapter unavailable")
		}
		preview, err := adapter.Product(t.Context(), 7)
		if err != nil || !preview.Supported {
			t.Fatal("compiled product preview failed")
		}
		metadata := journal.Snapshot()
		frozen, err := g09fixture.NewSupplierSink(journal, config)
		if err != nil || !gate.freeze(frozen) || gate.freeze(initial) {
			t.Fatal("gate did not freeze exactly once")
		}
		start := time.Now().UTC()
		result, err := adapter.Create(t.Context(), supplier.CreateRequest{ProductID: 7, Cycle: "monthly", Revision: preview.Revision, LocalServiceID: 99, Host: "svc-99", DownstreamURL: config.DownstreamURL, DownstreamToken: g09SupplierWorkerToken(key, 99), MaximumCostCents: 100, Currency: "CNY"})
		if err != nil || result.UpstreamID != 42 || result.InvoiceID != 90 || result.Status != "active" {
			t.Fatal("compiled create failed")
		}
		createEnd := len(journal.Snapshot())
		if _, err = adapter.Sync(t.Context(), 42); err != nil {
			t.Fatal("compiled sync failed")
		}
		if frozen.Err() != nil || gate.err() != nil {
			t.Fatal("bounded supplier failed")
		}
		g09VerifySupplierWorkerJournal(t, path, journal.Snapshot(), metadata, createEnd, 99, start, time.Now().UTC())
	})
	t.Run("unfrozen write is rejected and latched", func(t *testing.T) {
		// A nil inner sink deliberately proves forbidden traffic never dispatches.
		gate := &g09SupplierWorkerGate{}
		server := g09SourceLoopbackServer(t, gate)
		resp, err := server.Client().Post(server.URL+"/cart/settle", "application/x-www-form-urlencoded", strings.NewReader(""))
		if err != nil {
			t.Fatal("loopback request unavailable")
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || gate.err() == nil || gate.freeze(&g09fixture.SupplierSink{}) {
			t.Fatal("unfrozen mutation was not rejected permanently")
		}
	})
}
