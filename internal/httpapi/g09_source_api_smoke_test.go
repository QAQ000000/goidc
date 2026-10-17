package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/g09fixture"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Opt-in source API regression: Node uses real loopback sockets and the actual
// scenario library. Seed alone calls the simulation provisioner. No worker,
// release candidate, SMTP/SMS delivery, load profile or capacity claim is involved.
// Run serially after other database acceptance has ended:
//
//	ZJMFGO_G09_SOURCE_API_SMOKE=1 REQUIRE_INTEGRATION=1 scripts/go.sh test \
//	  ./internal/httpapi -run '^TestG09SourceAPIScenarioSmoke$' -count=1 -timeout=5m -v
func TestG09SourceAPIScenarioSmoke(t *testing.T) {
	if os.Getenv("ZJMFGO_G09_SOURCE_API_SMOKE") != "1" {
		t.Skip("opt-in G09 source API HTTP-to-Node regression; no capacity proof")
	}
	dsn := os.Getenv("ZJMFGO_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("REQUIRE_INTEGRATION") == "1" {
			t.Fatal("isolated test DSN required")
		}
		t.Skip("requires isolated PostgreSQL 18 test database")
	}
	parsed, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid isolated test DSN")
	}
	for _, host := range append([]string{parsed.ConnConfig.Host}, g09FallbackHosts(parsed)...) {
		if host != "127.0.0.1" && host != "::1" && host != "localhost" {
			t.Fatal("G09 source API smoke requires loopback test PostgreSQL")
		}
	}
	if !strings.HasSuffix(parsed.ConnConfig.Database, "_test") {
		t.Fatal("G09 source API smoke requires _test database")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node.js required for explicitly enabled smoke")
	}
	// Verify the test server before testServer is allowed to create its schema.
	parsed.MaxConns = 1
	parsed.ConnConfig.ConnectTimeout = 5 * time.Second
	preflight, err := pgxpool.NewWithConfig(t.Context(), parsed)
	if err != nil {
		t.Fatal("isolated PostgreSQL preflight unavailable")
	}
	var version int
	var actualDatabase string
	err = preflight.QueryRow(t.Context(), "SELECT current_setting('server_version_num')::int,current_database()").Scan(&version, &actualDatabase)
	preflight.Close()
	if err != nil || version < 180000 || version >= 190000 || actualDatabase != parsed.ConnConfig.Database {
		t.Fatal("G09 source API smoke requires PostgreSQL 18")
	}
	db, h := testServer(t)
	uid, cookie := user(t, db, h, "g09-source-customer", "customer")
	_, admin := user(t, db, h, "g09-source-admin", "super_admin")
	// Explicit seed-only SQL: fixed simulation prices, stock and department.
	pid := product(t, db, 20)
	if _, err = db.Pool.Exec(t.Context(), "UPDATE products SET price_cents=1000,setup_fee_cents=0 WHERE id=$1", pid); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE product_prices SET price_cents=1000,setup_fee_cents=0 WHERE product_id=$1 AND billing_cycle='monthly'", pid); err != nil {
		t.Fatal(err)
	}
	var department int64
	if err = db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES('G09 source smoke','g09-source') RETURNING id").Scan(&department); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", uid), map[string]any{"amount_cents": 20000}, admin, "g09-source-seed-topup"), 201)
	v, item := add(t, h, cookie, pid, 1)
	seedOrder := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, cookie, "g09-source-seed-order")
	expect(t, seedOrder, 201)
	seedInvoice := int64(data(t, seedOrder)["invoice_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", seedInvoice), map[string]any{}, cookie, "g09-source-seed-pay"), 201)
	seedJob, err := db.ClaimProvisionJob(t.Context())
	if err != nil || seedJob == nil {
		t.Fatal("seed simulation job missing")
	}
	if err = db.ProcessProvisionJob(t.Context(), *seedJob); err != nil {
		t.Fatal(err)
	}
	serviceID := seedJob.ServiceID
	var beforeDue time.Time
	var beforeBalance int64
	var beforeStock int
	if err = db.Pool.QueryRow(t.Context(), "SELECT s.next_due_at,u.balance_cents,p.stock FROM services s JOIN users u ON u.id=s.user_id JOIN products p ON p.id=s.product_id WHERE s.id=$1 AND s.status='active' AND s.provision_mode='simulation'", serviceID).Scan(&beforeDue, &beforeBalance, &beforeStock); err != nil || beforeBalance != 19000 || beforeStock != 19 {
		t.Fatal("incorrect seed baseline")
	}
	manual := store.ManualInvoiceInput{UserID: uid, DueAt: time.Now().UTC().Add(30 * 24 * time.Hour), Items: []store.ManualInvoiceItem{{Description: "G09 source manual payable", AmountCents: 1000}}}
	manualID := manualInvoiceCreate(t, h, admin, manual, "g09-source-manual-create")
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	private := t.TempDir()
	if err = os.Chmod(private, 0700); err != nil {
		t.Fatal(err)
	}
	baseFlow := func(id, kind string) map[string]any {
		return map[string]any{"id": id, "kind": kind, "customerId": uid, "cookie": cookie, "amountCents": 1000}
	}
	pay, renew, purchase, ticket := baseFlow("manual", "balance_pay"), baseFlow("renew", "local_renew"), baseFlow("purchase", "purchase_local"), baseFlow("ticket", "ticket")
	pay["invoiceId"] = manualID
	renew["serviceId"], renew["beforeDue"] = serviceID, beforeDue.UTC().Format(time.RFC3339Nano)
	purchase["productId"], purchase["optionIds"], purchase["optionQuantities"], purchase["purchaseFields"] = pid, []int64{}, map[string]int{}, map[string]string{}
	ticket["departmentId"] = department
	manifest := map[string]any{
		"schemaVersion": 1, "mode": "source_api", "runId": "source-smoke", "apiOrigin": server.URL, "publicOrigin": "http://localhost:3088",
		"accounts": []map[string]any{{"id": uid, "cookie": cookie, "budgetCents": beforeBalance}},
		"flows":    []map[string]any{pay, renew, purchase, ticket},
	}
	manifestPath := filepath.Join(private, "manifest.private.json")
	if err = g09fixture.WritePrivateJSON(manifestPath, manifest); err != nil {
		t.Fatal("write private manifest")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "scripts/g09-source-api-smoke.mjs", manifestPath)
	cmd.Dir = "../.."
	// Do not inherit DSNs, .env, proxy variables, NODE_OPTIONS or channel secrets.
	cmd.Env = []string{"LANG=C", "LC_ALL=C", "TZ=UTC"}
	if _, err = cmd.Output(); err != nil {
		// Child output is intentionally not dumped; private evidence can contain
		// credentials. Keep the error code fixed even if a dependency changes.
		t.Fatal("G09 source API Node chain failed; no worker/candidate/capacity proof")
	}
	var result struct {
		OK                bool   `json:"ok"`
		Mode              string `json:"mode"`
		CandidateVerified bool   `json:"candidateVerified"`
		WorkerVerified    bool   `json:"workerVerified"`
		HTTPRequests      int    `json:"httpRequests"`
		Results           []struct {
			Kind               string `json:"kind"`
			InvoiceID          int64  `json:"invoiceId"`
			OrderID            int64  `json:"orderId"`
			TicketID           int64  `json:"ticketId"`
			Succeeded          bool   `json:"succeeded"`
			DeliveryVerified   bool   `json:"deliveryVerified"`
			AccountingVerified bool   `json:"accountingVerified"`
		} `json:"results"`
	}
	output, err := os.ReadFile(filepath.Join(private, "result.json"))
	if err != nil || json.Unmarshal(output, &result) != nil || !result.OK || result.Mode != "source_api" || result.CandidateVerified || result.WorkerVerified || result.HTTPRequests != 17 || len(result.Results) != 4 {
		t.Fatal("invalid source API result envelope")
	}
	for i, kind := range []string{"balance_pay", "local_renew", "purchase_local", "ticket"} {
		if r := result.Results[i]; r.Kind != kind || !r.Succeeded || r.DeliveryVerified || r.AccountingVerified {
			t.Fatal("invalid source API operation receipt")
		}
	}
	if result.Results[0].InvoiceID != manualID || result.Results[1].InvoiceID < 1 || result.Results[2].OrderID < 1 || result.Results[2].InvoiceID < 1 || result.Results[3].TicketID < 1 {
		t.Fatal("missing source API entities")
	}
	invoiceIDs := []int64{manualID, result.Results[1].InvoiceID, result.Results[2].InvoiceID}
	g09AssertSQL(t, db, `SELECT count(*)=3 AND bool_and(i.user_id=$2 AND i.status='paid' AND i.paid_at IS NOT NULL AND i.amount_cents=1000
 AND (SELECT count(*) FROM balance_ledger l WHERE l.reference_type='invoice' AND l.reference_id=i.id AND l.entry_type='payment')=1
 AND (SELECT coalesce(sum(-l.delta_cents),0) FROM balance_ledger l WHERE l.reference_type='invoice' AND l.reference_id=i.id AND l.entry_type='payment' AND l.user_id=$2)=1000)
 FROM invoices i WHERE i.id=ANY($1::bigint[])`, invoiceIDs, uid)
	g09AssertSQL(t, db, `SELECT balance_cents=$2-3000 AND balance_cents=(SELECT sum(delta_cents) FROM balance_ledger WHERE user_id=$1)
 AND (SELECT count(*) FROM balance_ledger WHERE user_id=$1)=5
 AND NOT EXISTS(SELECT 1 FROM customer_credit_accounts WHERE user_id=$1 AND (used_cents<>0 OR limit_cents<>0))
 AND NOT EXISTS(SELECT 1 FROM payment_transactions WHERE user_id=$1) FROM users WHERE id=$1`, uid, beforeBalance)
	g09AssertSQL(t, db, `SELECT count(*)=1 AND bool_and(e.before_due=$3 AND e.after_due=s.next_due_at AND e.after_due>e.before_due
 AND e.before_used=0 AND e.after_used=0 AND s.renewal_discounts_used=0 AND s.status='active' AND s.provision_mode='simulation')
 FROM service_renewal_effects e JOIN services s ON s.id=e.service_id WHERE e.invoice_id=$1 AND s.id=$2`, result.Results[1].InvoiceID, serviceID, beforeDue)
	g09AssertSQL(t, db, `SELECT count(*)=1 AND bool_and(o.user_id=$2 AND o.status='paid' AND o.review_status='approved' AND o.total_cents=1000
 AND oi.quantity=1 AND s.unit_number=1 AND s.status='queued' AND s.provision_mode='simulation' AND NOT s.inventory_released
 AND j.action='create' AND j.status='pending' AND j.attempts=0)
 FROM orders o JOIN order_items oi ON oi.order_id=o.id JOIN services s ON s.order_item_id=oi.id
 JOIN provision_jobs j ON j.service_id=s.id WHERE o.id=$1`, result.Results[2].OrderID, uid)
	g09AssertSQL(t, db, "SELECT stock=$2-1 FROM products WHERE id=$1", pid, beforeStock)
	g09AssertSQL(t, db, `SELECT count(*)=1 AND bool_and(t.user_id=$2 AND t.department_id=$3
 AND (SELECT count(*) FROM ticket_mutation_receipts r WHERE r.ticket_id=t.id AND r.actor_id=$2 AND r.operation='create')=1
 AND (SELECT count(*) FROM ticket_notification_events e WHERE e.ticket_id=t.id AND e.kind='customer_created' AND e.actor_id=$2)=1)
 FROM tickets t WHERE t.id=$1`, result.Results[3].TicketID, uid, department)
	// The measured purchase job must remain pending: no hidden worker completion.
	g09AssertSQL(t, db, `SELECT count(*)=2 AND count(*) FILTER(WHERE status='succeeded')=1 AND count(*) FILTER(WHERE status='pending')=1 FROM provision_jobs`)
	g09AssertSQL(t, db, `SELECT NOT EXISTS(SELECT 1 FROM email_outbox) AND NOT EXISTS(SELECT 1 FROM sms_outbox)`)
	g09AssertSQL(t, db, `SELECT (SELECT count(*) FROM orders)=2 AND (SELECT count(*) FROM services)=2
 AND (SELECT count(*) FROM invoices)=4 AND (SELECT count(*) FROM service_renewal_effects)=1
 AND (SELECT count(*) FROM tickets)=1 AND (SELECT count(*) FROM ticket_mutation_receipts)=1`)
	t.Log("PASS: source HTTP-to-Node manual balance payment, renewal, purchase, ticket and SQL facts; worker/candidate/capacity unverified")
}

func g09FallbackHosts(c *pgxpool.Config) []string {
	var hosts []string
	for _, fallback := range c.ConnConfig.Fallbacks {
		hosts = append(hosts, fallback.Host)
	}
	return hosts
}

func g09AssertSQL(t *testing.T, db *store.Store, query string, args ...any) {
	t.Helper()
	var valid bool
	if err := db.Pool.QueryRow(t.Context(), query, args...).Scan(&valid); err != nil || !valid {
		t.Fatal("G09 source API SQL invariant failed", err)
	}
}
