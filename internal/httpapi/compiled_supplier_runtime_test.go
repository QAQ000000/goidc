package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/codex/zjmfgo/internal/supplier"
)

const compiledSupplierProvider = "fixture_supplier_runtime"
const compiledSupplierSchema = 2

// This provider is linked only into the HTTP test binary. Its two ordinary
// fields and two independently rotated credentials deliberately do not use
// the legacy supplier's endpoint, username or password convention.
type compiledSupplierAdapter struct {
	realm, account, catalogKey, writeKey string
}
type compiledSupplierControl struct {
	catalogKey, writeKey                       string
	catalogs, products, creates, syncs, renews int
	unknown                                    bool
	nextResource                               int64
	resources                                  map[int64]supplier.Service
}

var compiledSupplierState = struct {
	sync.Mutex
	accounts map[string]*compiledSupplierControl
}{accounts: make(map[string]*compiledSupplierControl)}
var compiledSupplierRegistration sync.Once
var compiledSupplierSequence atomic.Uint64

func compiledSupplierFixtures(t *testing.T) {
	t.Helper()
	compiledSupplierRegistration.Do(func() {
		err := supplier.Register(supplier.Provider{
			ID: compiledSupplierProvider, Name: "Fixture supplier runtime",
			Capabilities:   supplier.Capabilities{Catalog: true, Product: true, Create: true, Sync: true, Renew: true},
			IdentityFields: []string{"realm", "account"},
		}, channel.Descriptor{
			ID: compiledSupplierProvider, Name: "Fixture supplier runtime", Category: channel.Provision,
			InstanceMode: "supplier", SchemaVersion: compiledSupplierSchema,
			Capabilities: []string{"catalog", "product", "create", "sync", "renew"},
			Fields: []channel.Field{
				{Key: "realm", Label: "Realm", Type: channel.Text, Required: true},
				{Key: "account", Label: "Account", Type: channel.Text, Required: true},
				{Key: "timeout", Label: "Timeout", Type: channel.Number, Integer: true, Default: 30},
				{Key: "catalog_key", Label: "Read key", Type: channel.Text, Required: true, Secret: true},
				{Key: "write_key", Label: "Write key", Type: channel.Text, Required: true, Secret: true},
			},
		}, func(account supplier.Account) (supplier.Adapter, error) {
			realm, _ := account.Values["realm"].(string)
			name, _ := account.Values["account"].(string)
			if realm == "" || name == "" || account.Secrets["catalog_key"] == "" || account.Secrets["write_key"] == "" {
				return nil, errors.New("fixture supplier configuration is incomplete")
			}
			return compiledSupplierAdapter{realm, name, account.Secrets["catalog_key"], account.Secrets["write_key"]}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}
func (a compiledSupplierAdapter) key() string { return a.realm + "/" + a.account }
func (a compiledSupplierAdapter) Capabilities() supplier.Capabilities {
	return supplier.Capabilities{Catalog: true, Product: true, Create: true, Sync: true, Renew: true}
}
func compiledSupplierProduct() supplier.Product {
	return supplier.Product{ID: 73, Name: "Independent upstream VM", Currency: "CNY", Revision: "fixture-product-v1", SupplierVersion: 3, Supported: true,
		Prices: map[string]supplier.Price{"monthly": {AmountCents: 1600, SetupCents: 100}}, Options: []supplier.Option{}, Fields: []supplier.Field{}}
}
func (a compiledSupplierAdapter) control(read bool) (*compiledSupplierControl, error) {
	c := compiledSupplierState.accounts[a.key()]
	if c == nil || (read && a.catalogKey != c.catalogKey) || (!read && a.writeKey != c.writeKey) {
		return nil, &supplier.Error{Code: "fixture_auth_rejected"}
	}
	return c, nil
}
func (a compiledSupplierAdapter) Catalog(ctx context.Context) ([]supplier.Product, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	compiledSupplierState.Lock()
	defer compiledSupplierState.Unlock()
	c, err := a.control(true)
	if err != nil {
		return nil, err
	}
	c.catalogs++
	return []supplier.Product{compiledSupplierProduct()}, nil
}
func (a compiledSupplierAdapter) Product(ctx context.Context, id int64) (supplier.Product, error) {
	if err := ctx.Err(); err != nil {
		return supplier.Product{}, err
	}
	compiledSupplierState.Lock()
	defer compiledSupplierState.Unlock()
	c, err := a.control(true)
	if err != nil {
		return supplier.Product{}, err
	}
	c.products++
	if id != 73 {
		return supplier.Product{}, &supplier.Error{Code: "fixture_product_missing"}
	}
	return compiledSupplierProduct(), nil
}
func (a compiledSupplierAdapter) Create(ctx context.Context, in supplier.CreateRequest) (supplier.CreateResult, error) {
	if err := ctx.Err(); err != nil {
		return supplier.CreateResult{}, err
	}
	compiledSupplierState.Lock()
	defer compiledSupplierState.Unlock()
	c, err := a.control(false)
	if err != nil {
		return supplier.CreateResult{}, err
	}
	if in.ProductID != 73 || in.Revision != "fixture-product-v1" || in.Currency != "CNY" || in.MaximumCostCents < 1700 || in.Cycle != "monthly" || in.LocalServiceID < 1 {
		return supplier.CreateResult{}, &supplier.Error{Code: "fixture_purchase_invalid"}
	}
	c.creates++
	if c.unknown {
		return supplier.CreateResult{}, &supplier.Error{Code: "fixture_write_unknown", Unknown: true}
	}
	c.nextResource++
	remote := supplier.Service{UpstreamID: c.nextResource, ProductID: 73, Status: "Active", Domain: fmt.Sprintf("%s-%d.fixture.invalid", a.account, c.nextResource), NextDueDate: "1800000000", Username: "root", Password: "fixture-resource-private"}
	c.resources[remote.UpstreamID] = remote
	return supplier.CreateResult{UpstreamID: remote.UpstreamID, InvoiceID: 9000 + remote.UpstreamID, ProductID: 73, Status: remote.Status, Domain: remote.Domain, NextDueDate: remote.NextDueDate, CostCents: 1700, Currency: "CNY"}, nil
}
func (a compiledSupplierAdapter) Sync(ctx context.Context, id int64) (supplier.Service, error) {
	if err := ctx.Err(); err != nil {
		return supplier.Service{}, err
	}
	compiledSupplierState.Lock()
	defer compiledSupplierState.Unlock()
	c, err := a.control(true)
	if err != nil {
		return supplier.Service{}, err
	}
	c.syncs++
	remote, ok := c.resources[id]
	if !ok {
		return supplier.Service{}, &supplier.Error{Code: "fixture_resource_missing"}
	}
	return remote, nil
}
func (a compiledSupplierAdapter) Execute(context.Context, supplier.ActionRequest) (supplier.Service, error) {
	return supplier.Service{}, &supplier.Error{Code: "fixture_unsupported_action"}
}
func (a compiledSupplierAdapter) QuoteRenewal(ctx context.Context, in supplier.RenewalRequest) (supplier.RenewalRequest, error) {
	remote, err := a.Sync(ctx, in.UpstreamID)
	if err != nil {
		return in, err
	}
	if in.ProductID != remote.ProductID || in.MaximumCostCents < 1600 || in.Currency != "CNY" || in.Cycle != "monthly" {
		return in, &supplier.Error{Code: "fixture_renewal_invalid"}
	}
	in.CostCents = 1600
	due, _ := strconv.ParseInt(remote.NextDueDate, 10, 64)
	in.PreviousDue = time.Unix(due, 0).UTC().Format(time.RFC3339)
	return in, nil
}
func (a compiledSupplierAdapter) Renew(ctx context.Context, in supplier.RenewalRequest, checkpoint func(supplier.CreateResult) error) (supplier.CreateResult, supplier.Service, error) {
	if err := ctx.Err(); err != nil {
		return supplier.CreateResult{}, supplier.Service{}, err
	}
	compiledSupplierState.Lock()
	c, err := a.control(false)
	if err != nil {
		compiledSupplierState.Unlock()
		return supplier.CreateResult{}, supplier.Service{}, err
	}
	remote, ok := c.resources[in.UpstreamID]
	if !ok {
		compiledSupplierState.Unlock()
		return supplier.CreateResult{}, supplier.Service{}, &supplier.Error{Code: "fixture_resource_missing"}
	}
	c.renews++
	result := supplier.CreateResult{UpstreamID: in.UpstreamID, InvoiceID: 19000 + in.UpstreamID, ProductID: in.ProductID, CostCents: in.CostCents, Currency: in.Currency}
	compiledSupplierState.Unlock()
	if err := checkpoint(result); err != nil {
		return result, remote, &supplier.Error{Code: "fixture_checkpoint_failed", Unknown: true}
	}
	compiledSupplierState.Lock()
	defer compiledSupplierState.Unlock()
	due, _ := strconv.ParseInt(remote.NextDueDate, 10, 64)
	remote.NextDueDate = strconv.FormatInt(due+2678400, 10)
	c.resources[in.UpstreamID] = remote
	return result, remote, nil
}
func (a compiledSupplierAdapter) ConfirmRenewal(ctx context.Context, in supplier.RenewalRequest, invoice int64) (supplier.CreateResult, supplier.Service, error) {
	remote, err := a.Sync(ctx, in.UpstreamID)
	return supplier.CreateResult{UpstreamID: in.UpstreamID, InvoiceID: invoice, ProductID: in.ProductID, CostCents: in.CostCents, Currency: in.Currency}, remote, err
}

type compiledSupplierFixture struct {
	*supplierFixture
	realm string
}

func newCompiledSupplierFixture(t *testing.T) *compiledSupplierFixture {
	t.Helper()
	compiledSupplierFixtures(t)
	base := &supplierFixture{}
	base.db, base.h = testServer(t)
	_, base.root = user(t, base.db, base.h, "compiled-supplier-root", "super_admin")
	base.uid, base.customer = user(t, base.db, base.h, "compiled-supplier-customer", "customer")
	rid, reader := user(t, base.db, base.h, "compiled-supplier-reader", "admin")
	base.reader = reader
	contentRole(t, base.db, rid, "products.view", "services.view")
	return &compiledSupplierFixture{base, fmt.Sprintf("fixture-realm-%d", compiledSupplierSequence.Add(1))}
}
func (f *compiledSupplierFixture) input(account string) map[string]any {
	return map[string]any{"name": "Compiled " + account, "provider": compiledSupplierProvider, "active": true, "schema_version": compiledSupplierSchema,
		"config":        map[string]any{"realm": f.realm, "account": account},
		"secure_config": map[string]string{"catalog_key": account + "-catalog-private", "write_key": account + "-write-private"}}
}
func (f *compiledSupplierFixture) account(t *testing.T, account string) int64 {
	t.Helper()
	compiledSupplierState.Lock()
	compiledSupplierState.accounts[f.realm+"/"+account] = &compiledSupplierControl{catalogKey: account + "-catalog-private", writeKey: account + "-write-private", nextResource: 70, resources: make(map[int64]supplier.Service)}
	compiledSupplierState.Unlock()
	w := request(f.h, http.MethodPost, "/api/v1/admin/suppliers", f.input(account), f.root, "")
	expect(t, w, http.StatusCreated)
	if strings.Contains(w.Body.String(), "-private") {
		t.Fatal("create leaked named credentials")
	}
	return int64(data(t, w)["id"].(float64))
}
func (f *compiledSupplierFixture) update(account string, version int64) map[string]any {
	in := f.input(account)
	delete(in, "secure_config")
	in["expected_version"] = version
	in["expected_schema_version"] = compiledSupplierSchema
	return in
}
func compiledSupplierReject(t *testing.T, status int, body string) {
	t.Helper()
	if status != http.StatusConflict && status != http.StatusBadRequest {
		t.Fatalf("expected safe configuration rejection, got %d: %s", status, body)
	}
}
func (f *compiledSupplierFixture) rejected(t *testing.T, method, path string, input any) {
	t.Helper()
	w := request(f.h, method, path, input, f.root, "")
	compiledSupplierReject(t, w.Code, w.Body.String())
}
func (f *compiledSupplierFixture) listAccount(t *testing.T, id int64) map[string]any {
	t.Helper()
	w := request(f.h, http.MethodGet, "/api/v1/admin/suppliers", nil, f.root, "")
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), "-private") {
		t.Fatal("account list leaked credentials")
	}
	for _, entry := range listData(t, w) {
		row := entry
		if int64(row["id"].(float64)) == id {
			return row
		}
	}
	t.Fatalf("account %d missing from list", id)
	return nil
}
func (f *compiledSupplierFixture) importAndPublish(t *testing.T, id int64, slug string) int64 {
	t.Helper()
	f.supplierID = id
	w := request(f.h, "GET", fmt.Sprintf("/api/v1/admin/suppliers/%d/products/73", id), nil, f.root, "")
	expect(t, w, 200)
	product := data(t, w)
	in := store.SupplierImportInput{UpstreamProductID: 73, ExpectedSupplierVersion: int64(f.listAccount(t, id)["version"].(float64)), ExpectedUpstreamRevision: product["revision"].(string), Name: "Independent VM", Slug: slug, BillingCycle: "monthly", PriceCents: 2500, MaximumCostCents: 1700}
	pid := f.importProduct(t, in)
	f.publish(t, pid, slug)
	return pid
}

func TestCompiledSupplierAccountConfigurationAndIsolation(t *testing.T) {
	f := newCompiledSupplierFixture(t)
	a, b := f.account(t, "alpha"), f.account(t, "beta")
	path := fmt.Sprintf("/api/v1/admin/suppliers/%d", a)
	providers := request(f.h, "GET", "/api/v1/admin/supplier-providers", nil, f.root, "")
	expect(t, providers, 200)
	found := false
	for _, entry := range listData(t, providers) {
		row := entry
		if row["id"] == compiledSupplierProvider {
			found = true
			descriptor := row["descriptor"].(map[string]any)
			if descriptor["instance_mode"] != "supplier" || descriptor["schema_version"] != float64(compiledSupplierSchema) || len(descriptor["fields"].([]any)) != 5 {
				t.Fatalf("incomplete provider schema: %#v", descriptor)
			}
		}
	}
	if !found {
		t.Fatal("compiled supplier missing from provider API")
	}
	for _, id := range []int64{a, b} {
		w := request(f.h, "POST", fmt.Sprintf("/api/v1/admin/suppliers/%d/catalog", id), nil, f.root, "")
		expect(t, w, 200)
		if len(listData(t, w)) != 1 {
			t.Fatal("wrong independent catalogue")
		}
		row := f.listAccount(t, id)
		if row["schema_version"] != float64(compiledSupplierSchema) || row["configuration_status"] != "valid" {
			t.Fatalf("account configuration not ready: %#v", row)
		}
		present := row["secure_fields"].(map[string]any)
		if present["catalog_key"] != true || present["write_key"] != true {
			t.Fatal("missing named credential presence", present)
		}
	}
	// Credential changes cannot create another account for the same upstream identity.
	duplicate := f.input("alpha")
	duplicate["config"].(map[string]any)["timeout"] = 60
	duplicate["secure_config"] = map[string]string{"catalog_key": "other-read-private", "write_key": "other-write-private"}
	f.rejected(t, "POST", "/api/v1/admin/suppliers", duplicate)
	for key, replacement := range map[string]any{"realm": "retarget", "account": "retarget", "timeout": 60} {
		in := f.update("alpha", 1)
		in["config"].(map[string]any)[key] = replacement
		f.rejected(t, "PUT", path, in)
	}
	// SQL writes cannot bypass the same immutable account binding either.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE supplier_accounts SET config=jsonb_set(config,'{account}','\"retarget\"'::jsonb) WHERE id=$1", a); err == nil {
		t.Fatal("database allowed an existing supplier account to be retargeted")
	}
	// Credentials are authenticated to the local account ID, even for one implementation.
	var first, second []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT secure_fields FROM supplier_accounts WHERE id=$1", a).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT secure_fields FROM supplier_accounts WHERE id=$1", b).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(first, []byte("-private")) || bytes.Equal(first, second) {
		t.Fatal("named credentials were not independently encrypted")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE supplier_accounts SET secure_fields=$2::jsonb WHERE id=$1", b, first); err != nil {
		t.Fatal(err)
	}
	f.rejected(t, "POST", fmt.Sprintf("/api/v1/admin/suppliers/%d/catalog", b), nil)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE supplier_accounts SET secure_fields=$2::jsonb WHERE id=$1", b, second); err != nil {
		t.Fatal(err)
	}
	// Fields are also bound independently: one credential cannot be copied into
	// another slot on the same account.
	var fields map[string][]byte
	if err := json.Unmarshal(first, &fields); err != nil {
		t.Fatal(err)
	}
	fields["catalog_key"], fields["write_key"] = fields["write_key"], fields["catalog_key"]
	swapped, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE supplier_accounts SET secure_fields=$2::jsonb WHERE id=$1", a, swapped); err != nil {
		t.Fatal(err)
	}
	f.rejected(t, "POST", path+"/catalog", nil)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE supplier_accounts SET secure_fields=$2::jsonb WHERE id=$1", a, first); err != nil {
		t.Fatal(err)
	}
	// Rotate just the read key. The omitted write key must remain available.
	in := f.update("alpha", 1)
	in["secure_config"] = map[string]string{"catalog_key": "alpha-rotated-private"}
	expect(t, request(f.h, "PUT", path, in, f.root, ""), 200)
	compiledSupplierState.Lock()
	compiledSupplierState.accounts[f.realm+"/alpha"].catalogKey = "alpha-rotated-private"
	compiledSupplierState.Unlock()
	expect(t, request(f.h, "POST", path+"/catalog", nil, f.root, ""), 200)
	f.rejected(t, "PUT", path, in)
	pid := f.importAndPublish(t, a, "compiled-rotated")
	_, _ = f.purchase(t, pid, "compiled-rotated-purchase")
	job := f.claim(t)
	if job == nil {
		t.Fatal("missing delivery job")
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	state, _, lease := f.state(t, job)
	if state != "succeeded" || lease != nil {
		t.Fatal("rotation lost the untouched write key", state, lease)
	}
	var leaks int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE payload::text LIKE '%-private%'").Scan(&leaks); err != nil || leaks != 0 {
		t.Fatal("audit leaked named credentials", leaks, err)
	}
}

func TestCompiledSupplierSchemaValidationAndSafeStop(t *testing.T) {
	f := newCompiledSupplierFixture(t)
	for _, change := range []func(map[string]any){
		func(in map[string]any) { in["schema_version"] = 1 },
		func(in map[string]any) { in["config"].(map[string]any)["undeclared"] = "noise" },
		func(in map[string]any) { in["config"].(map[string]any)["catalog_key"] = "public-private" },
		func(in map[string]any) { delete(in["secure_config"].(map[string]string), "write_key") },
	} {
		in := f.input("invalid")
		change(in)
		f.rejected(t, "POST", "/api/v1/admin/suppliers", in)
	}
	id := f.account(t, "schema")
	path := fmt.Sprintf("/api/v1/admin/suppliers/%d", id)
	stale := f.update("schema", 1)
	stale["expected_schema_version"] = 1
	f.rejected(t, "PUT", path, stale)
	clear := f.update("schema", 1)
	clear["clear_secure_fields"] = []string{"write_key"}
	f.rejected(t, "PUT", path, clear)
	// A newer stored schema must not be silently read through an older binary.
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE supplier_accounts DISABLE TRIGGER supplier_account_identity_immutable"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE supplier_accounts SET schema_version=99 WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE supplier_accounts ENABLE TRIGGER supplier_account_identity_immutable"); err != nil {
		t.Fatal(err)
	}
	row := f.listAccount(t, id)
	if row["configuration_status"] == "valid" {
		t.Fatal("newer schema was treated as compatible")
	}
	f.rejected(t, "POST", path+"/catalog", nil)
	var before []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT secure_fields FROM supplier_accounts WHERE id=$1", id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	f.db.ProvisionEncryptionKey = nil
	expect(t, request(f.h, "PUT", path, supplierDisableInput(1), f.root, ""), 200)
	var active bool
	var version, schema int
	var after []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT active,version,schema_version,secure_fields FROM supplier_accounts WHERE id=$1", id).Scan(&active, &version, &schema, &after); err != nil {
		t.Fatal(err)
	}
	if active || version != 2 || schema != 99 || !bytes.Equal(before, after) {
		t.Fatal("safe stop rewrote incompatible configuration")
	}
}

func TestCompiledSupplierMissingImplementationOnlyStops(t *testing.T) {
	f := newCompiledSupplierFixture(t)
	id := f.account(t, "missing")
	path := fmt.Sprintf("/api/v1/admin/suppliers/%d", id)
	// Model a previous binary's now-removed provider while retaining its data.
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE supplier_accounts DISABLE TRIGGER supplier_account_identity_immutable"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE supplier_accounts SET provider='removed_compiled_fixture' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE supplier_accounts ENABLE TRIGGER supplier_account_identity_immutable"); err != nil {
		t.Fatal(err)
	}
	row := f.listAccount(t, id)
	if row["configuration_status"] != "implementation_missing" {
		t.Fatal("removed implementation status missing", row)
	}
	f.rejected(t, "POST", path+"/catalog", nil)
	in := f.update("missing", 1)
	in["provider"] = "removed_compiled_fixture"
	in["active"] = false
	f.rejected(t, "PUT", path, in)
	var before string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT (to_jsonb(a)-'active'-'version'-'updated_at')::text FROM supplier_accounts a WHERE id=$1", id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	f.db.ProvisionEncryptionKey = nil
	expect(t, request(f.h, "PUT", path, supplierDisableInput(1), f.root, ""), 200)
	var after string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT (to_jsonb(a)-'active'-'version'-'updated_at')::text FROM supplier_accounts a WHERE id=$1", id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("stopping a removed implementation rewrote its configuration")
	}
	f.rejected(t, "PUT", path, supplierDisableInput(1))
}

func TestCompiledSupplierAccountAuditRollback(t *testing.T) {
	f := newCompiledSupplierFixture(t)
	id := f.account(t, "audit")
	path := fmt.Sprintf("/api/v1/admin/suppliers/%d", id)
	var original []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT secure_fields FROM supplier_accounts WHERE id=$1", id).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_compiled_supplier_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='supplier.saved' THEN RAISE EXCEPTION 'injected supplier audit failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_compiled_supplier_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_compiled_supplier_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/suppliers", f.input("audit-new"), f.root, ""), 500)
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM supplier_accounts").Scan(&count); err != nil || count != 1 {
		t.Fatal("audit failure left created account", count, err)
	}
	in := f.update("audit", 1)
	in["secure_config"] = map[string]string{"write_key": "audit-new-private"}
	expect(t, request(f.h, "PUT", path, in, f.root, ""), 500)
	var version int
	var current []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT version,secure_fields FROM supplier_accounts WHERE id=$1", id).Scan(&version, &current); err != nil {
		t.Fatal(err)
	}
	if version != 1 || !bytes.Equal(original, current) {
		t.Fatal("audit failure committed credential rotation")
	}
}

func TestCompiledSupplierRuntimeBindingSyncAndRenewal(t *testing.T) {
	f := newCompiledSupplierFixture(t)
	a, b := f.account(t, "runtime-a"), f.account(t, "runtime-b")
	first := f.importAndPublish(t, a, "compiled-runtime-a")
	sid, _ := f.purchase(t, first, "compiled-runtime-purchase-a")
	create := f.claim(t)
	if create == nil {
		t.Fatal("missing first delivery")
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *create); err != nil {
		t.Fatal(err)
	}
	state, svc, lease := f.state(t, create)
	if state != "succeeded" || svc != "active" || lease != nil {
		t.Fatal(state, svc, lease)
	}
	// The same numeric upstream resource is legal on a distinct supplier account.
	second := f.importAndPublish(t, b, "compiled-runtime-b")
	otherSid, _ := f.purchase(t, second, "compiled-runtime-purchase-b")
	other := f.claim(t)
	if other == nil {
		t.Fatal("missing second delivery")
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *other); err != nil {
		t.Fatal(err)
	}
	otherState, _, _ := f.state(t, other)
	if otherState != "succeeded" {
		t.Fatal("resource identity crossed supplier accounts", otherState)
	}
	var resource, otherResource string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT upstream_id FROM services WHERE id=$1", sid).Scan(&resource); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT upstream_id FROM services WHERE id=$1", otherSid).Scan(&otherResource); err != nil {
		t.Fatal(err)
	}
	if resource != "71" || otherResource != resource {
		t.Fatal("fixture failed to exercise scoped resource identity", resource, otherResource)
	}
	// Store dispatch follows the service snapshot even while another account is selected in this fixture.
	f.supplierID = a
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/actions", sid), map[string]any{"action": "sync"}, f.root, "compiled-runtime-sync"), 202)
	syncJob := f.claim(t)
	if syncJob == nil || syncJob.Action != "sync" {
		t.Fatal("missing typed sync job", syncJob)
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *syncJob); err != nil {
		t.Fatal(err)
	}
	state, _, lease = f.state(t, syncJob)
	if state != "succeeded" || lease != nil {
		t.Fatal("sync did not use supplier runtime", state, lease)
	}
	// Legacy callbacks must not become a cross-provider lifecycle write channel.
	localID := strconv.FormatInt(sid, 10)
	nonce := "compiled-provider-callback"
	values := url.Values{
		"id": {localID}, "host_id": {resource}, "domainstatus": {"Active"}, "domain": {"cross-provider.invalid"}, "rand_str": {nonce},
		"username": {"root"}, "password": {"cross-provider-private"}, "dedicatedip": {"198.51.100.20"},
		"assignedips": {"198.51.100.21"}, "port": {"22"}, "os": {"Debian"}, "os_url": {"debian"}, "nextduedate": {"1900000000"},
	}
	values.Set("signature", supplierCallbackSign(localID, supplierCallbackToken(sid), nonce))
	if w := supplierCallbackRequest(f.h, "/api/host/sync", values); w.Code < 400 {
		t.Fatal("legacy callback accepted another provider", w.Body.String())
	}
	var callbackDomain string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT domain FROM services WHERE id=$1", sid).Scan(&callbackDomain); err != nil || callbackDomain == "cross-provider.invalid" {
		t.Fatal("rejected callback changed another provider service", err)
	}
	var before, afterPayment, after time.Time
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", sid).Scan(&before); err != nil {
		t.Fatal(err)
	}
	renewal := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", sid), map[string]any{}, f.customer, "compiled-runtime-renew")
	expect(t, renewal, 201)
	invoice := int64(data(t, renewal)["invoice_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "compiled-runtime-renew-pay"), 201)
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", sid).Scan(&afterPayment); err != nil || !afterPayment.Equal(before) {
		t.Fatal("local payment extended supplier due before delivery", err)
	}
	renewJob := f.claim(t)
	if renewJob == nil || renewJob.Action != "renew" {
		t.Fatal("missing supplier renewal", renewJob)
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *renewJob); err != nil {
		t.Fatal(err)
	}
	state, _, lease = f.state(t, renewJob)
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", sid).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || lease != nil || !after.After(before) {
		t.Fatal("typed renewal did not finish", state, lease, before, after)
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *renewJob); err == nil {
		t.Fatal("completed renewal was dispatched again")
	}
	compiledSupplierState.Lock()
	aControl := *compiledSupplierState.accounts[f.realm+"/runtime-a"]
	bControl := *compiledSupplierState.accounts[f.realm+"/runtime-b"]
	compiledSupplierState.Unlock()
	if aControl.creates != 1 || bControl.creates != 1 || aControl.syncs < 2 || aControl.renews != 1 || bControl.renews != 0 {
		t.Fatal("provider/account dispatch mismatch", aControl.creates, bControl.creates, aControl.syncs, aControl.renews, bControl.renews)
	}
}

func TestCompiledSupplierUnknownWriteNeverAutomaticallyResends(t *testing.T) {
	f := newCompiledSupplierFixture(t)
	id := f.account(t, "unknown")
	pid := f.importAndPublish(t, id, "compiled-unknown")
	sid, _ := f.purchase(t, pid, "compiled-unknown-purchase")
	compiledSupplierState.Lock()
	compiledSupplierState.accounts[f.realm+"/unknown"].unknown = true
	compiledSupplierState.Unlock()
	job := f.claim(t)
	if job == nil {
		t.Fatal("missing unknown delivery")
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	state, svc, lease := f.state(t, job)
	if state != "waiting_verification" || svc != "provisioning" || lease == nil || *lease != job.ID {
		t.Fatal(state, svc, lease)
	}
	rotation := f.update("unknown", 1)
	rotation["secure_config"] = map[string]string{"write_key": "unknown-rotated-private"}
	f.rejected(t, "PUT", fmt.Sprintf("/api/v1/admin/suppliers/%d", id), rotation)
	f.rejected(t, "PUT", fmt.Sprintf("/api/v1/admin/suppliers/%d", id), supplierDisableInput(1))
	if err := f.db.ProcessProvisionJob(t.Context(), *job); err == nil {
		t.Fatal("uncertain write was replayed")
	}
	second := f.importAndPublish(t, id, "compiled-unknown-second")
	_, _ = f.purchase(t, second, "compiled-unknown-blocked")
	blocked := f.claim(t)
	if blocked == nil {
		t.Fatal("missing blocked delivery")
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *blocked); err != nil {
		t.Fatal(err)
	}
	state, _, _ = f.state(t, blocked)
	if state != "pending" {
		t.Fatal("unknown account lease did not block next delivery", state)
	}
	if err := f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	compiledSupplierState.Lock()
	calls := compiledSupplierState.accounts[f.realm+"/unknown"].creates
	compiledSupplierState.Unlock()
	if calls != 1 {
		t.Fatal("unknown supplier write was resent", calls)
	}
	verifyPath := fmt.Sprintf("/api/v1/admin/services/%d/supplier-verification", sid)
	input := store.SupplierVerificationInput{JobID: job.ID, ExpectedAttempts: job.Attempts, Outcome: "not_applied", Note: "核验独立供应商无资源及账单"}
	f.rejected(t, "POST", verifyPath, input)
	input.AbsenceAcknowledged = true
	expect(t, request(f.h, "POST", verifyPath, input, f.root, ""), 200)
	state, svc, lease = f.state(t, job)
	if state != "failed" || svc != "failed" || lease != nil {
		t.Fatal("explicit verification did not release supplier lease", state, svc, lease)
	}
}

// Authorization must be evaluated again after the supplier row becomes
// available: holding a session's row lock does not stop its clock from expiring.
func TestCompiledSupplierLockWaitRejectsExpiredSession(t *testing.T) {
	for _, operation := range []string{"update", "disable_only", "probe"} {
		t.Run(operation, func(t *testing.T) {
			f := newCompiledSupplierFixture(t)
			id := f.account(t, "expired")
			path := fmt.Sprintf("/api/v1/admin/suppliers/%d", id)
			method, key := http.MethodPut, ""
			var input any = f.update("expired", 1)
			input.(map[string]any)["secure_config"] = map[string]string{"write_key": "expired-replacement-private"}
			switch operation {
			case "disable_only":
				input = supplierDisableInput(1)
			case "probe":
				method, path, key = http.MethodPost, path+"/test-connection", "compiled-expired-probe"
				input = map[string]any{"expected_version": 1, "expected_schema_version": compiledSupplierSchema}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			blocker, err := f.db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			var blockerPID int
			if err = blocker.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			if _, err = blocker.Exec(ctx, "SELECT id FROM supplier_accounts WHERE id=$1 FOR UPDATE", id); err != nil {
				t.Fatal(err)
			}
			var before string
			if err = f.db.Pool.QueryRow(ctx, "SELECT to_jsonb(a)::text FROM supplier_accounts a WHERE id=$1", id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			credential := tokenHash(strings.SplitN(f.root, "=", 2)[1])
			if _, err = f.db.Pool.Exec(ctx, "UPDATE sessions SET expires_at=clock_timestamp()+interval '2 seconds' WHERE token_hash=$1", credential); err != nil {
				t.Fatal(err)
			}
			type response struct {
				code int
				body string
			}
			result := make(chan response, 1)
			go func() {
				w := request(f.h, method, path, input, f.root, key)
				result <- response{w.Code, w.Body.String()}
			}()
			// Assert the authenticated request actually reached this row lock before
			// letting its session expire; an early middleware rejection is insufficient.
			for {
				var waiting bool
				if err = f.db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))", blockerPID).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case response := <-result:
					t.Fatalf("request did not reach supplier row lock: %d %s", response.code, response.body)
				case <-ctx.Done():
					t.Fatal("request did not enter supplier row wait")
				case <-time.After(10 * time.Millisecond):
				}
			}
			for {
				var expired bool
				if err = f.db.Pool.QueryRow(ctx, "SELECT expires_at<=clock_timestamp() FROM sessions WHERE token_hash=$1", credential).Scan(&expired); err != nil {
					t.Fatal(err)
				}
				if expired {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("session did not naturally expire")
				case <-time.After(20 * time.Millisecond):
				}
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case response := <-result:
				if response.code != http.StatusUnauthorized {
					t.Fatalf("expired session mutated supplier after waiting: %d %s", response.code, response.body)
				}
			case <-ctx.Done():
				t.Fatal("expired supplier request did not finish after releasing its row lock")
			}
			var after string
			if err = f.db.Pool.QueryRow(ctx, "SELECT to_jsonb(a)::text FROM supplier_accounts a WHERE id=$1", id).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatal("expired session changed supplier state or credential ciphertext")
			}
			var probes, operations int
			if err = f.db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action IN ('supplier.connection_test_started','supplier.connection_test_completed')").Scan(&probes); err != nil {
				t.Fatal(err)
			}
			if err = f.db.Pool.QueryRow(ctx, "SELECT count(*) FROM mutation_operations WHERE scope='admin:supplier.connection_test'").Scan(&operations); err != nil {
				t.Fatal(err)
			}
			compiledSupplierState.Lock()
			control := *compiledSupplierState.accounts[f.realm+"/expired"]
			compiledSupplierState.Unlock()
			if probes != 0 || operations != 0 || control.catalogs != 0 || control.products != 0 || control.creates != 0 || control.syncs != 0 || control.renews != 0 {
				t.Fatal("expired request contacted upstream or reserved a connection probe", probes, operations, control.catalogs)
			}
		})
	}
}

func TestCompiledSupplierLegacyAccountUpgradeKeepsBinding(t *testing.T) {
	f := supplierSetup(t)
	path := fmt.Sprintf("/api/v1/admin/suppliers/%d", f.supplierID)
	var before, after, named []byte
	var format int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT credentials_encrypted,credential_format FROM supplier_accounts WHERE id=$1", f.supplierID).Scan(&before, &format); err != nil {
		t.Fatal(err)
	}
	if format != 0 || len(before) == 0 {
		t.Fatal("legacy API creation did not retain the original encrypted format")
	}
	upgrade := map[string]any{
		"name": "Legacy account with named credentials", "provider": supplier.ProviderZJMF, "active": true,
		"schema_version": 1, "expected_schema_version": 1, "expected_version": 1,
		"config":        map[string]any{"endpoint": f.remote.URL, "username": "account"},
		"secure_config": map[string]string{"secret": "supplier-private-password"},
	}
	expect(t, request(f.h, "PUT", path, upgrade, f.root, ""), 200)
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT credentials_encrypted,secure_fields,credential_format FROM supplier_accounts WHERE id=$1", f.supplierID).Scan(&after, &named, &format); err != nil {
		t.Fatal(err)
	}
	if format != 1 || !bytes.Equal(before, after) || bytes.Contains(named, []byte("supplier-private-password")) {
		t.Fatal("named credential upgrade rewrote the legacy artifact or exposed a secret")
	}
	// Products imported before the configuration upgrade still dispatch against
	// the same immutable account and ZJMF protocol through the worker.
	f.publish(t, f.pid, "compiled-upgraded-legacy")
	_, _ = f.purchase(t, f.pid, "compiled-upgraded-legacy-purchase")
	job := f.claim(t)
	if job == nil {
		t.Fatal("missing upgraded legacy delivery")
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	state, _, lease := f.state(t, job)
	if state != "succeeded" || lease != nil || f.creates.Load() != 1 || f.pays.Load() != 1 {
		t.Fatal("legacy binding was broken by named credential upgrade", state, lease)
	}
	// The explicit format is authoritative. A malformed new payload must not
	// silently fall back to the old, still-decryptable password blob.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE supplier_accounts SET secure_fields='{}'::jsonb WHERE id=$1", f.supplierID); err != nil {
		t.Fatal(err)
	}
	w := request(f.h, "POST", path+"/catalog", nil, f.root, "")
	compiledSupplierReject(t, w.Code, w.Body.String())
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE supplier_accounts SET secure_fields=$2::jsonb WHERE id=$1", f.supplierID, named); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/catalog", nil, f.root, ""), 200)
}

// Keep JSON encoding honest: provider credentials cannot accidentally become
// serializable when the account configuration evolves.
func TestCompiledSupplierAccountJSONHidesNamedCredentials(t *testing.T) {
	raw, err := json.Marshal(supplier.Account{Provider: compiledSupplierProvider, Values: map[string]any{"realm": "test", "account": "one"}, Secrets: map[string]string{"catalog_key": "read-private", "write_key": "write-private"}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("-private")) {
		t.Fatal("supplier Account marshals named credential values")
	}
}
