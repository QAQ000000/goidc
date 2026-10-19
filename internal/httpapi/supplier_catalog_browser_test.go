package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
)

// Opt-in browser acceptance uses a disposable test schema and loopback supplier.
// The browser must import products 7 and 8 at 150%, then buy product 7 annually
// with Japan, three quantity units and the required private purchase field.
func TestSupplierCatalogBrowserFixture(t *testing.T) {
	ready, finish := browserFixturePaths(t)
	origin := os.Getenv("ZJMFGO_BROWSER_ORIGIN")
	if origin == "" {
		origin = "http://localhost:3102"
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		t.Fatal("supplier catalog browser origin must be a loopback HTTP origin")
	}
	address := os.Getenv("ZJMFGO_BROWSER_LISTEN_ADDR")
	if address == "" {
		address = "127.0.0.1:18102"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("supplier catalog browser listen address must use 127.0.0.1")
	}
	f := supplierConfigurableSetupWithImport(t, false)
	section := catalogGroupInput("supplier-browser-section", nil)
	section.Name = "供应商导入"
	sectionID := catalogGroupID(t, f.h, f.root, "section", section)
	category := catalogGroupInput("supplier-browser-category", &sectionID)
	category.Name = "浏览器导入分组"
	categoryID := catalogGroupID(t, f.h, f.root, "category", category)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", f.uid), map[string]any{"amount_cents": 100000}, f.root, "supplier-catalog-browser-credit"), 201)

	// New copies both keys into the Store. Preserve the private-field key used
	// by the shared fixture, so the actual browser checkout can encrypt values.
	handler := New(f.db, config.Config{PublicOrigin: origin,
		ProvisionEncryptionKey:     append([]byte(nil), f.db.ProvisionEncryptionKey...),
		PurchaseFieldEncryptionKey: append([]byte(nil), f.db.PurchaseFieldEncryptionKey...),
	}, slog.Default()).Handler()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	stopWorker := startBrowserProvisionWorker(t, f.db, nil)
	defer stopWorker()
	var schema string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT current_schema()::text").Scan(&schema); err != nil {
		t.Fatal(err)
	}

	// Only the separate owner-readable file contains test login credentials;
	// the ready manifest and publishable browser evidence contain none.
	credentials := ready + ".credentials.env"
	file, err := os.OpenFile(credentials, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(credentials) })
	_, writeErr := file.WriteString("GOIDC_BROWSER_ADMIN_EMAIL=configurable-catalog-root@example.test\nGOIDC_BROWSER_CUSTOMER_EMAIL=configurable-catalog-customer@example.test\nGOIDC_BROWSER_PASSWORD=integration-password\n")
	if err := errors.Join(writeErr, file.Close()); err != nil {
		t.Fatal(err)
	}
	writeBrowserJSON(t, ready, map[string]any{
		"api_origin": server.URL, "web_origin": origin, "schema": schema,
		"supplier_id": f.supplierID, "customer_id": f.uid,
		"credentials_file": credentials, "local_category_id": categoryID,
		"local_category_name":  section.Name + " / " + category.Name,
		"upstream_product_ids": []int64{7, 8}, "sale_percentage": "150",
		"purchase": map[string]any{"upstream_product_id": 7, "billing_cycle": "annually", "choice_label": "日本", "quantity": 3, "private_field_label": "实例密钥"},
	})
	waitBrowserFinish(t, ready, finish, 20*time.Minute)
	stopWorker()

	var imports, products, services, unfinished, leases int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM supplier_catalog_imports),
 (SELECT count(*) FROM products p JOIN supplier_product_bindings b ON b.product_id=p.id WHERE p.active AND p.category_id=$1 AND b.supplier_id=$2),
 (SELECT count(*) FROM services WHERE status='active' AND billing_cycle='annually' AND upstream_id<>''),
 (SELECT count(*) FROM provision_jobs WHERE status<>'succeeded'),
 (SELECT count(*) FROM supplier_accounts WHERE running_job_id IS NOT NULL)`, categoryID, f.supplierID).Scan(&imports, &products, &services, &unfinished, &leases); err != nil {
		t.Fatal(err)
	}
	if imports != 2 || products != 2 || services != 1 || unfinished != 0 || leases != 0 || f.creates.Load() != 1 || f.pays.Load() != 1 {
		t.Fatalf("incomplete batch import browser acceptance: imports=%d products=%d annual_services=%d unfinished=%d leases=%d creates=%d pays=%d", imports, products, services, unfinished, leases, f.creates.Load(), f.pays.Load())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, resource := range f.resources {
		if resource.pid != 7 || resource.cycle != "annually" || resource.choice != "602" || resource.quantity != "3" || resource.password == "" {
			t.Fatal("browser selections did not reach the upstream resource")
		}
	}
	t.Log("supplier catalog browser acceptance: batch import, local category, annual checkout, selected configuration, private field, upstream payment and active sync")
}
