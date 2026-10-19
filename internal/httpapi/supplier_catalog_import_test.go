package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/codex/zjmfgo/internal/supplier"
)

type supplierCatalogImportFixture struct {
	db            *store.Store
	h             http.Handler
	root, reader  string
	supplierID    int64
	reads         atomic.Int64
	priceVersion  atomic.Int64
	blocked       atomic.Bool
	entered, open chan struct{}
	once          sync.Once
}

func supplierCatalogImportSetup(t *testing.T) *supplierCatalogImportFixture {
	t.Helper()
	f := &supplierCatalogImportFixture{entered: make(chan struct{}), open: make(chan struct{})}
	f.db, f.h = testServer(t)
	f.db.PurchaseFieldEncryptionKey = bytes.Repeat([]byte{3}, 32)
	f.priceVersion.Store(1)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zjmf_api_login" {
			fmt.Fprint(w, `{"status":200,"jwt":"catalog-private-jwt"}`)
			return
		}
		if r.URL.Path != "/cart/get_product_config" || r.Header.Get("Authorization") != "Bearer catalog-private-jwt" {
			t.Errorf("unexpected catalog endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		f.reads.Add(1)
		if f.blocked.Load() {
			f.once.Do(func() { close(f.entered) })
			<-f.open
		}
		pid, _ := strconv.ParseInt(r.URL.Query().Get("pid"), 10, 64)
		json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": supplierCatalogImportDetail(pid, f.priceVersion.Load())})
	}))
	t.Cleanup(remote.Close)
	_, f.root = user(t, f.db, f.h, "catalog-import-root", "super_admin")
	id, reader := user(t, f.db, f.h, "catalog-import-reader", "admin")
	f.reader = reader
	contentRole(t, f.db, id, "products.view")
	w := request(f.h, "POST", "/api/v1/admin/suppliers", store.SupplierInput{Name: "Catalog upstream", Provider: supplier.ProviderZJMF, BaseURL: remote.URL, Username: "catalog-account", Password: "catalog-private-password", Active: enabled()}, f.root, "")
	expect(t, w, 201)
	f.supplierID = int64(data(t, w)["id"].(float64))
	return f
}

func supplierCatalogImportDetail(pid, version int64) map[string]any {
	pricing := func(monthly, quarterly, setup string) []map[string]any {
		return []map[string]any{{"code": "CNY", "monthly": monthly, "msetupfee": setup, "quarterly": quarterly, "qsetupfee": setup}}
	}
	data := map[string]any{
		"products":         map[string]any{"id": pid, "name": fmt.Sprintf("Upstream %d", pid), "description": "Imported description", "location_version": version, "stock_control": 1, "qty": 8, "pay_type": map[string]any{"pay_type": "recurring"}},
		"product_pricings": pricing("10.01", "29.01", "0.03"),
		"config_groups": []map[string]any{{"options": []map[string]any{
			{"id": 501, "option_name": "Operating system", "option_type": 10, "sub": []map[string]any{{"id": 601, "option_name": "Linux", "pricings": pricing("0.03", "0.05", "0.01")}, {"id": 602, "option_name": "Windows", "pricings": pricing("0.05", "0.09", "0.03")}}},
			{"id": 502, "option_name": "Data disk", "option_type": 14, "qty_minimum": 0, "qty_maximum": 10, "qty_stage": 1, "sub": []map[string]any{{"id": 603, "option_name": "GB", "qty_minimum": 0, "qty_maximum": 10, "pricings": pricing("0.03", "0.05", "0.01")}}},
			{"id": 503, "option_name": "Graduated capacity", "option_type": 15, "qty_minimum": 1, "qty_maximum": 10, "qty_stage": 1, "sub": []map[string]any{{"id": 604, "option_name": "Capacity", "qty_minimum": 1, "qty_maximum": 5, "pricings": pricing("0.05", "0.09", "0.01")}, {"id": 605, "option_name": "Capacity", "qty_minimum": 6, "qty_maximum": 10, "pricings": pricing("0.03", "0.05", "0.03")}}},
		}}},
		"customfields": []map[string]any{
			{"id": 701, "fieldname": "Reference", "fieldtype": "text", "required": 1, "regexpr": "^[a-z]+$"},
			{"id": 702, "fieldname": "Private password", "fieldtype": "password", "required": 1},
			{"id": 703, "fieldname": "Terms", "fieldtype": "tickbox", "required": 1},
			{"id": 704, "fieldname": "Region", "fieldtype": "dropdown", "fieldoptions": "east,west", "required": 1},
		},
		"advanced": []any{},
	}
	if pid == 99 {
		data["advanced"] = []map[string]any{{"unsupported": true}}
	}
	return data
}

func (f *supplierCatalogImportFixture) path(suffix string) string {
	return fmt.Sprintf("/api/v1/admin/suppliers/%d/%s", f.supplierID, suffix)
}

func (f *supplierCatalogImportFixture) preview(t *testing.T, ids ...int64) (store.SupplierCatalogImportInput, store.SupplierCatalogImportPreview) {
	t.Helper()
	in := store.SupplierCatalogImportInput{ExpectedSupplierVersion: 1, PriceMultiplierBPS: 15000, Active: true, Items: []store.SupplierCatalogImportItem{}}
	for _, id := range ids {
		in.Items = append(in.Items, store.SupplierCatalogImportItem{UpstreamProductID: id})
	}
	w := request(f.h, "POST", f.path("import-preview"), in, f.root, "")
	expect(t, w, 200)
	var envelope struct {
		Data store.SupplierCatalogImportPreview `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	for i := range in.Items {
		in.Items[i].ExpectedUpstreamRevision = envelope.Data.Items[i].Product.Revision
	}
	return in, envelope.Data
}

func supplierCatalogImportResults(t *testing.T, w *httptest.ResponseRecorder) store.SupplierCatalogImportResult {
	t.Helper()
	var envelope struct {
		Data store.SupplierCatalogImportResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestSupplierCatalogBatchImportsAllCyclesChoicesAndFields(t *testing.T) {
	f := supplierCatalogImportSetup(t)
	in, preview := f.preview(t, 70, 71)
	if len(preview.Items) != 2 || !preview.Items[0].Importable || preview.Items[0].LocalPrices["monthly"].AmountCents != 1502 || preview.Items[0].LocalPrices["monthly"].SetupCents != 5 {
		t.Fatalf("incorrect schema/rounding preview: %+v", preview)
	}
	var products int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM products").Scan(&products); err != nil || products != 0 {
		t.Fatal("preview mutated products", products, err)
	}
	section := catalogGroupID(t, f.h, f.root, "section", catalogGroupInput("import-section", nil))
	category := catalogGroupID(t, f.h, f.root, "category", catalogGroupInput("import-category", &section))
	in.CategoryID = &category
	w := request(f.h, "POST", f.path("import-batch"), in, f.root, "catalog-import-first")
	expect(t, w, 201)
	result := supplierCatalogImportResults(t, w)
	if len(result.Items) != 2 || result.Items[0].Status != "created" || result.Items[1].Status != "created" {
		t.Fatal("batch did not create both products", result)
	}
	pid := result.Items[0].ProductID
	var raw []byte
	var active bool
	var description string
	var savedCategory int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT ci.configuration,p.active,p.description,p.category_id FROM supplier_catalog_imports ci JOIN supplier_product_bindings b ON b.id=ci.binding_id JOIN products p ON p.id=b.product_id WHERE p.id=$1`, pid).Scan(&raw, &active, &description, &savedCategory); err != nil {
		t.Fatal(err)
	}
	var configuration store.SupplierCatalogConfiguration
	if err := json.Unmarshal(raw, &configuration); err != nil || !active || savedCategory != category || description != "Imported description" || configuration.MultiplierBPS != 15000 || len(configuration.Options) != 4 || len(configuration.Fields) != 4 {
		t.Fatal("incomplete product mapping", configuration, active, description, err)
	}
	var cycles, options, tiers, encrypted int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM product_prices WHERE product_id=$1 AND active),(SELECT count(*) FROM product_options po JOIN product_option_groups g ON g.id=po.group_id WHERE g.product_id=$1),(SELECT count(*) FROM product_option_price_tiers t JOIN product_options po ON po.id=t.option_id JOIN product_option_groups g ON g.id=po.group_id WHERE g.product_id=$1),(SELECT count(*) FROM product_purchase_fields WHERE product_id=$1 AND encrypted)`, pid).Scan(&cycles, &options, &tiers, &encrypted); err != nil || cycles != 2 || options != 4 || tiers != 6 || encrypted != 1 {
		t.Fatal("missing matrix, choice, tiers or encrypted fields", cycles, options, tiers, encrypted, err)
	}
	for _, secret := range []string{"catalog-private-password", "catalog-private-jwt"} {
		if strings.Contains(w.Body.String(), secret) || strings.Contains(string(raw), secret) {
			t.Fatal("credentials leaked")
		}
	}
	reads := f.reads.Load()
	replay := request(f.h, "POST", f.path("import-batch"), in, f.root, "catalog-import-first")
	expect(t, replay, 201)
	if replay.Body.String() != w.Body.String() || f.reads.Load() != reads {
		t.Fatal("receipt replay repeated network or changed result")
	}
	in.PriceMultiplierBPS = 20000
	expect(t, request(f.h, "POST", f.path("import-batch"), in, f.root, "catalog-import-first"), 409)
	newKey := request(f.h, "POST", f.path("import-batch"), in, f.root, "catalog-import-again")
	expect(t, newKey, 201)
	if got := supplierCatalogImportResults(t, newKey); got.Items[0].Status != "existing" || got.Items[0].ProductID != pid {
		t.Fatal("repeated supplier import overwrote or duplicated product", got)
	}
	var amount int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT price_cents FROM products WHERE id=$1", pid).Scan(&amount); err != nil || amount != 1502 {
		t.Fatal("existing import repriced local product", amount, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE supplier_catalog_imports SET configuration='{}' WHERE binding_id=(SELECT id FROM supplier_product_bindings WHERE product_id=$1)", pid); err == nil {
		t.Fatal("import snapshot is mutable")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents) VALUES($1,'annually',1,0)", pid); err == nil {
		t.Fatal("unsupported billing cycle accepted")
	}
}

func TestSupplierCatalogBatchRejectsStaleUnsupportedAndAuditFailure(t *testing.T) {
	f := supplierCatalogImportSetup(t)
	in, preview := f.preview(t, 70, 99)
	if preview.Items[1].Importable {
		t.Fatal("unsupported product labeled importable")
	}
	expect(t, request(f.h, "POST", f.path("import-batch"), in, f.root, "catalog-unsupported"), 409)
	in, _ = f.preview(t, 70, 71)
	f.priceVersion.Store(2)
	expect(t, request(f.h, "POST", f.path("import-batch"), in, f.root, "catalog-stale-revision"), 409)
	f.priceVersion.Store(1)
	in.ExpectedSupplierVersion = 2
	expect(t, request(f.h, "POST", f.path("import-batch"), in, f.root, "catalog-stale-account"), 409)
	in.ExpectedSupplierVersion = 1
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_catalog_import_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='supplier.catalog.product.imported' THEN RAISE EXCEPTION 'injected import audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_catalog_import_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_catalog_import_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", f.path("import-batch"), in, f.root, "catalog-audit-failure"), 500)
	var products, mappings, receipts int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM products),(SELECT count(*) FROM supplier_catalog_imports),(SELECT count(*) FROM mutation_operations WHERE scope LIKE 'admin:supplier.catalog.import.%')`).Scan(&products, &mappings, &receipts); err != nil || products != 0 || mappings != 0 || receipts != 0 {
		t.Fatal("failed batch left partial products or receipt", products, mappings, receipts, err)
	}
}

func TestSupplierCatalogBatchPermissionValidationAndConcurrentReplay(t *testing.T) {
	f := supplierCatalogImportSetup(t)
	in, _ := f.preview(t, 70)
	reads := f.reads.Load()
	expect(t, request(f.h, "POST", f.path("import-preview"), in, f.reader, ""), 403)
	expect(t, request(f.h, "POST", f.path("import-batch"), in, f.reader, "catalog-reader-denied"), 403)
	expect(t, request(f.h, "POST", f.path("import-batch"), in, f.root, ""), 400)
	invalid := in
	invalid.Items = []store.SupplierCatalogImportItem{in.Items[0], in.Items[0]}
	expect(t, request(f.h, "POST", f.path("import-preview"), invalid, f.root, ""), 409)
	invalid.Items = make([]store.SupplierCatalogImportItem, 51)
	for i := range invalid.Items {
		invalid.Items[i].UpstreamProductID = int64(i + 1)
	}
	expect(t, request(f.h, "POST", f.path("import-preview"), invalid, f.root, ""), 409)
	invalid = in
	missingCategory := int64(999999)
	invalid.CategoryID = &missingCategory
	expect(t, request(f.h, "POST", f.path("import-preview"), invalid, f.root, ""), 409)
	if f.reads.Load() != reads {
		t.Fatal("invalid or unauthorized request contacted supplier")
	}
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Go(func() {
			responses <- request(f.h, "POST", f.path("import-batch"), in, f.root, "catalog-concurrent-key")
		})
	}
	wg.Wait()
	close(responses)
	var first string
	for response := range responses {
		expect(t, response, 201)
		if first != "" && first != response.Body.String() {
			t.Fatal("concurrent replay returned different result")
		}
		first = response.Body.String()
	}
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM supplier_catalog_imports").Scan(&count); err != nil || count != 1 {
		t.Fatal("concurrent batch duplicated product", count, err)
	}
}

func TestSupplierCatalogBatchRechecksCredentialAfterUpstreamRead(t *testing.T) {
	f := supplierCatalogImportSetup(t)
	in, _ := f.preview(t, 70)
	actor, cookie := user(t, f.db, f.h, "catalog-import-writer", "admin")
	contentRole(t, f.db, actor, "products.view", "products.manage")
	f.blocked.Store(true)
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		finished <- request(f.h, "POST", f.path("import-batch"), in, cookie, "catalog-revoked-session")
	}()
	<-f.entered
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE sessions SET expires_at=now()-interval '1 second' WHERE user_id=$1", actor); err != nil {
		t.Fatal(err)
	}
	close(f.open)
	expect(t, <-finished, 401)
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM supplier_catalog_imports").Scan(&count); err != nil || count != 0 {
		t.Fatal("expired credential created product", count, err)
	}
}
