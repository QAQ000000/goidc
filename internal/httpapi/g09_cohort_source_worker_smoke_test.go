package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/g09fixture"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// This source Handler test is an explicit 600-source prerequisite, not the
// candidate/namespace/27-minute capacity run. No worker exists during source.
func TestG09CohortSourceWorkerSmoke(t *testing.T) {
	if os.Getenv("ZJMFGO_G09_COHORT_SMOKE") != "1" {
		t.Skip("opt-in exact 600 source API + two real workers; no candidate capacity proof")
	}
	raw := os.Getenv("ZJMFGO_TEST_DATABASE_URL")
	parsed, err := g09SourceWorkerDBConfig(raw)
	if err != nil {
		t.Fatal("cohort requires explicit loopback URL _test DSN without options")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	parsed.MaxConns = 1
	parsed.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, parsed)
	if err != nil {
		cancel()
		t.Fatal("cohort PG preflight unavailable")
	}
	var version int
	var database string
	err = pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int,current_database()").Scan(&version, &database)
	pool.Close()
	cancel()
	if err != nil || version < 180000 || version >= 190000 || database != parsed.ConnConfig.Database {
		t.Fatal("cohort requires PostgreSQL 18")
	}
	// An independent disposable schema proves actual recipient cardinality first.
	if !t.Run("independent ticket pilot", func(t *testing.T) {
		p := g09CohortPrepare(t, 150, false)
		r := p.ticket(t, 0)
		p.verifyTicket(t, r, true)
		g09CohortAssertSQL(t, p.db, `SELECT (SELECT count(*) FROM email_outbox)=1 AND (SELECT count(*) FROM sms_outbox)=1 AND (SELECT count(*) FROM provision_jobs)=0`)
	}) {
		t.Fatal("ticket pilot failed; formal source run forbidden")
	}
	e := g09CohortPrepare(t, 150, true)
	binary := filepath.Join(e.private, "worker")
	build := exec.CommandContext(t.Context(), "./scripts/go.sh", "build", "-o", binary, "./cmd/worker")
	build.Dir = "../.."
	if _, err = build.CombinedOutput(); err != nil {
		t.Fatal("cohort source worker build failed")
	}
	binaryBytes, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal("worker hash unavailable")
	}
	scoped, err := g09SourceWorkerScopedDSN(raw, e.schema)
	if err != nil {
		t.Fatal("cohort isolated worker DSN unavailable")
	}
	// All identity/catalog/preferences/channel/balance setup finished above.
	for _, u := range e.identities {
		for cookie, id := range map[string]int64{u.customer: u.customerID, u.staff: u.staffID} {
			if got := e.call(t, "GET", "/api/v1/me", cookie, nil, "", 200); g09SourceID(t, got, "id") != id {
				t.Fatal("formal source session identity mismatch")
			}
		}
	}
	g09CohortAssertSQL(t, e.db, `SELECT count(*)=301 AND bool_and(expires_at>now()+interval '15 minutes') FROM sessions`)
	// Sequential source is deliberately bounded (one HTTP request at a time).
	started := time.Now().UTC()
	sourceCtx, sourceCancel := context.WithDeadline(t.Context(), started.Add(120*time.Second))
	defer sourceCancel()
	e.ctx = sourceCtx
	records := make([]g09CohortSource, 0, 450)
	for i := 0; i < 300; i++ {
		productID, kind, account, binding := e.localProduct, "simulation", int64(0), int64(0)
		if i >= 270 {
			s := e.suppliers[(i-270)/3]
			productID, kind, account, binding = s.productID, "supplier", s.accountID, s.bindingID
		}
		// Each customer has at most two purchases, and owns its cart throughout.
		records = append(records, e.purchase(t, i%150, i, productID, kind, account, binding))
	}
	for i := 0; i < 150; i++ {
		records = append(records, e.ticket(t, i))
	}
	acceptedAt := time.Now().UTC()
	if acceptedAt.After(started.Add(120 * time.Second)) {
		t.Fatal("exact 600-source preparation exceeded fixed 120 seconds")
	}
	sourceCancel()
	e.ctx = t.Context()
	e.seal(t, records, started, acceptedAt)
	e.initVerifier(t, records)
	e.drain(t, binary, scoped, records, g09SourceHash(string(binaryBytes)), started, acceptedAt)
}

// Fields are source facts, never caller-supplied business IDs. Sensitive rendered
// bodies/addresses remain only in the private delivery oracle and mutation file.
type g09CohortSource struct {
	FlowID, Kind                                               string
	CustomerID, ProductID, SupplierAccountID, BindingID        int64
	OrderID, InvoiceID, OrderItemID, ServiceID, ProvisionJobID int64
	TicketID, EventID, StaffID, DepartmentID                   int64
	EmailDeliveryID, EmailOutboxID, SMSDeliveryID, SMSOutboxID int64
	AcceptedAtUTC                                              time.Time
}
type g09CohortIdentity struct {
	customerID, staffID, departmentID int64
	customer, staff, email, phone     string
}
type g09CohortSupplier struct {
	accountID, bindingID, productID int64
	config                          g09fixture.SupplierConfig
	gate                            *g09SupplierWorkerGate
	metadata, sink                  *g09fixture.SupplierSink
	journal                         *g09fixture.Journal
	path                            string
	prefix                          []g09fixture.Acceptance
}
type g09CohortDelivery struct {
	ID, EventID, OutboxID, RecipientID          int64
	Channel, Recipient, Subject, Body, Instance string
}
type g09CohortEnv struct {
	mutationSequence             int
	loginSequence                int
	ctx                          context.Context
	db                           *store.Store
	private, schema, run, origin string
	client                       *http.Client
	root                         string
	localProduct                 int64
	identities                   []g09CohortIdentity
	suppliers                    []*g09CohortSupplier
	notifications                *g09fixture.Journal
	notificationPath             string
	mutations                    *os.File
	oracles                      map[int64][]g09CohortDelivery
	verifier                     *g09fixture.CohortVerifier
	observer                     *g09fixture.PGObserver
	verificationSamples          []g09CohortVerificationEvidence
}

const g09CohortFrom = "g09-cohort-fixture@example.test"
const g09CohortMail = "g09-cohort-mail"
const g09CohortSMS = "g09-cohort-sms"

func g09CohortPrepare(t *testing.T, n int, withPurchases bool) *g09CohortEnv {
	t.Helper()
	private := t.TempDir()
	if os.Chmod(private, 0700) != nil {
		t.Fatal("private cohort directory unavailable")
	}
	var cleanupSchema string
	baseConfig, configErr := g09SourceWorkerDBConfig(os.Getenv("ZJMFGO_TEST_DATABASE_URL"))
	if configErr != nil {
		t.Fatal("schema cleanup configuration invalid")
	}
	t.Cleanup(func() {
		if cleanupSchema == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		base, err := pgxpool.NewWithConfig(ctx, baseConfig.Copy())
		if err != nil {
			t.Error("owned schema cleanup verification unavailable")
			return
		}
		defer base.Close()
		var removed bool
		if base.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname=$1)`, cleanupSchema).Scan(&removed) != nil || !removed {
			t.Error("owned cohort schema was not removed")
		}
	})
	db, handler := testServer(t)
	cleanupSchema = db.Pool.Config().ConnConfig.RuntimeParams["search_path"]
	e := &g09CohortEnv{ctx: t.Context(), db: db, private: private, schema: db.Pool.Config().ConnConfig.RuntimeParams["search_path"], oracles: map[int64][]g09CohortDelivery{}}
	e.run = "g09-cohort-" + strings.TrimPrefix(e.schema, "zjmfgo_test_")
	api := g09SourceLoopbackServer(t, handler)
	e.origin = api.URL
	e.client = &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(e.client.CloseIdleConnections)
	var err error
	e.mutations, err = os.OpenFile(filepath.Join(private, "mutations.private.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("private mutation ledger unavailable")
	}
	t.Cleanup(func() {
		if e.mutations.Close() != nil {
			t.Error("mutation ledger close failed")
		}
	})
	var staffRoleID int64
	hash, err := bcrypt.GenerateFromPassword([]byte("g09-cohort-private-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal("identity fixture hash unavailable")
	}
	seed := func(name, role string) (int64, string) {
		var id int64
		var assigned *int64
		if role == "admin" {
			assigned = &staffRoleID
		}
		if db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role,admin_role_id) VALUES($1,$2,$3,$1,$4,$5) RETURNING id`, name, name+"@example.test", string(hash), role, assigned).Scan(&id) != nil {
			t.Fatal("fixture identity seed failed")
		}
		if _, err = db.Pool.Exec(t.Context(), `INSERT INTO carts(user_id) VALUES($1)`, id); err != nil {
			t.Fatal("fixture cart seed failed")
		}
		return id, e.login(t, name, role)
	}
	rootID, root := seed("g09-cohort-root", "super_admin")
	e.root = root
	// A super_admin bypasses department scope. It is demoted to an empty
	// fixture-only role below after all administrative preparation has finished.
	if _, err = db.Pool.Exec(t.Context(), `UPDATE users SET ticket_scope='departments',ticket_unassigned=false,email_verified=false,phone_verified=false WHERE id=$1`, rootID); err != nil {
		t.Fatal("bootstrap recipient exclusion failed")
	}
	if _, err = db.Pool.Exec(t.Context(), `UPDATE channel_instances SET enabled=false WHERE category IN ('mail','sms'); UPDATE notification_templates SET active=false`); err != nil {
		t.Fatal("cohort notification isolation failed")
	}
	if _, err = db.Pool.Exec(t.Context(), `UPDATE notification_templates SET active=true,subject=$1,body=$2 WHERE kind='ticket_customer_created'`, e.run+" {{ticket_no}}", e.run+" {{ticket_no}}\n{{body}}\n{{ticket_url}}"); err != nil {
		t.Fatal("cohort ticket templates unavailable")
	}
	e.notificationPath = filepath.Join(private, "notifications.private.jsonl")
	e.notifications, err = g09fixture.NewJournal(e.notificationPath)
	if err != nil {
		t.Fatal("private notification journal unavailable")
	}
	t.Cleanup(func() {
		if e.notifications.Close() != nil {
			t.Error("notification journal close failed")
		}
	})
	smtp, err := g09fixture.StartSMTP(e.notifications)
	if err != nil {
		t.Fatal("cohort SMTP unavailable")
	}
	t.Cleanup(func() {
		if smtp.Close() != nil {
			t.Error("SMTP close failed")
		}
	})
	host, portText, err := net.SplitHostPort(smtp.Addr())
	if err != nil || host != "127.0.0.1" {
		t.Fatal("SMTP outside loopback")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal("SMTP port invalid")
	}
	token := "g09-cohort-private-loopback-token"
	sms, err := g09fixture.SMSHandler(e.notifications, token)
	if err != nil {
		t.Fatal("SMS fixture unavailable")
	}
	smsServer := g09SourceLoopbackServer(t, sms)
	for _, ch := range []map[string]any{
		{"id": g09CohortMail, "category": "mail", "implementation_id": "smtp", "display_name": "G09 cohort mail", "enabled": true, "secret": token, "config": map[string]any{"host": host, "port": port, "from": g09CohortFrom, "tls_mode": "none"}},
		{"id": g09CohortSMS, "category": "sms", "implementation_id": "generic_http_json", "display_name": "G09 cohort SMS", "enabled": true, "secret": token, "config": map[string]any{"endpoint": smsServer.URL}},
	} {
		e.call(t, "POST", "/api/v1/admin/channel-instances", root, ch, "", 201)
	}
	if db.Pool.QueryRow(t.Context(), `INSERT INTO admin_roles(name,permissions) VALUES('g09-cohort-ticket-role',ARRAY['tickets.view','tickets.manage']) RETURNING id`).Scan(&staffRoleID) != nil {
		t.Fatal("cohort staff role seed failed")
	}
	for i := 0; i < n; i++ {
		uid, customer := seed(fmt.Sprintf("g09-cohort-customer-%03d", i), "customer")
		sid, staff := seed(fmt.Sprintf("g09-cohort-staff-%03d", i), "admin")
		var department int64
		if db.Pool.QueryRow(t.Context(), `INSERT INTO ticket_departments(name,slug) VALUES($1,$2) RETURNING id`, fmt.Sprintf("G09 cohort %03d", i), fmt.Sprintf("g09-cohort-%03d", i)).Scan(&department) != nil {
			t.Fatal("cohort department seed failed")
		}
		phone := fmt.Sprintf("+999%09d", i+1)
		email := fmt.Sprintf("g09-cohort-staff-%03d@example.test", i)
		if _, err = db.Pool.Exec(t.Context(), `UPDATE users SET admin_role_id=$2,ticket_scope='departments',ticket_unassigned=false,email_verified=true,phone=$3 WHERE id=$1`, sid, staffRoleID, phone); err != nil {
			t.Fatal("scoped staff identity seed failed")
		}
		if _, err = db.Pool.Exec(t.Context(), `UPDATE users SET phone_verified=true WHERE id=$1`, sid); err != nil {
			t.Fatal("staff phone verification seed failed")
		}
		if _, err = db.Pool.Exec(t.Context(), `INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)`, sid, department); err != nil {
			t.Fatal("staff department scope seed failed")
		}
		p := e.call(t, "GET", "/api/v1/me/ticket-notification-preferences", staff, nil, "", 200)
		e.call(t, "PATCH", "/api/v1/me/ticket-notification-preferences", staff, map[string]any{"email_enabled": true, "sms_enabled": true, "expected_version": p["version"]}, "", 200)
		e.identities = append(e.identities, g09CohortIdentity{uid, sid, department, customer, staff, email, phone})
		if withPurchases {
			e.call(t, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", uid), root, map[string]any{"amount_cents": 10000}, e.run+fmt.Sprintf("-balance-%03d", i), 201)
		}
	}
	if withPurchases {
		e.localProduct = product(t, db, 270)
		if _, err = db.Pool.Exec(t.Context(), `UPDATE products SET price_cents=1000,setup_fee_cents=0 WHERE id=$1`, e.localProduct); err != nil {
			t.Fatal("simulation catalog seed failed")
		}
		if _, err = db.Pool.Exec(t.Context(), `UPDATE product_prices SET price_cents=1000,setup_fee_cents=0 WHERE product_id=$1 AND billing_cycle='monthly'`, e.localProduct); err != nil {
			t.Fatal("simulation cycle seed failed")
		}
		for i := 0; i < 10; i++ {
			e.suppliers = append(e.suppliers, e.prepareSupplier(t, i))
		}
	}
	g09CohortAssertSQL(t, db, `SELECT (SELECT count(*) FROM provision_jobs)=0 AND (SELECT count(*) FROM email_outbox)=0 AND (SELECT count(*) FROM sms_outbox)=0 AND (SELECT count(*) FROM customer_contacts)=0 AND (SELECT count(*) FROM channel_instances WHERE enabled AND category='mail')=1 AND (SELECT count(*) FROM channel_instances WHERE enabled AND category='sms')=1 AND (SELECT count(*) FROM notification_templates WHERE active)=2`)
	var disabledRole int64
	if db.Pool.QueryRow(t.Context(), `INSERT INTO admin_roles(name,permissions) VALUES('g09-bootstrap-no-ticket','{}') RETURNING id`).Scan(&disabledRole) != nil {
		t.Fatal("bootstrap role seed failed")
	}
	if _, err = db.Pool.Exec(t.Context(), `UPDATE users SET role='admin',admin_role_id=$2 WHERE id=$1`, rootID, disabledRole); err != nil {
		t.Fatal("bootstrap notification exclusion failed")
	}
	g09CohortAssertSQL(t, db, `SELECT count(*)=$1 AND bool_and(expires_at>now()+interval '15 minutes') FROM sessions`, 2*n+1)
	if withPurchases {
		e.captureBaseline(t)
	}
	return e
}

func (e *g09CohortEnv) login(t *testing.T, name, role string) string {
	t.Helper()
	field, identifier := "username", name
	if role == "customer" {
		field, identifier = "identifier", name+"@example.test"
	}
	raw, _ := json.Marshal(map[string]string{field: identifier, "password": "g09-cohort-private-password"})
	req, err := http.NewRequestWithContext(e.ctx, "POST", e.origin+"/api/v1/auth/login", bytes.NewReader(raw))
	if err != nil {
		t.Fatal("login request invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:3088")
	req.Header.Set("X-Requested-With", "zjmfgo")
	// Distinct loopback clients exercise real login rate limits as independent
	// identities; preparation login cost is outside the source window.
	e.loginSequence++
	dialer := &net.Dialer{Timeout: 5 * time.Second, LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 1, byte(e.loginSequence/250), byte(e.loginSequence%250+1))}}
	transport := &http.Transport{Proxy: nil, DialContext: dialer.DialContext}
	defer transport.CloseIdleConnections()
	loginClient := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	res, err := loginClient.Do(req)
	if err != nil {
		t.Fatal("real cohort login unavailable")
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	c := res.Cookies()
	if res.StatusCode != 200 || len(c) != 1 || !c[0].HttpOnly {
		t.Fatal("real cohort login failed")
	}
	return c[0].Name + "=" + c[0].Value
}

// Write the original mutation BEFORE sending. An unknown response terminates the
// run; no new key/version/payload is invented to repair a partially admitted chain.
func (e *g09CohortEnv) call(t *testing.T, method, path, cookie string, body any, key string, want int) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal("invalid cohort body")
	}
	if method != "GET" {
		e.mutationSequence++
		record, _ := json.Marshal(map[string]any{"phase": "request", "sequence": e.mutationSequence, "at": time.Now().UTC(), "method": method, "path": path, "key": key, "body": body, "bodySHA256": g09SourceHash(string(raw))})
		if _, err = e.mutations.Write(append(record, '\n')); err != nil || e.mutations.Sync() != nil {
			t.Fatal("original mutation could not be frozen")
		}
	}
	req, err := http.NewRequestWithContext(e.ctx, method, e.origin+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal("invalid cohort request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:3088")
	req.Header.Set("X-Requested-With", "zjmfgo")
	req.Header.Set("Cookie", cookie)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := e.client.Do(req)
	if err != nil {
		t.Fatal("cohort request failed or deadline elapsed; original mutation retained, no retry")
	}
	defer res.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if method != "GET" && err == nil && len(raw) <= 1<<20 {
		result, _ := json.Marshal(map[string]any{"phase": "response", "sequence": e.mutationSequence, "at": time.Now().UTC(), "status": res.StatusCode, "response": json.RawMessage(raw)})
		if len(result) == 0 {
			t.Fatal("mutation result is not JSON")
		}
		if _, writeErr := e.mutations.Write(append(result, '\n')); writeErr != nil || e.mutations.Sync() != nil {
			t.Fatal("mutation result could not be frozen")
		}
	}
	if err != nil || len(raw) > 1<<20 || res.StatusCode != want {
		t.Fatalf("cohort %s %s returned status %d, want %d; no retry", method, path, res.StatusCode, want)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Data == nil {
		t.Fatal("invalid cohort response")
	}
	return envelope.Data
}

func (e *g09CohortEnv) prepareSupplier(t *testing.T, i int) *g09CohortSupplier {
	t.Helper()
	s := &g09CohortSupplier{path: filepath.Join(e.private, fmt.Sprintf("supplier-%02d.private.jsonl", i))}
	var err error
	s.journal, err = g09fixture.NewJournal(s.path)
	if err != nil {
		t.Fatal("supplier journal unavailable")
	}
	t.Cleanup(func() {
		if s.journal.Close() != nil {
			t.Error("supplier journal close failed")
		}
	})
	s.config = g09fixture.SupplierConfig{Username: fmt.Sprintf("g09-private-supplier-%02d", i), Password: "g09-private-password", Token: fmt.Sprintf("g09-private-bearer-%02d", i), DownstreamURL: e.origin, RequestBudget: 96, Resources: []g09fixture.SupplierResource{{LocalID: 1, HostID: 42, InvoiceID: 90}}, DownstreamTokens: map[int64]string{1: strings.Repeat("x", 32)}}
	s.metadata, err = g09fixture.NewSupplierSink(s.journal, s.config)
	if err != nil {
		t.Fatal("supplier metadata sink unavailable")
	}
	s.gate = &g09SupplierWorkerGate{sink: s.metadata}
	server := g09SourceLoopbackServer(t, s.gate)
	a := e.call(t, "POST", "/api/v1/admin/suppliers", e.root, store.SupplierInput{Name: fmt.Sprintf("G09 cohort %02d", i), Provider: supplier.ProviderZJMF, BaseURL: server.URL, Username: s.config.Username, Password: s.config.Password, Active: enabled()}, "", 201)
	s.accountID = g09SourceID(t, a, "id")
	preview := e.call(t, "GET", fmt.Sprintf("/api/v1/admin/suppliers/%d/products/7", s.accountID), e.root, nil, "", 200)
	revision, ok := preview["revision"].(string)
	if !ok || revision == "" || preview["supported"] != true {
		t.Fatal("supplier preview unsupported")
	}
	stock := 3
	slug := fmt.Sprintf("g09-cohort-supplier-%02d", i)
	imported := e.call(t, "POST", fmt.Sprintf("/api/v1/admin/suppliers/%d/import", s.accountID), e.root, store.SupplierImportInput{UpstreamProductID: 7, ExpectedSupplierVersion: 1, ExpectedUpstreamRevision: revision, Name: slug, Slug: slug, BillingCycle: "monthly", PriceCents: 1000, Stock: &stock, MaximumCostCents: 100}, "", 201)
	s.productID = g09SourceID(t, imported, "id")
	publication := productInput(slug, nil)
	publication.Name = slug
	publication.ProvisionMode = "supplier"
	publication.ExpectedRevision = 1
	publication.PriceCents = 1000
	publication.SetupFeeCents = 0
	publication.Stock = &stock
	publication.ExpectedStock = &stock
	e.call(t, "PATCH", fmt.Sprintf("/api/v1/admin/products/%d", s.productID), e.root, publication, "", 200)
	binding := e.call(t, "GET", fmt.Sprintf("/api/v1/admin/products/%d/supplier-binding", s.productID), e.root, nil, "", 200)
	s.bindingID = g09SourceID(t, binding, "id")
	if g09SourceID(t, binding, "supplier_id") != s.accountID || g09SourceID(t, binding, "upstream_product_id") != 7 {
		t.Fatal("supplier import binding mismatch")
	}
	return s
}

func (e *g09CohortEnv) purchase(t *testing.T, customer, sequence int, pid int64, kind string, account, binding int64) g09CohortSource {
	t.Helper()
	u := e.identities[customer]
	key := e.run + fmt.Sprintf("-purchase-%03d", sequence)
	cart := e.call(t, "GET", "/api/v1/cart", u.customer, nil, "", 200)
	e.call(t, "POST", "/api/v1/cart/items", u.customer, map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "billing_cycle": "monthly", "option_ids": []int64{}, "option_quantities": map[string]int{}, "purchase_fields": map[string]string{}}, key+"-cart", 200)
	cart = e.call(t, "GET", "/api/v1/cart", u.customer, nil, "", 200)
	items, ok := cart["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatal("cohort cart does not contain exactly one item")
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatal("invalid cart item")
	}
	order := e.call(t, "POST", "/api/v1/orders", u.customer, map[string]any{"cart_version": cart["version"], "item_ids": []int64{g09SourceID(t, item, "id")}}, key+"-order", 201)
	r := g09CohortSource{FlowID: key, Kind: kind, CustomerID: u.customerID, ProductID: pid, SupplierAccountID: account, BindingID: binding, OrderID: g09SourceID(t, order, "order_id"), InvoiceID: g09SourceID(t, order, "invoice_id")}
	invoice := e.call(t, "GET", fmt.Sprintf("/api/v1/invoices/%d", r.InvoiceID), u.customer, nil, "", 200)
	if g09SourceID(t, invoice, "id") != r.InvoiceID || g09SourceID(t, invoice, "user_id") != u.customerID || invoice["status"] != "unpaid" || invoice["amount_cents"] != float64(1000) {
		t.Fatal("cohort invoice identity/amount/state mismatch")
	}
	e.call(t, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", r.InvoiceID), u.customer, map[string]any{"expected_version": g09SourceID(t, invoice, "version")}, key+"-pay", 201)
	r.AcceptedAtUTC = time.Now().UTC()
	if e.db.Pool.QueryRow(e.ctx, `SELECT oi.id,s.id,j.id FROM order_items oi JOIN services s ON s.order_item_id=oi.id JOIN provision_jobs j ON j.service_id=s.id AND j.action='create' WHERE oi.order_id=$1`, r.OrderID).Scan(&r.OrderItemID, &r.ServiceID, &r.ProvisionJobID) != nil {
		t.Fatal("cohort source-to-create mapping unavailable")
	}
	return r
}

func (e *g09CohortEnv) ticket(t *testing.T, i int) g09CohortSource {
	t.Helper()
	u := e.identities[i]
	key := e.run + fmt.Sprintf("-ticket-%03d", i)
	ticket := e.call(t, "POST", "/api/v1/tickets", u.customer, map[string]any{"subject": key, "body": "fixture public body", "priority": "normal", "department_id": u.departmentID, "idempotency_key": key}, key, 201)
	r := g09CohortSource{FlowID: key, Kind: "ticket", CustomerID: u.customerID, StaffID: u.staffID, DepartmentID: u.departmentID, TicketID: g09SourceID(t, ticket, "id"), AcceptedAtUTC: time.Now().UTC()}
	// Query ALL delivery decisions, including unexpected/skipped recipients.
	rows, err := e.db.Pool.Query(e.ctx, `SELECT d.id,e.id,coalesce(d.email_outbox_id,d.sms_outbox_id,0),d.recipient_id,d.channel,d.normalized_address,d.rendered_subject,d.rendered_body,coalesce(d.channel_instance_id,'') FROM ticket_notification_events e JOIN ticket_notification_deliveries d ON d.event_id=e.id WHERE e.ticket_id=$1 ORDER BY d.channel,d.id`, r.TicketID)
	if err != nil {
		t.Fatal("ticket delivery mapping unavailable")
	}
	var ds []g09CohortDelivery
	for rows.Next() {
		var d g09CohortDelivery
		if rows.Scan(&d.ID, &d.EventID, &d.OutboxID, &d.RecipientID, &d.Channel, &d.Recipient, &d.Subject, &d.Body, &d.Instance) != nil {
			rows.Close()
			t.Fatal("ticket delivery mapping invalid")
		}
		ds = append(ds, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ds) != 2 || ds[0].Channel != "email" || ds[1].Channel != "sms" || ds[0].Recipient != u.email || ds[1].Recipient != u.phone || ds[0].RecipientID != u.staffID || ds[1].RecipientID != u.staffID || ds[0].Instance != g09CohortMail || ds[1].Instance != g09CohortSMS || ds[0].EventID != ds[1].EventID || ds[0].OutboxID < 1 || ds[1].OutboxID < 1 {
		t.Fatal("ticket must produce exactly one unique scoped employee email and SMS")
	}
	r.EventID = ds[0].EventID
	r.EmailDeliveryID = ds[0].ID
	r.EmailOutboxID = ds[0].OutboxID
	r.SMSDeliveryID = ds[1].ID
	r.SMSOutboxID = ds[1].OutboxID
	e.oracles[r.TicketID] = ds
	e.verifyTicket(t, r, true)
	return r
}

func (e *g09CohortEnv) verifyTicket(t *testing.T, r g09CohortSource, pending bool) {
	t.Helper()
	ds := e.oracles[r.TicketID]
	if len(ds) != 2 {
		t.Fatal("ticket oracle cardinality is not two")
	}
	for _, d := range ds {
		if d.OutboxID < 1 || d.RecipientID != r.StaffID || d.Body == "" || strings.Contains(d.Body, "{{") || !strings.Contains(d.Body, e.run) {
			t.Fatal("ticket rendered source is not frozen")
		}
	}
	if pending {
		g09CohortAssertSQL(t, e.db, `SELECT bool_and(d.outcome='queued' AND d.preference_enabled AND d.address_verified) AND count(*)=2 AND (SELECT count(*) FROM email_outbox WHERE id=$2 AND status='pending' AND attempts=0)=1 AND (SELECT count(*) FROM sms_outbox WHERE id=$3 AND status='pending' AND attempts=0)=1 FROM ticket_notification_deliveries d WHERE d.event_id=$1`, r.EventID, r.EmailOutboxID, r.SMSOutboxID)
	}
}

func (e *g09CohortEnv) seal(t *testing.T, records []g09CohortSource, start, end time.Time) {
	t.Helper()
	if err := g09ValidateCohortSources(records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 450 {
		t.Fatalf("source flow count=%d, want 450", len(records))
	}
	var sim, sup, tickets int
	var serviceIDs, jobIDs, invoiceIDs, ticketIDs []int64
	for _, r := range records {
		switch r.Kind {
		case "simulation":
			sim++
		case "supplier":
			sup++
		case "ticket":
			tickets++
			ticketIDs = append(ticketIDs, r.TicketID)
		default:
			t.Fatal("unknown cohort source kind")
		}
		if r.Kind != "ticket" {
			serviceIDs = append(serviceIDs, r.ServiceID)
			jobIDs = append(jobIDs, r.ProvisionJobID)
			invoiceIDs = append(invoiceIDs, r.InvoiceID)
		}
	}
	if sim != 270 || sup != 30 || tickets != 150 || len(serviceIDs) != 300 || len(jobIDs) != 300 {
		t.Fatalf("cohort source cardinality sim=%d supplier=%d tickets=%d", sim, sup, tickets)
	}
	// Supplier gates are frozen only after all source IDs are known; preparation
	// metadata requests never share an entity resource with the real worker.
	// Product IDs are not contiguous by implementation contract; map by account.
	byAccount := map[int64][]g09fixture.SupplierResource{}
	for _, r := range records {
		if r.Kind == "supplier" {
			byAccount[r.SupplierAccountID] = append(byAccount[r.SupplierAccountID], g09fixture.SupplierResource{LocalID: r.ServiceID, HostID: 420000 + r.ServiceID, InvoiceID: 900000 + r.ServiceID})
		}
	}
	for _, s := range e.suppliers {
		resources := byAccount[s.accountID]
		if len(resources) != 3 {
			t.Fatal("supplier account does not have exactly three frozen services")
		}
		cfg := s.config
		cfg.Resources = resources
		cfg.DownstreamTokens = map[int64]string{}
		for _, r := range resources {
			cfg.DownstreamTokens[r.LocalID] = g09SupplierWorkerToken(e.db.ProvisionEncryptionKey, r.LocalID)
		}
		cfg.RequestBudget = 240
		sink, err := g09fixture.NewSupplierSink(s.journal, cfg)
		if err != nil || !s.gate.freeze(sink) {
			t.Fatal("supplier gate freeze failed")
		}
		s.sink = sink
		s.prefix = s.journal.Snapshot()
		if s.metadata.Err() != nil || len(s.metadata.Snapshot()) != 0 || len(s.prefix) == 0 {
			t.Fatal("supplier metadata phase not read-only")
		}
		for _, record := range s.prefix {
			if record.RecipientHash != g09SourceHash("0") {
				t.Fatal("supplier metadata phase admitted entity traffic")
			}
		}
	}
	if len(e.notifications.Snapshot()) != 0 {
		t.Fatal("worker protocol activity occurred before source seal")
	}
	if end.Sub(start) > 120*time.Second {
		t.Fatal("source window exceeded 120 seconds")
	}
	e.verifySources(t, records, true)
	g09CohortAssertSQL(t, e.db, `SELECT count(*)=300 AND bool_and(status='pending' AND attempts=0 AND locked_at IS NULL AND external_started_at IS NULL AND completed_at IS NULL) FROM provision_jobs`)
	g09CohortAssertSQL(t, e.db, `SELECT (SELECT count(*) FROM email_outbox WHERE status='pending' AND attempts=0 AND locked_at IS NULL)=150 AND (SELECT count(*) FROM sms_outbox WHERE status='pending' AND attempts=0 AND locked_at IS NULL AND octet_length(body_encrypted)>0)=150 AND (SELECT count(*) FROM channel_instances WHERE enabled AND category IN ('mail','sms') AND octet_length(secret_ciphertext)>0)=2`)
	// One immutable manifest is written before workers can claim a pending row.
	manifest := map[string]any{"schemaVersion": 1, "mode": "g09_600_source_prerequisite", "capacityVerified": false, "candidateVerified": false, "sourceWindowSeconds": end.Sub(start).Seconds(), "flows": records, "counts": map[string]int{"simulation": sim, "supplier": sup, "tickets": tickets, "provisionTasks": 300, "emailTasks": 150, "smsTasks": 150, "totalTasks": 600}, "serviceIds": serviceIDs, "jobIds": jobIDs, "invoiceIds": invoiceIDs, "ticketIds": ticketIDs}
	if err := g09fixture.WritePrivateJSON(filepath.Join(e.private, "delivery-oracles.private.json"), e.oracles); err != nil {
		t.Fatal("private delivery oracles could not be frozen")
	}
	if err := g09fixture.WritePrivateJSON(filepath.Join(e.private, "cohort-sealed.private.json"), manifest); err != nil {
		t.Fatal("cohort seal failed")
	}
}

// Enforce table-local uniqueness: equal IDs in different queue tables are legal.
func g09ValidateCohortSources(records []g09CohortSource) error {
	if len(records) != 450 {
		return fmt.Errorf("cohort requires exactly 450 source flows")
	}
	seen := map[string]map[int64]bool{}
	flows := map[string]bool{}
	kinds := map[string]int{}
	accounts := map[int64]int{}
	buyers := map[int64]int{}
	unique := func(table string, id int64) bool {
		if id < 1 {
			return false
		}
		if seen[table] == nil {
			seen[table] = map[int64]bool{}
		}
		if seen[table][id] {
			return false
		}
		seen[table][id] = true
		return true
	}
	for _, r := range records {
		if r.FlowID == "" || flows[r.FlowID] || r.CustomerID < 1 || r.AcceptedAtUTC.IsZero() {
			return fmt.Errorf("invalid or duplicate source flow")
		}
		flows[r.FlowID] = true
		kinds[r.Kind]++
		switch r.Kind {
		case "simulation", "supplier":
			buyers[r.CustomerID]++
			for k, id := range map[string]int64{"orders": r.OrderID, "invoices": r.InvoiceID, "order_items": r.OrderItemID, "services": r.ServiceID, "provision_jobs": r.ProvisionJobID} {
				if !unique(k, id) {
					return fmt.Errorf("missing or duplicate purchase identity")
				}
			}
			if r.ProductID < 1 || r.TicketID != 0 || r.EventID != 0 || r.EmailOutboxID != 0 || r.SMSOutboxID != 0 {
				return fmt.Errorf("cross-kind source record")
			}
			if r.Kind == "supplier" {
				if r.SupplierAccountID < 1 || r.BindingID < 1 {
					return fmt.Errorf("supplier mapping missing")
				}
				accounts[r.SupplierAccountID]++
			} else if r.SupplierAccountID != 0 || r.BindingID != 0 {
				return fmt.Errorf("simulation contains supplier mapping")
			}
		case "ticket":
			for k, id := range map[string]int64{"tickets": r.TicketID, "events": r.EventID, "staff": r.StaffID, "departments": r.DepartmentID, "deliveries": r.EmailDeliveryID, "sms_deliveries": r.SMSDeliveryID, "email_outbox": r.EmailOutboxID, "sms_outbox": r.SMSOutboxID, "ticket_customers": r.CustomerID} {
				if !unique(k, id) {
					return fmt.Errorf("missing or duplicate ticket identity")
				}
			}
			// Both delivery IDs belong to the same table.
			if !unique("deliveries", r.SMSDeliveryID) || r.ProvisionJobID != 0 || r.ServiceID != 0 || r.OrderID != 0 || r.InvoiceID != 0 {
				return fmt.Errorf("cross-kind ticket record")
			}
		default:
			return fmt.Errorf("unknown source kind")
		}
	}
	if kinds["simulation"] != 270 || kinds["supplier"] != 30 || kinds["ticket"] != 150 || len(accounts) != 10 {
		return fmt.Errorf("exact source mix not met")
	}
	if len(buyers) != 150 {
		return fmt.Errorf("purchase customer count not 150")
	}
	for id, n := range buyers {
		if n != 2 || !seen["ticket_customers"][id] {
			return fmt.Errorf("each customer requires two purchases and one ticket")
		}
	}
	for _, n := range accounts {
		if n != 3 {
			return fmt.Errorf("supplier account source budget not three")
		}
	}
	return nil
}

func (e *g09CohortEnv) verifySources(t *testing.T, records []g09CohortSource, pending bool) {
	t.Helper()
	jobState, serviceState, outboxState := "succeeded", "active", "sent"
	attempts := 1
	if pending {
		jobState, serviceState, outboxState = "pending", "queued", "pending"
		attempts = 0
	}
	for _, r := range records {
		if r.Kind == "ticket" {
			g09CohortAssertSQL(t, e.db, `SELECT t.user_id=$2 AND t.department_id=$3 AND (SELECT count(*) FROM ticket_mutation_receipts WHERE ticket_id=t.id AND actor_id=$2 AND operation='create')=1 AND (SELECT count(*) FROM ticket_notification_events WHERE ticket_id=t.id AND actor_id=$2 AND kind='customer_created')=1 FROM tickets t WHERE t.id=$1`, r.TicketID, r.CustomerID, r.DepartmentID)
			g09CohortAssertSQL(t, e.db, `SELECT count(*)=2 AND bool_and(d.recipient_id=$2 AND d.outcome='queued' AND d.reason='' AND d.address_verified AND d.preference_enabled AND d.event_id=$3) FROM ticket_notification_events e JOIN ticket_notification_deliveries d ON d.event_id=e.id WHERE e.ticket_id=$1`, r.TicketID, r.StaffID, r.EventID)
			ds := e.oracles[r.TicketID]
			var ticketNo string
			if e.db.Pool.QueryRow(t.Context(), `SELECT ticket_no FROM tickets WHERE id=$1`, r.TicketID).Scan(&ticketNo) != nil {
				t.Fatal("ticket number unavailable")
			}
			for _, d := range ds {
				if !strings.Contains(d.Body, ticketNo) || !strings.Contains(d.Body, e.run) || !strings.Contains(d.Body, "fixture public body") || !strings.Contains(d.Body, fmt.Sprintf("/admin/tickets/%d", r.TicketID)) || strings.Contains(d.Body, "{{") {
					t.Fatal("frozen notification body incomplete")
				}
			}
			g09CohortAssertSQL(t, e.db, `SELECT m.status=$3 AND m.attempts=$4 AND m.locked_at IS NULL AND m.kind='ticket_customer_created' AND m.channel_instance_id=$5 AND m.recipient=$6 AND m.subject=$7 AND m.body=$8 AND m.expires_at>now()+interval '6 days' AND s.status=$3 AND s.attempts=$4 AND s.locked_at IS NULL AND s.kind='ticket_customer_created' AND s.channel_instance_id=$9 AND s.recipient=$10 AND s.expires_at>now()+interval '6 days' FROM email_outbox m CROSS JOIN sms_outbox s WHERE m.id=$1 AND s.id=$2`, r.EmailOutboxID, r.SMSOutboxID, outboxState, attempts, g09CohortMail, ds[0].Recipient, ds[0].Subject, ds[0].Body, g09CohortSMS, ds[1].Recipient)
			if !pending {
				g09CohortAssertSQL(t, e.db, `SELECT s.sent_at IS NOT NULL AND s.last_error='' AND octet_length(s.body_encrypted)=0 AND (SELECT count(*) FROM sms_delivery_evidence d WHERE d.outbox_id=s.id AND d.attempts=1 AND d.provider='generic_http_json' AND d.outcome='accepted' AND d.http_status=200 AND NOT d.response_truncated AND octet_length(d.response_encrypted)>0 AND d.locked_at IS NOT NULL)=1 AND (SELECT sent_at IS NOT NULL AND last_error='' FROM email_outbox WHERE id=$2) FROM sms_outbox s WHERE s.id=$1`, r.SMSOutboxID, r.EmailOutboxID)
			}
			continue
		}
		g09CohortAssertSQL(t, e.db, `SELECT i.status='paid' AND i.user_id=$2 AND i.amount_cents=1000 AND i.paid_at IS NOT NULL AND i.order_id=$3 AND o.status='paid' AND o.review_status='approved' AND o.total_cents=1000 AND oi.id=$4 AND oi.product_id=$5 AND oi.quantity=1 AND s.id=$6 AND s.user_id=$2 AND s.product_id=$5 AND s.unit_number=1 AND s.provision_mode=$7 AND s.status=$8 AND NOT s.auto_renew AND NOT s.inventory_released AND j.id=$9 AND j.action='create' AND j.status=$10 AND j.attempts=$11 AND j.locked_at IS NULL AND (SELECT count(*) FROM balance_ledger l WHERE l.user_id=$2 AND l.reference_type='invoice' AND l.reference_id=i.id AND l.entry_type='payment' AND l.delta_cents=-1000)=1 AND (SELECT count(*) FROM order_items WHERE order_id=$3)=1 AND (SELECT count(*) FROM services WHERE order_item_id=$4)=1 AND (SELECT count(*) FROM provision_jobs WHERE service_id=$6 AND action='create')=1 FROM invoices i JOIN orders o ON o.id=i.order_id JOIN order_items oi ON oi.order_id=o.id JOIN services s ON s.order_item_id=oi.id JOIN provision_jobs j ON j.service_id=s.id AND j.id=$9 WHERE i.id=$1`, r.InvoiceID, r.CustomerID, r.OrderID, r.OrderItemID, r.ProductID, r.ServiceID, r.Kind, serviceState, r.ProvisionJobID, jobState, attempts)
		if r.Kind == "supplier" {
			g09CohortAssertSQL(t, e.db, `SELECT (delivery_snapshot->>'supplier_id')::bigint=$2 AND (delivery_snapshot->>'binding_id')::bigint=$3 FROM services WHERE id=$1`, r.ServiceID, r.SupplierAccountID, r.BindingID)
		}
		if !pending {
			if r.Kind == "simulation" {
				g09CohortAssertSQL(t, e.db, `SELECT s.upstream_id='local-'||s.id::text AND s.next_due_at>now() AND j.completed_at IS NOT NULL AND j.last_error='' AND j.result->>'mode'='simulation' AND (SELECT count(*) FROM audit_events WHERE entity_type='service' AND entity_id=s.id::text AND action='service.simulation.completed' AND (payload->>'job_id')::bigint=j.id)=1 FROM services s JOIN provision_jobs j ON j.service_id=s.id WHERE s.id=$1 AND j.id=$2`, r.ServiceID, r.ProvisionJobID)
			} else {
				g09CohortAssertSQL(t, e.db, `SELECT s.upstream_id=$3::bigint::text AND s.domain=$4 AND s.next_due_at=to_timestamp(1795000000) AND j.completed_at IS NOT NULL AND j.external_started_at IS NOT NULL AND j.last_error='' AND (j.result->>'upstream_id')::bigint=$3 AND (j.result->>'invoice_id')::bigint=$5 AND (j.result->>'cost_cents')::bigint=100 AND j.result->>'currency'='CNY' AND (SELECT count(*) FROM audit_events WHERE entity_type='service' AND entity_id=s.id::text AND action='supplier.delivery.succeeded' AND (payload->>'job_id')::bigint=j.id)=1 FROM services s JOIN provision_jobs j ON j.service_id=s.id WHERE s.id=$1 AND j.id=$2`, r.ServiceID, r.ProvisionJobID, 420000+r.ServiceID, fmt.Sprintf("g09-%d.test", r.ServiceID), 900000+r.ServiceID)
			}
		}
	}
	for _, u := range e.identities {
		g09CohortAssertSQL(t, e.db, `SELECT u.balance_cents=8000 AND u.balance_cents=(SELECT sum(delta_cents) FROM balance_ledger WHERE user_id=u.id) AND (SELECT count(*) FROM balance_ledger WHERE user_id=u.id)=3 AND NOT EXISTS(SELECT 1 FROM customer_credit_accounts WHERE user_id=u.id AND used_cents<>0) AND NOT EXISTS(SELECT 1 FROM payment_transactions WHERE user_id=u.id) FROM users u WHERE id=$1`, u.customerID)
	}
	g09CohortAssertSQL(t, e.db, `SELECT (SELECT count(*) FROM services)=300 AND (SELECT count(*) FROM orders)=300 AND (SELECT count(*) FROM invoices)=300 AND (SELECT count(*) FROM email_outbox)=150 AND (SELECT count(*) FROM sms_outbox)=150 AND (SELECT count(*) FROM tickets)=150 AND (SELECT count(*) FROM ticket_notification_events)=150 AND (SELECT count(*) FROM ticket_notification_deliveries)=300 AND NOT EXISTS(SELECT 1 FROM products WHERE stock<>0)`)
}

func g09CohortTaskIDs(records []g09CohortSource) (jobs, mail, sms, services, invoices, customers []int64) {
	seen := map[int64]bool{}
	for _, r := range records {
		if r.Kind == "ticket" {
			mail = append(mail, r.EmailOutboxID)
			sms = append(sms, r.SMSOutboxID)
		} else {
			jobs = append(jobs, r.ProvisionJobID)
			services = append(services, r.ServiceID)
			invoices = append(invoices, r.InvoiceID)
		}
		if !seen[r.CustomerID] {
			customers = append(customers, r.CustomerID)
			seen[r.CustomerID] = true
		}
	}
	return
}
func (e *g09CohortEnv) complete(t *testing.T, records []g09CohortSource) bool {
	t.Helper()
	jobs, mail, sms, _, _, _ := g09CohortTaskIDs(records)
	var complete, bad bool
	if e.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*)=300 AND bool_and(status='succeeded') FROM provision_jobs WHERE id=ANY($1::bigint[])) AND (SELECT count(*)=150 AND bool_and(status='sent') FROM email_outbox WHERE id=ANY($2::bigint[])) AND (SELECT count(*)=150 AND bool_and(status='sent') FROM sms_outbox WHERE id=ANY($3::bigint[])), EXISTS(SELECT 1 FROM provision_jobs WHERE id=ANY($1::bigint[]) AND status IN ('failed','waiting_verification')) OR EXISTS(SELECT 1 FROM email_outbox WHERE id=ANY($2::bigint[]) AND status IN ('dead_letter','expired')) OR EXISTS(SELECT 1 FROM sms_outbox WHERE id=ANY($3::bigint[]) AND status IN ('dead_letter','expired'))`, jobs, mail, sms).Scan(&complete, &bad) != nil {
		t.Fatal("explicit cohort state query failed")
	}
	if bad {
		t.Fatal("frozen source entered failure/unknown/expired state")
	}
	return complete
}

// This projection excludes periodic-job and service updated_at fields: periodic
// sync may legitimately update those while the cohort's actual effects persist.
func (e *g09CohortEnv) stableDigest(t *testing.T, records []g09CohortSource) string {
	t.Helper()
	jobs, mail, sms, services, invoices, customers := g09CohortTaskIDs(records)
	var raw []byte
	query := `SELECT jsonb_build_object(
 'jobs',(SELECT jsonb_agg(jsonb_build_array(id,service_id,action,status,attempts,completed_at,locked_at,result) ORDER BY id) FROM provision_jobs WHERE id=ANY($1::bigint[])),
 'mail',(SELECT jsonb_agg(jsonb_build_array(id,status,attempts,sent_at,locked_at,last_error) ORDER BY id) FROM email_outbox WHERE id=ANY($2::bigint[])),
 'sms',(SELECT jsonb_agg(jsonb_build_array(id,status,attempts,sent_at,locked_at,last_error) ORDER BY id) FROM sms_outbox WHERE id=ANY($3::bigint[])),
 'services',(SELECT jsonb_agg(jsonb_build_array(id,user_id,product_id,order_item_id,unit_number,status,upstream_id,domain,next_due_at,inventory_released,auto_renew,renewal_generation,renewal_discounts_used) ORDER BY id) FROM services WHERE id=ANY($4::bigint[])),
 'invoices',(SELECT jsonb_agg(jsonb_build_array(id,user_id,order_id,status,amount_cents,paid_at) ORDER BY id) FROM invoices WHERE id=ANY($5::bigint[])),
 'customers',(SELECT jsonb_agg(jsonb_build_array(id,balance_cents) ORDER BY id) FROM users WHERE id=ANY($6::bigint[])),
 'ledger',(SELECT jsonb_agg(jsonb_build_array(id,user_id,delta_cents,balance_after,entry_type,reference_type,reference_id) ORDER BY id) FROM balance_ledger WHERE user_id=ANY($6::bigint[])),
 'products',(SELECT jsonb_agg(jsonb_build_array(id,stock) ORDER BY id) FROM products),
 'evidence',(SELECT jsonb_agg(jsonb_build_array(outbox_id,attempts,locked_at,provider,http_status,outcome) ORDER BY outbox_id,attempts,locked_at) FROM sms_delivery_evidence WHERE outbox_id=ANY($3::bigint[])))`
	if e.db.Pool.QueryRow(t.Context(), query, jobs, mail, sms, services, invoices, customers).Scan(&raw) != nil {
		t.Fatal("cohort stable effect projection unavailable")
	}
	return g09SourceHash(string(raw))
}

type g09CohortBackground struct {
	ID, ServiceID  int64
	Action, Status string
	Attempts       int
	UpdatedAt      time.Time
}

func (e *g09CohortEnv) background(t *testing.T, records []g09CohortSource) []g09CohortBackground {
	t.Helper()
	jobs, _, _, _, _, _ := g09CohortTaskIDs(records)
	rows, err := e.db.Pool.Query(t.Context(), `SELECT id,service_id,action,status,attempts,updated_at FROM provision_jobs WHERE NOT(id=ANY($1::bigint[])) ORDER BY id LIMIT 61`, jobs)
	if err != nil {
		t.Fatal("background classification unavailable")
	}
	defer rows.Close()
	var result []g09CohortBackground
	owned := map[int64]bool{}
	for _, r := range records {
		if r.Kind == "supplier" {
			owned[r.ServiceID] = true
		}
	}
	for rows.Next() {
		var r g09CohortBackground
		if rows.Scan(&r.ID, &r.ServiceID, &r.Action, &r.Status, &r.Attempts, &r.UpdatedAt) != nil {
			t.Fatal("background projection invalid")
		}
		if !owned[r.ServiceID] || r.Action != "sync" {
			t.Fatal("unexpected background source outside frozen supplier services")
		}
		result = append(result, r)
	}
	if rows.Err() != nil || len(result) > 30 {
		t.Fatal("background supplier row budget exceeded")
	}
	return result
}

func (e *g09CohortEnv) twoMinuteTicks(t *testing.T, after time.Time) bool {
	t.Helper()
	var ok bool
	if e.db.Pool.QueryRow(t.Context(), `SELECT count(*)=4 AND bool_and(n>=2 AND span>=55) FROM (SELECT task_key,count(*) n,extract(epoch FROM(max(started_at)-min(started_at))) span FROM scheduler_runs WHERE task_key=ANY($1::text[]) AND status='succeeded' AND affected_count=0 AND started_at>$2 GROUP BY task_key) ticks`, []string{"affiliate_settlement", "service_expiry_notifications", "billing_notifications", "overdue_service_suspensions"}, after).Scan(&ok) != nil {
		t.Fatal("minute scheduler evidence unavailable")
	}
	return ok
}

func (e *g09CohortEnv) drain(t *testing.T, binary, dsn string, records []g09CohortSource, binaryHash string, start, end time.Time) {
	t.Helper()
	workerStarted := time.Now().UTC()
	deadline := workerStarted.Add(10 * time.Minute)
	workers := []*g09SourceWorkerChild{g09StartSourceWorkerOrigin(t, binary, e.private, dsn, e.db.ProvisionEncryptionKey, 0, e.origin), g09StartSourceWorkerOrigin(t, binary, e.private, dsn, e.db.ProvisionEncryptionKey, 1, e.origin)}
	defer func() {
		if t.Failed() {
			for _, w := range workers {
				w.stop(t)
			}
			e.logFailureState(t, workers)
		}
	}()
	for _, w := range workers {
		w.waitReady(t)
	}
	var drainedAt time.Time
	var digest string
	var samples []map[string]any
	var previousBackground string
	var minuteTicksVerified bool
	var nextObservation time.Time
	for {
		if time.Now().After(deadline) {
			t.Fatal("fixed ten-minute worker budget elapsed")
		}
		for _, w := range workers {
			w.assertRunning(t)
		}
		if e.notifications.Err() != nil {
			t.Fatal("notification journal failed")
		}
		for _, s := range e.suppliers {
			if s.sink.Err() != nil || s.gate.err() != nil {
				t.Fatal("supplier fixture failed")
			}
		}
		background := e.background(t, records)
		encoded, _ := json.Marshal(background)
		current := g09SourceHash(string(encoded))
		if current != previousBackground {
			samples = append(samples, map[string]any{"at": time.Now().UTC(), "background": background})
			previousBackground = current
		}
		if !time.Now().Before(nextObservation) {
			e.observeVerifier(t, "draining", nil, false)
			nextObservation = time.Now().Add(2 * time.Second)
		}
		if e.complete(t, records) {
			if drainedAt.IsZero() {
				e.verifySources(t, records, false)
				e.verifyProtocols(t, records, workerStarted, time.Now().UTC())
				complete := true
				e.observeVerifier(t, "first_drained", &complete, true)
				drainedAt = time.Now().UTC()
				digest = e.stableDigest(t, records)
			}
			if e.stableDigest(t, records) != digest {
				t.Fatal("frozen cohort effects changed after drain")
			}
			if e.twoMinuteTicks(t, drainedAt) {
				if time.Now().After(deadline) {
					t.Fatal("minute-tick evidence arrived after fixed ten-minute worker budget")
				}
				minuteTicksVerified = true
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("cohort did not drain and preserve effects over two real minute ticks within fixed ten-minute worker budget")
		}
		select {
		case <-t.Context().Done():
			t.Fatal("cohort drain cancelled")
		case <-time.After(time.Second):
		}
	}
	for _, w := range workers {
		w.stop(t)
		if !w.cleanTERM {
			t.Fatal("cohort worker did not complete clean TERM/Wait")
		}
	}
	workerEnded := time.Now().UTC()
	if workerEnded.After(deadline) {
		t.Fatal("clean worker shutdown exceeded fixed ten-minute worker budget")
	}
	e.verifySources(t, records, false)
	e.verifyProtocols(t, records, workerStarted, workerEnded)
	complete := true
	e.observeVerifier(t, "final", &complete, true)
	e.verifyWrongOwnerRejected(t, records)
	if e.stableDigest(t, records) != digest {
		t.Fatal("cohort final effects differ from first drained snapshot")
	}
	var busy int
	if e.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM supplier_accounts WHERE running_job_id IS NOT NULL`).Scan(&busy) != nil || busy != 0 {
		t.Fatal("supplier account lease remains after clean worker stop")
	}
	if err := g09fixture.WritePrivateJSON(filepath.Join(e.private, "cohort-drain-result.private.json"), map[string]any{"schemaVersion": 1, "mode": "source_handler_600_prerequisite", "sourceWindowStart": start, "sourceWindowEnd": end, "sourceWindowSeconds": end.Sub(start).Seconds(), "workerStartedAt": workerStarted, "firstDrainedAt": drainedAt, "workerEndedAt": workerEnded, "workerBinarySHA256": binaryHash, "workerPIDs": []int{workers[0].cmd.Process.Pid, workers[1].cmd.Process.Pid}, "workersExitedViaTERM": true, "drainTimeoutSeconds": 600, "candidateVerified": false, "capacityVerified": false, "twoRealMinuteTicksVerified": minuteTicksVerified, "stableEffectSHA256": digest, "notificationAcceptances": 300, "backgroundSamples": samples, "backgroundAttemptHistoryComplete": false}); err != nil {
		t.Fatal("private cohort result write failed")
	}
	public, err := json.Marshal(map[string]any{"mode": "source_handler_600_prerequisite", "sourceWindowStart": start, "sourceWindowEnd": end, "sourceWindowSeconds": end.Sub(start).Seconds(), "workerStartedAt": workerStarted, "firstDrainedAt": drainedAt, "workerEndedAt": workerEnded, "workerBinarySHA256": binaryHash, "workerPIDs": []int{workers[0].cmd.Process.Pid, workers[1].cmd.Process.Pid}, "workersExitedViaTERM": true, "drainTimeoutSeconds": 600, "candidateVerified": false, "capacityVerified": false, "twoRealMinuteTicksVerified": minuteTicksVerified, "stableEffectSHA256": digest, "notificationAcceptances": 300, "backgroundSamples": samples, "backgroundAttemptHistoryComplete": false, "verificationSamples": e.verificationSamples, "workerLogs": g09CohortWorkerLogEvidence(workers)})
	if err != nil {
		t.Fatal("public cohort evidence encoding failed")
	}
	t.Logf("G09_COHORT_RESULT %s", public)
	t.Logf("PASS: exact 600 tasks from real source HTTP (270 simulation + 30 supplier creates; 150 ticket email + 150 SMS), source %.3fs, real workers x2 drain %.3fs, effects persisted across two real minute scheduler ticks; candidate/namespace/27-minute/production capacity unverified", end.Sub(start).Seconds(), drainedAt.Sub(workerStarted).Seconds())
}

func g09CohortDurable(t *testing.T, path string, want []g09fixture.Acceptance) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal("durable protocol journal unavailable")
	}
	defer f.Close()
	var got []g09fixture.Acceptance
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var r g09fixture.Acceptance
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			t.Fatal("invalid durable protocol record")
		}
		got = append(got, r)
	}
	if scanner.Err() != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("protocol journal snapshot and disk differ")
	}
}

func (e *g09CohortEnv) verifyProtocols(t *testing.T, records []g09CohortSource, start, end time.Time) {
	t.Helper()
	expected := map[string]string{}
	var wants []g09CohortSource
	for _, r := range records {
		if r.Kind != "ticket" {
			continue
		}
		for _, d := range e.oracles[r.TicketID] {
			key := d.Channel + ":" + g09SourceHash(d.Recipient)
			hash := g09SourceHash(d.Body)
			if d.Channel == "email" {
				hash = g09SourceSMTPHash(g09CohortFrom, d.Recipient, d.Subject, d.Body)
			}
			if _, dup := expected[key]; dup {
				t.Fatal("notification identity is not unique")
			}
			expected[key] = hash
		}
		wants = append(wants, r)
	}
	accepted := e.notifications.Snapshot()
	if len(accepted) != 300 || len(expected) != 300 {
		t.Fatal("notification acceptance count is not exactly 300")
	}
	matched := map[string]bool{}
	for i, r := range accepted {
		key := r.Channel + ":" + r.RecipientHash
		if r.Sequence != uint64(i+1) || r.AcceptedAt.Before(start) || r.AcceptedAt.After(end) || matched[key] || expected[key] != r.MessageHash {
			t.Fatal("notification protocol identity/body/sequence mismatch")
		}
		matched[key] = true
	}
	g09CohortDurable(t, e.notificationPath, accepted)
	// Persisted SMS response is decrypted with the same frozen attempt domain;
	// raw body and address are never put into public diagnostics.
	for _, r := range wants {
		var locked time.Time
		var encrypted []byte
		if e.db.Pool.QueryRow(t.Context(), `SELECT locked_at,response_encrypted FROM sms_delivery_evidence WHERE outbox_id=$1 AND attempts=1`, r.SMSOutboxID).Scan(&locked, &encrypted) != nil {
			t.Fatal("SMS response evidence missing")
		}
		domain := fmt.Sprintf("sms:response:%d:1:%s", r.SMSOutboxID, locked.UTC().Format("2006-01-02T15:04:05.999999999Z"))
		opened, err := openSMSEvidence(e.db.ProvisionEncryptionKey, encrypted, domain)
		if err != nil || string(opened) != `{"accepted":true}` {
			t.Fatal("SMS accepted response evidence mismatch")
		}
	}
	for _, s := range e.suppliers {
		e.verifySupplierProtocol(t, s, start, end)
	}
}

// This oracle is independent of the sink's response serializer. Per service the
// first eight operations prove create/pay exactly once; later Active header reads
// are classified as periodic sync, with no exactly-once claim for those cycles.
func (e *g09CohortEnv) verifySupplierProtocol(t *testing.T, s *g09CohortSupplier, start, end time.Time) {
	t.Helper()
	facts := s.sink.Snapshot()
	if len(facts) != 3 {
		t.Fatal("supplier account did not admit three paid resources")
	}
	byHash := map[string]g09fixture.SupplierFact{}
	for _, f := range facts {
		if !f.Paid {
			t.Fatal("supplier service not paid")
		}
		byHash[g09SourceHash(strconv.FormatInt(f.LocalID, 10))] = f
	}
	records := s.journal.Snapshot()
	if len(records) < len(s.prefix) || !reflect.DeepEqual(records[:len(s.prefix)], s.prefix) {
		t.Fatal("supplier metadata prefix changed")
	}
	g09CohortDurable(t, s.path, records)
	counters := map[int64]int{}
	for i, r := range records {
		if r.Sequence != uint64(i+1) {
			t.Fatal("supplier durable sequence invalid")
		}
		if i < len(s.prefix) {
			if r.RecipientHash != g09SourceHash("0") {
				t.Fatal("supplier metadata admitted an entity")
			}
			continue
		}
		if r.AcceptedAt.Before(start) || r.AcceptedAt.After(end) {
			t.Fatal("supplier protocol outside worker interval")
		}
		if r.RecipientHash == g09SourceHash("0") {
			switch r.Channel {
			case "supplier.login", "supplier.cart/get_product_config", "supplier.user_info":
			default:
				t.Fatal("unexpected worker supplier metadata operation")
			}
			continue
		}
		f, ok := byHash[r.RecipientHash]
		if !ok {
			t.Fatal("cross-account supplier entity acceptance")
		}
		phase := counters[f.LocalID]
		channel, body := g09CohortSupplierExpected(f.SupplierResource, phase)
		raw, err := json.Marshal(body)
		if err != nil || r.Channel != channel || r.MessageHash != g09SourceHash(string(raw)) {
			t.Fatal("supplier per-service create/pay/periodic response hash mismatch")
		}
		counters[f.LocalID]++
	}
	for _, f := range facts {
		if counters[f.LocalID] < 8 {
			t.Fatal("supplier create/pay protocol incomplete")
		}
	}
}

func g09CohortSupplierExpected(r g09fixture.SupplierResource, phase int) (string, any) {
	ids := map[string]any{"hostid": []int64{r.HostID}, "invoiceid": r.InvoiceID}
	response := func(status int) any { return map[string]any{"status": status, "data": ids} }
	invoice := func(status string) any {
		return map[string]any{"status": 200, "data": map[string]any{"detail": map[string]any{"id": r.InvoiceID, "subtotal": "1.00", "status": status}, "currency": map[string]any{"code": "CNY"}, "invoice_items": []any{map[string]any{"type": "host", "rel_id": r.HostID, "amount": "1.00"}}}}
	}
	host := func(status string) any {
		return map[string]any{"status": 200, "data": map[string]any{"host_data": map[string]any{"id": r.HostID, "productid": 7, "domainstatus": status, "domain": fmt.Sprintf("g09-%d.test", r.LocalID), "nextduedate": 1795000000, "assignedips": []any{}}}}
	}
	switch phase {
	case 0:
		return "supplier.cart/clear", map[string]any{"status": 200}
	case 1:
		return "supplier.cart/add_to_shop", map[string]any{"status": 200}
	case 2:
		return "supplier.cart/settle", response(200)
	case 3:
		return "supplier.get_invoices_detail", invoice("Unpaid")
	case 4:
		return "supplier.host/header", host("Pending")
	case 5:
		return "supplier.apply_credit", response(1001)
	case 6:
		return "supplier.get_invoices_detail", invoice("Paid")
	default:
		return "supplier.host/header", host("Active")
	}
}

func (e *g09CohortEnv) captureBaseline(t *testing.T) {
	t.Helper()
	customers := map[int64]map[string]any{}
	products := map[int64]map[string]any{}
	for _, u := range e.identities {
		var balance int64
		if e.db.Pool.QueryRow(t.Context(), `SELECT balance_cents FROM users WHERE id=$1`, u.customerID).Scan(&balance) != nil || balance != 10000 {
			t.Fatal("opening balance differs from API seed")
		}
		rows, err := e.db.Pool.Query(t.Context(), `SELECT id,delta_cents,balance_after FROM balance_ledger WHERE user_id=$1 ORDER BY id`, u.customerID)
		if err != nil {
			t.Fatal("opening ledger unavailable")
		}
		var ledger []map[string]int64
		for rows.Next() {
			var id, delta, after int64
			if rows.Scan(&id, &delta, &after) != nil {
				rows.Close()
				t.Fatal("opening ledger invalid")
			}
			ledger = append(ledger, map[string]int64{"ID": id, "DeltaCents": delta, "BalanceAfter": after})
		}
		err = rows.Err()
		rows.Close()
		if err != nil || len(ledger) != 1 || ledger[0]["DeltaCents"] != 10000 || ledger[0]["BalanceAfter"] != 10000 {
			t.Fatal("opening ledger does not match API seed")
		}
		customers[u.customerID] = map[string]any{"BalanceCents": balance, "Ledger": ledger}
	}
	ids := []int64{e.localProduct}
	for _, s := range e.suppliers {
		ids = append(ids, s.productID)
	}
	for _, id := range ids {
		var stock int64
		if e.db.Pool.QueryRow(t.Context(), `SELECT stock FROM products WHERE id=$1`, id).Scan(&stock) != nil {
			t.Fatal("opening stock unavailable")
		}
		products[id] = map[string]any{"Stock": stock, "Services": map[int64]bool{}}
	}
	if g09fixture.WritePrivateJSON(filepath.Join(e.private, "baseline.private.json"), map[string]any{"CapturedAtUTC": time.Now().UTC(), "Customers": customers, "Products": products}) != nil {
		t.Fatal("private baseline could not be frozen")
	}
}
