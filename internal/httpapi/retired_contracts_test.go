package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func requireContractsRetired(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	expect(t, w, http.StatusGone)
	var body struct {
		Error struct{ Code string } `json:"error"`
		Data  json.RawMessage       `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != "contracts_retired" || body.Data != nil {
		t.Fatalf("unexpected retirement response: %s (%v)", w.Body.String(), err)
	}
	if w.Header().Get("Content-Disposition") != "" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatal("retired contract response exposed a document")
	}
}

func TestContractsRetiredHandlerNeedsNoStore(t *testing.T) {
	w := httptest.NewRecorder()
	(&Server{}).retiredContracts(w, httptest.NewRequest(http.MethodPost, "/api/v1/contracts", strings.NewReader("not json")))
	requireContractsRetired(t, w)
}

func TestContractsRetiredRoutesPreserveAuthorizationAndHistory(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "contract-retired-root", "super_admin")
	_, staff := user(t, db, h, "contract-retired-staff", "admin")
	readerID, reader := user(t, db, h, "contract-retired-reader", "admin")
	uid, customer := user(t, db, h, "contract-retired-customer", "customer")
	_, other := user(t, db, h, "contract-retired-other", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	sid, iid := billingSchedulerFixture(t, db, uid, product(t, db, 10), 1714, false)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1", iid); err != nil {
		t.Fatal(err)
	}
	// Historical data is seeded directly, not through retired endpoints. Even an
	// old enabled setting and a saved successful request must not reopen them.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE contract_settings SET enabled=true"); err != nil {
		t.Fatal(err)
	}
	var tid, cid int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO contract_templates(name,content) VALUES('历史模板','历史正文 {{customer_name}}') RETURNING id").Scan(&tid); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO contracts(customer_id,service_id,template_id,template_name,customer_name,customer_email,product_name,service_name,domain,paid_at,content,party_snapshot,service_snapshot,status)
VALUES($1,$2,$3,'历史模板','历史客户','legacy@example.test','历史商品','历史服务','legacy.example.test',now(),'冻结历史正文','{"party":"frozen"}','{"service":"frozen"}','approved') RETURNING id`, uid, sid, tid).Scan(&cid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO contract_history(contract_id,actor_id,action,before_status,after_status,note,version) VALUES($1,$2,'reviewed','pending','approved','历史审核',1)`, cid, rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO contract_documents(contract_id,version,content,signer_name,status) VALUES($1,1,'冻结历史正文','历史客户','approved')`, cid); err != nil {
		t.Fatal(err)
	}
	input := struct {
		ServiceID  int64 `json:"service_id"`
		TemplateID int64 `json:"template_id"`
	}{ServiceID: sid, TemplateID: tid}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	const savedKey = "contract-retired-saved-request"
	if _, err = db.Pool.Exec(t.Context(), `INSERT INTO contract_receipts(actor_id,operation,idempotency_key,request_hash,result) VALUES($1,'POST /api/v1/contracts',$2,$3,jsonb_build_object('id',$4::bigint))`, uid, savedKey, hex.EncodeToString(sum[:]), cid); err != nil {
		t.Fatal(err)
	}
	keyResponse := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "retired-contract-session-only-check"}, root, "")
	expect(t, keyResponse, http.StatusCreated)
	apiSecret := data(t, keyResponse)["secret"].(string)
	before := retiredContractState(t, db)
	contractPath := fmt.Sprintf("/api/v1/admin/contracts/%d", cid)
	customerPath := fmt.Sprintf("/api/v1/contracts/%d", cid)
	templatePath := fmt.Sprintf("/api/v1/admin/contract-templates/%d", tid)
	routes := []struct {
		method, path string
		admin        bool
	}{
		{"GET", "/api/v1/admin/contracts/available-templates", true},
		{"GET", "/api/v1/admin/contracts", true},
		{"GET", "/api/v1/admin/contracts/eligible-services", true},
		{"GET", contractPath, true},
		{"GET", contractPath + "/document", true},
		{"POST", "/api/v1/admin/contracts", true},
		{"GET", "/api/v1/admin/contract-templates", true},
		{"GET", templatePath, true},
		{"GET", "/api/v1/contracts", false},
		{"GET", "/api/v1/contracts/eligible-services", false},
		{"GET", customerPath, false},
		{"GET", customerPath + "/document", false},
		{"POST", "/api/v1/contracts", false},
		{"GET", "/api/v1/contract-templates", false},
		{"POST", "/api/v1/admin/contract-templates", true},
		{"PUT", templatePath, true},
		{"POST", contractPath + "/review", true},
		{"PUT", contractPath + "/shipping", true},
		{"POST", contractPath + "/void", true},
		{"POST", "/api/v1/admin/contracts/bulk-void", true},
		{"POST", customerPath + "/submit", false},
		{"GET", "/api/v1/admin/contract-settings", true},
		{"PUT", "/api/v1/admin/contract-settings", true},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			allowed, wrongPortal := []string{customer, other}, staff
			if route.admin {
				allowed, wrongPortal = []string{root, staff}, customer
				expect(t, request(h, route.method, route.path, input, reader, savedKey), http.StatusForbidden)
			}
			for _, cookie := range allowed {
				w := request(h, route.method, route.path, input, cookie, savedKey)
				requireContractsRetired(t, w)
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("retirement response can be cached")
				}
			}
			for _, cookie := range []string{"", "zjmfgo_session=invalid; zjmfgo_admin_session=invalid", wrongPortal} {
				expect(t, request(h, route.method, route.path, input, cookie, savedKey), http.StatusUnauthorized)
			}
			// Every retired contract route retains sessionAuth. A genuine API
			// key must therefore reach the explicit session-only denial, even
			// though its administrator would be allowed with a browser cookie.
			policyErrorCode(t, requestBearer(h, route.method, route.path, input, apiSecret), http.StatusForbidden, "session_required")
			// Malformed bearer syntax is ignored by bearerCredential. Reads
			// have no browser cookie (401); writes first fail the outer CSRF
			// wrapper (403), preserving the pre-retirement middleware behavior.
			invalidStatus, invalidCode := http.StatusUnauthorized, "unauthorized"
			if route.method != http.MethodGet {
				invalidStatus, invalidCode = http.StatusForbidden, "csrf_required"
			}
			policyErrorCode(t, requestBearer(h, route.method, route.path, input, "retired-route-bearer"), invalidStatus, invalidCode)
		})
	}
	// Browser safety checks still run before the retired mutation handler.
	expect(t, requestFrom(h, "POST", "/api/v1/contracts", input, "192.0.2.1:1234", customer, map[string]string{"X-Requested-With": ""}), http.StatusForbidden)
	expect(t, requestFrom(h, "POST", "/api/v1/contracts", input, "192.0.2.1:1234", customer, map[string]string{"Origin": "https://invalid.example"}), http.StatusForbidden)
	if after := retiredContractState(t, db); !reflect.DeepEqual(before, after) {
		for table, value := range before {
			if after[table] != value {
				t.Errorf("retired route changed %s", table)
			}
		}
	}
}

func retiredContractState(t *testing.T, db *store.Store) map[string]string {
	t.Helper()
	out := map[string]string{}
	// Authentication access logs may advance; contract, business, audit and
	// mutation-receipt data must remain byte-for-byte equal as JSONB.
	for _, table := range []string{"contract_settings", "contract_templates", "contracts", "contract_history", "contract_documents", "contract_receipts", "mutation_operations", "users", "services", "orders", "order_items", "invoices", "balance_ledger", "audit_events"} {
		var state string
		if err := db.Pool.QueryRow(t.Context(), "SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM "+table+" t").Scan(&state); err != nil {
			t.Fatal(err)
		}
		out[table] = state
	}
	return out
}
