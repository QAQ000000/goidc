package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/g09fixture"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Explicit source Handler -> actual Node scenario -> real signed callback SQL.
// There is no external merchant checkout request, worker or capacity evidence.
func TestG09EpaySourceScenarioSmoke(t *testing.T) {
	if os.Getenv("ZJMFGO_G09_EPAY_SOURCE_SMOKE") != "1" {
		t.Skip("opt-in signed Epay source HTTP/Node scenario; no real merchant or capacity proof")
	}
	parsed, err := g09SourceWorkerDBConfig(os.Getenv("ZJMFGO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("explicit Epay smoke requires authorized loopback _test URL")
	}
	preflightCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	parsed.MaxConns = 1
	parsed.ConnConfig.ConnectTimeout = 5 * time.Second
	preflight, err := pgxpool.NewWithConfig(preflightCtx, parsed)
	if err != nil {
		t.Fatal("Epay isolated preflight unavailable")
	}
	var version int
	var database string
	err = preflight.QueryRow(preflightCtx, "SELECT current_database(),current_setting('server_version_num')::int").Scan(&database, &version)
	preflight.Close()
	if err != nil || database != parsed.ConnConfig.Database || version < 180000 || version >= 190000 {
		t.Fatal("Epay source smoke requires isolated PG18")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node required for explicit Epay source smoke")
	}
	var merchantRequests atomic.Int32
	merchant := g09SourceLoopbackServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		merchantRequests.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	const secret = "g09-private-epay-source-key"
	db, h := testServerConfig(t, func(c *config.Config) {
		c.PaymentMockEnabled = false
		c.EpayAPIURL = merchant.URL
		c.EpayPID = "1000"
		c.EpayKey = secret
		c.EpayTypes = []string{"alipay", "wxpay"}
	})
	uid, customer := user(t, db, h, "g09-epay-customer", "customer")
	_, staff := user(t, db, h, "g09-epay-staff", "super_admin")
	// Seed only: two real admin-created manual payable invoices, no paid facts.
	invoices := []int64{}
	for i := 0; i < 2; i++ {
		invoices = append(invoices, manualInvoiceCreate(t, h, staff, store.ManualInvoiceInput{UserID: uid, DueAt: time.Now().UTC().Add(30 * 24 * time.Hour), Items: []store.ManualInvoiceItem{{Description: "G09 Epay source payable", AmountCents: 1000}}}, fmt.Sprintf("g09-epay-manual-%d", i)))
	}
	api := g09SourceLoopbackServer(t, h)
	private := t.TempDir()
	if os.Chmod(private, 0700) != nil {
		t.Fatal("private Epay directory unavailable")
	}
	flows := []map[string]any{}
	for i, provider := range []string{"epay_alipay", "epay_wxpay"} {
		flows = append(flows, map[string]any{"id": fmt.Sprintf("epay-%d", i), "kind": "external_settle", "provider": provider, "customerId": uid, "cookie": customer, "invoiceId": invoices[i], "amountCents": 1000})
	}
	manifest := map[string]any{"schemaVersion": 1, "mode": "source_epay", "apiOrigin": api.URL, "publicOrigin": "http://localhost:3088", "merchantOrigin": merchant.URL, "runId": "epay-source", "pid": "1000", "key": secret, "accounts": []map[string]any{{"id": uid, "cookie": customer, "budgetCents": 2000}}, "flows": flows}
	manifestPath := filepath.Join(private, "epay-manifest.private.json")
	if g09fixture.WritePrivateJSON(manifestPath, manifest) != nil {
		t.Fatal("private Epay manifest failed")
	}
	ctx, stop := context.WithTimeout(t.Context(), 45*time.Second)
	defer stop()
	cmd := exec.CommandContext(ctx, node, "scripts/g09-epay-source-smoke.mjs", manifestPath)
	cmd.Dir = "../.."
	cmd.Env = []string{"LANG=C", "LC_ALL=C", "TZ=UTC"}
	if _, err = cmd.CombinedOutput(); err != nil {
		t.Fatal("Node signed Epay source chain failed; no external merchant/capacity proof")
	}
	raw, err := os.ReadFile(filepath.Join(private, "epay-result.json"))
	var result struct {
		OK        bool   `json:"ok"`
		Mode      string `json:"mode"`
		Capacity  bool   `json:"capacityVerified"`
		Candidate bool   `json:"candidateVerified"`
		Requests  int    `json:"actualHTTPRequests"`
		Callbacks int    `json:"callbackAttempts"`
		Results   []struct {
			InvoiceID  int64 `json:"invoiceId"`
			Succeeded  bool  `json:"succeeded"`
			Accounting bool  `json:"accountingVerified"`
		} `json:"results"`
	}
	if err != nil || json.Unmarshal(raw, &result) != nil || !result.OK || result.Mode != "source_epay" || result.Capacity || result.Candidate || result.Requests != 12 || result.Callbacks != 4 || len(result.Results) != 2 {
		t.Fatal("Epay source result invalid")
	}
	for i, r := range result.Results {
		if r.InvoiceID != invoices[i] || !r.Succeeded || r.Accounting {
			t.Fatal("Epay source result identity or proof invalid")
		}
	}
	g09AssertSQL(t, db, `SELECT count(*)=2 AND bool_and(i.user_id=$2 AND i.status='paid' AND i.amount_cents=1000 AND i.paid_at IS NOT NULL AND p.status='succeeded' AND p.user_id=$2 AND p.amount_cents=1000 AND p.external_transaction_id<>'' AND (SELECT count(*) FROM payment_transactions x WHERE x.invoice_id=i.id)=1 AND (SELECT count(*) FROM audit_events a WHERE a.entity_type='invoice' AND a.entity_id=i.id::text AND a.action='payment.webhook.succeeded')=1) FROM invoices i JOIN payment_transactions p ON p.invoice_id=i.id WHERE i.id=ANY($1::bigint[])`, invoices, uid)
	g09AssertSQL(t, db, `SELECT (SELECT count(*) FROM payment_webhook_events WHERE status='processed')=2 AND (SELECT count(*) FROM payment_transactions)=2 AND (SELECT count(DISTINCT external_transaction_id) FROM payment_transactions)=2 AND NOT EXISTS(SELECT 1 FROM balance_ledger WHERE user_id=$1) AND NOT EXISTS(SELECT 1 FROM provision_jobs) AND NOT EXISTS(SELECT 1 FROM orders) AND (SELECT balance_cents=0 FROM users WHERE id=$1)`, uid)
	// Public journals contain request hashes/status only, never signed forms or cookies.
	public, err := os.ReadFile(filepath.Join(private, "epay-requests.jsonl"))
	if err != nil {
		t.Fatal("public Epay metadata unavailable")
	}
	for _, secret := range []string{secret, customer, "TRADE_SUCCESS", "checkout_url", "out_trade_no", "trade_no="} {
		if strings.Contains(string(public), secret) {
			t.Fatal("Epay public metadata includes private protocol")
		}
	}
	file, err := os.Open(filepath.Join(private, "epay-mutations.private.jsonl"))
	if err != nil {
		t.Fatal("private Epay originals unavailable")
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	privateCount, callbackForms := 0, 0
	for scanner.Scan() {
		var row map[string]any
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			t.Fatal("Epay original JSON invalid")
		}
		privateCount++
		if row["kind"] == "epay_callback_original" {
			callbackForms++
			body, ok := row["body"].(string)
			if !ok || g09SourceHash(body) != row["payloadSha256"] || row["plannedAttempts"] != float64(2) {
				t.Fatal("Epay original callback conservation invalid")
			}
		}
	}
	if scanner.Err() != nil || privateCount != 4 || callbackForms != 2 || merchantRequests.Load() != 0 {
		t.Fatal("Epay originals or isolated merchant boundary invalid")
	}
	t.Log("PASS: two real source API intents -> independently signed Node notifications each with one exact duplicate -> paid invoices, unique payment/event/audit and unchanged balances; 12 real HTTP requests; real merchant/worker/candidate/capacity unverified")
}
