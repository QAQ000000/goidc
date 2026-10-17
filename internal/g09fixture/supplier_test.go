package g09fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/supplier"
)

func supplierFixture(t *testing.T, budget int) (*SupplierSink, *Journal, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "supplier.jsonl")
	j, err := NewJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	tokens := map[int64]string{99: strings.Repeat("x", 32), 100: strings.Repeat("y", 32)}
	resources := []SupplierResource{{LocalID: 99, HostID: 42, InvoiceID: 90}, {LocalID: 100, HostID: 43, InvoiceID: 91}}
	s, err := NewSupplierSink(j, SupplierConfig{Username: "g09-synthetic-user", Password: "g09-private-synthetic-password", Token: "g09-private-synthetic-bearer", DownstreamURL: "http://127.0.0.1:29000", DownstreamTokens: tokens, Resources: resources, RequestBudget: budget})
	if err != nil {
		t.Fatal(err)
	}
	// Caller-owned maps/slices cannot change the frozen service binding.
	tokens[99] = strings.Repeat("z", 32)
	resources[0].LocalID = 999
	server := httptest.NewServer(s)
	t.Cleanup(server.Close)
	return s, j, server, path
}

func TestSupplierSinkLostAcknowledgmentKeepsFrozenIdentity(t *testing.T) {
	for _, lostPath := range []string{"/cart/settle", "/apply_credit"} {
		t.Run(strings.TrimPrefix(lostPath, "/"), func(t *testing.T) {
			s, j, _, _ := supplierFixture(t, 200)
			var lost atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == lostPath && lost.CompareAndSwap(false, true) {
					// Let the real sink persist and commit, then actually drop the
					// socket before the compiled adapter can receive its response.
					recorded := httptest.NewRecorder()
					s.ServeHTTP(recorded, r)
					if recorded.Code != 200 {
						t.Error("fault injection did not reach success")
					}
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
					return
				}
				s.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			a := fixtureAdapter(t, server)
			p, err := a.Product(t.Context(), 7)
			if err != nil {
				t.Fatal(err)
			}
			request := supplier.CreateRequest{ProductID: 7, Cycle: "monthly", Revision: p.Revision, LocalServiceID: 99, Host: "svc-99", DownstreamURL: "http://127.0.0.1:29000", DownstreamToken: strings.Repeat("x", 32), MaximumCostCents: 100, Currency: "CNY"}
			out, err := a.Create(t.Context(), request)
			if lostPath == "/cart/settle" {
				var upstream *supplier.Error
				if !errors.As(err, &upstream) || !upstream.Unknown || upstream.Code != "settlement_unknown" {
					t.Fatal("lost create result not classified unknown", err)
				}
				// This is a deliberate fixture-level exact-identity replay. It
				// does not authorize a production worker to retry an unknown create.
				out, err = a.Create(t.Context(), request)
			}
			if err != nil || out.UpstreamID != 42 || out.InvoiceID != 90 || out.Status != "active" {
				t.Fatal("frozen identity not recoverable in fixture", err)
			}
			facts := s.Snapshot()
			if len(facts) != 1 || !facts[0].Paid || s.Err() != nil {
				t.Fatal("lost acknowledgment invented resource")
			}
			counts := map[string]int{}
			for _, record := range j.Snapshot() {
				counts[record.Channel]++
			}
			if !lost.Load() || counts["supplier.cart/settle"] != 1 || counts["supplier.apply_credit"] != 1 {
				t.Fatal("duplicate mutation after lost acknowledgment", counts)
			}
		})
	}
}

func fixtureAdapter(t *testing.T, server *httptest.Server) supplier.Adapter {
	t.Helper()
	a, err := supplier.New(supplier.Account{Provider: supplier.ProviderZJMF, BaseURL: server.URL, Username: "g09-synthetic-user", Password: "g09-private-synthetic-password"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func fixturePurchase(t *testing.T, a supplier.Adapter, local int64) supplier.CreateResult {
	t.Helper()
	p, err := a.Product(t.Context(), 7)
	if err != nil || !p.Supported {
		t.Fatal("compiled product contract failed", err)
	}
	out, err := a.Create(t.Context(), supplier.CreateRequest{ProductID: 7, Cycle: "monthly", Revision: p.Revision, LocalServiceID: local, Host: fmt.Sprintf("svc-%d", local), DownstreamURL: "http://127.0.0.1:29000", DownstreamToken: map[int64]string{99: strings.Repeat("x", 32), 100: strings.Repeat("y", 32)}[local], MaximumCostCents: 100, Currency: "CNY"})
	if err != nil {
		t.Fatal("compiled create contract failed", err)
	}
	return out
}

func TestSupplierSinkCompiledCreateSyncReplay(t *testing.T) {
	s, j, server, path := supplierFixture(t, 200)
	a := fixtureAdapter(t, server)
	start := time.Now()
	catalog, err := a.Catalog(t.Context())
	if err != nil || len(catalog) != 1 || catalog[0].ID != 7 {
		t.Fatal("compiled catalog contract failed", err)
	}
	first := fixturePurchase(t, a, 99)
	if first.UpstreamID != 42 || first.InvoiceID != 90 || first.ProductID != 7 || first.Status != "active" || first.CostCents != 100 || first.Currency != "CNY" {
		t.Fatal("incorrect create facts")
	}
	remote, err := a.Sync(t.Context(), 42)
	if err != nil || remote.Status != "Active" || remote.NextDueDate != "1795000000" {
		t.Fatal("compiled sync contract failed", err)
	}
	// A new adapter/login retries the same frozen downstream identity. The
	// clear endpoint returns existing IDs; no second settle or payment occurs.
	replay := fixturePurchase(t, fixtureAdapter(t, server), 99)
	if replay != first {
		t.Fatal("replay invented a new resource")
	}
	second := fixturePurchase(t, a, 100)
	if second.UpstreamID != 43 || second.InvoiceID != 91 {
		t.Fatal("second source not independently bound")
	}
	if _, err = a.Sync(t.Context(), 999); err == nil {
		t.Fatal("unknown resource accepted")
	}
	states := s.Snapshot()
	if len(states) != 2 || !states[0].Paid || !states[1].Paid || s.Err() != nil {
		t.Fatal("bounded resources not conserved")
	}
	counts := map[string]int{}
	records := j.Snapshot()
	for i, record := range records {
		if record.Sequence != uint64(i+1) || record.AcceptedAt.Before(start) {
			t.Fatal("invalid journal sequence or time")
		}
		counts[record.Channel]++
	}
	if counts["supplier.cart/settle"] != 2 || counts["supplier.apply_credit"] != 2 {
		t.Fatal("duplicate create charged or settled twice", counts)
	}
	// Independently reopen durable data; do not accept Snapshot as disk proof.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != len(records) {
		t.Fatal("durable protocol conservation failed")
	}
	for i, line := range lines {
		var actual Acceptance
		if json.Unmarshal([]byte(line), &actual) != nil || actual != records[i] {
			t.Fatal("durable journal differs from accepted response")
		}
	}
	for _, secret := range []string{"g09-synthetic-user", "g09-private-synthetic-password", "g09-private-synthetic-bearer", strings.Repeat("x", 32), "g09-99.test"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("journal leaked raw identity or secret")
		}
	}
	// The canonical successful apply_credit response can be correlated without
	// publishing request bodies or authentication secrets.
	response := `{"data":{"hostid":[42],"invoiceid":90},"status":1001}`
	wantHash := sha256.Sum256([]byte(response))
	found := false
	for _, record := range records {
		if record.Channel == "supplier.apply_credit" && record.RecipientHash == digest("99") {
			found = record.MessageHash == hex.EncodeToString(wantHash[:])
		}
	}
	if !found {
		t.Fatal("no source-to-protocol hash match")
	}
}

func TestSupplierSinkConcurrentDuplicateSettlementHasOneResource(t *testing.T) {
	s, j, server, _ := supplierFixture(t, 100)
	state := s.resources[99]
	if code := supplierRequest(t, server, "/cart/add_to_shop", s.cart(state), "g09-private-synthetic-bearer"); code != 200 {
		t.Fatal(code)
	}
	settle := s.common(state)
	for key, values := range s.cart(state) {
		settle.Set("cart_data["+key+"]", values[0])
	}
	// Independent real socket requests return the same preallocated identity.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code := supplierRequest(t, server, "/cart/settle", settle, "g09-private-synthetic-bearer"); code != 200 {
				t.Error(code)
			}
		}()
	}
	wg.Wait()
	facts := s.Snapshot()
	if len(facts) != 1 || facts[0].HostID != 42 || facts[0].InvoiceID != 90 || facts[0].Paid || s.Err() != nil {
		t.Fatal("duplicate settlement broke resource conservation")
	}
	if len(j.Snapshot()) != 9 {
		t.Fatal("protocol attempts incorrectly collapsed")
	}
	for _, key := range []string{"downstream_token", "cart_data[pid]", "downstream_id"} {
		bad := url.Values{}
		for name, values := range settle {
			bad[name] = append([]string(nil), values...)
		}
		if key == "downstream_id" {
			bad.Add(key, "100")
		} else {
			bad.Set(key, "wrong")
		}
		if code := supplierRequest(t, server, "/cart/settle", bad, "g09-private-synthetic-bearer"); code == 200 {
			t.Fatal("invalid request admitted", key)
		}
	}
	if len(j.Snapshot()) != 9 || len(s.Snapshot()) != 1 {
		t.Fatal("invalid request changed successful facts")
	}
	wrongSource := s.common(state)
	wrongSource.Set("downstream_token", strings.Repeat("y", 32)) // Valid token for the other frozen source.
	if code := supplierRequest(t, server, "/cart/clear", wrongSource, "g09-private-synthetic-bearer"); code != 400 || len(j.Snapshot()) != 9 {
		t.Fatal("cross-service token accepted")
	}
}

func supplierRequest(t *testing.T, server *httptest.Server, path string, values url.Values, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestSupplierSinkRejectsUnauthenticatedUnknownAndOverBudget(t *testing.T) {
	s, j, server, _ := supplierFixture(t, 3)
	common := url.Values{"downstream_id": {"999"}, "downstream_url": {"http://127.0.0.1:29000"}, "downstream_token": {strings.Repeat("x", 32)}}
	if code := supplierRequest(t, server, "/cart/clear", common, "wrong"); code != 401 {
		t.Fatal(code)
	}
	if code := supplierRequest(t, server, "/cart/clear", common, "g09-private-synthetic-bearer"); code != 404 {
		t.Fatal(code)
	}
	if code := supplierRequest(t, server, "/zjmf_api_login", url.Values{"username": {"g09-synthetic-user"}, "password": {"wrong"}}, ""); code != 401 {
		t.Fatal(code)
	}
	if code := supplierRequest(t, server, "/cart/clear", common, "g09-private-synthetic-bearer"); code != 503 {
		t.Fatal(code)
	}
	if s.Err() == nil || len(j.Snapshot()) != 0 || len(s.Snapshot()) != 0 {
		t.Fatal("rejection/budget not conserved")
	}
}

func TestSupplierSinkPersistenceFailurePreventsResourceAdmission(t *testing.T) {
	s, j, server, _ := supplierFixture(t, 100)
	common := url.Values{"downstream_id": {"99"}, "downstream_url": {"http://127.0.0.1:29000"}, "downstream_token": {strings.Repeat("x", 32)}}
	state := s.resources[99]
	if code := supplierRequest(t, server, "/cart/add_to_shop", s.cart(state), "g09-private-synthetic-bearer"); code != 200 {
		t.Fatal(code)
	}
	before := len(j.Snapshot())
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	for key, values := range s.cart(state) {
		common.Set("cart_data["+key+"]", values[0])
	}
	for range 2 {
		if code := supplierRequest(t, server, "/cart/settle", common, "g09-private-synthetic-bearer"); code != 503 {
			t.Fatal(code)
		}
	}
	if s.Err() == nil || j.Err() == nil || len(s.Snapshot()) != 0 || len(j.Snapshot()) != before {
		t.Fatal("failed journal admitted a resource")
	}
}
