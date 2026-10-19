package store

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/supplier"
)

func TestSupplierCatalogImportUpgrade174PreservesFixedBindings(t *testing.T) {
	db := supplierExpirySchema171(t)
	for _, name := range []string{supplierExpiryMigration172, "173_notification_deadline_recovery.sql", "174_web_push.sql"} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	var boundary string
	var newTable bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT max(version),to_regclass('supplier_catalog_imports') IS NOT NULL FROM schema_migrations`).Scan(&boundary, &newTable); err != nil || boundary != "174_web_push.sql" || newTable {
		t.Fatal("fixture must start at untouched schema174", boundary, newTable, err)
	}
	supplierLifecycleUpgradeHistory(t, db)
	before := supplierLifecycleUpgradeFacts(t, db)
	oldMigrations := supplierExpiryUpgradeMigrations(t, db, "174_web_push.sql")
	if err := db.CheckSchema(t.Context()); err == nil {
		t.Fatal("new runtime accepted schema174 before import support")
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if before != supplierLifecycleUpgradeFacts(t, db) || oldMigrations != supplierExpiryUpgradeMigrations(t, db, "174_web_push.sql") {
		t.Fatal("import migration changed legacy identities, resources, jobs, money or migration checksums")
	}
	var imported int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM supplier_catalog_imports").Scan(&imported); err != nil || imported != 0 {
		t.Fatal("old fixed bindings were inferred as configurable imports", imported, err)
	}
	var product int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT product_id FROM supplier_product_bindings LIMIT 1").Scan(&product); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents) VALUES($1,'quarterly',3000,0)", product); err == nil {
		t.Fatal("upgrade loosened historical fixed binding price guard")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET billing_cycle='quarterly' WHERE id=$1", product); err == nil {
		t.Fatal("upgrade allowed historical supplier source change")
	}
	if err := db.Migrate(t.Context()); err != nil || before != supplierLifecycleUpgradeFacts(t, db) {
		t.Fatal("repeat migration changed original facts", err)
	}
	if err := db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNotificationUpgradeChecksums(t, db)
}

func TestSupplierMarkupCentsRoundingAndBounds(t *testing.T) {
	for _, tc := range []struct {
		amount, bps, want int64
	}{
		{0, 15000, 0}, {1, 15000, 2}, {3, 15000, 5}, {1001, 15000, 1502}, {1, 1, 0}, {100000000, 10000, 100000000},
	} {
		got, err := SupplierMarkupCents(tc.amount, tc.bps)
		if err != nil || got != tc.want {
			t.Fatal("component rounding mismatch", tc, got, err)
		}
	}
	for _, tc := range [][2]int64{{-1, 10000}, {100000001, 10000}, {100000000, 1000000}, {1, 0}, {1, 1000001}, {9223372036854775807, 1000000}} {
		if _, err := SupplierMarkupCents(tc[0], tc[1]); err == nil {
			t.Fatal("out-of-bounds price accepted", tc)
		}
	}
}

func TestSupplierCatalogImportRejectsZeroOnlyQuantityRange(t *testing.T) {
	product := supplier.Product{ID: 7, Name: "Zero-only quantity", Revision: "test-revision", Currency: "CNY", Supported: true,
		Prices: map[string]supplier.Price{"monthly": {AmountCents: 100}},
		Options: []supplier.Option{{ID: 8, Name: "Quantity", Type: "quantity", Required: true, MinQty: 0, MaxQty: 0,
			Choices: []supplier.Choice{{ID: 9, Name: "Units", MinQty: 0, MaxQty: 0, Prices: map[string]supplier.Price{"monthly": {AmountCents: 10}}}}}},
	}
	db := &Store{}
	if _, reason := db.supplierCatalogImportable(product, 10000); reason != "invalid_quantity_bounds" {
		t.Fatal("zero-only upstream quantity would become locally unbounded", reason)
	}
	product.Options[0].MaxQty = 1
	product.Options[0].Choices[0].MaxQty = 1
	if _, reason := db.supplierCatalogImportable(product, 10000); reason != "" {
		t.Fatal("valid bounded quantity was rejected", reason)
	}
}

func TestSupplierCatalogImportReadTimeoutLeavesNoWrites(t *testing.T) {
	db := isolatedTestStore(t)
	db.ProvisionEncryptionKey = bytes.Repeat([]byte{4}, 32)
	actor, _ := topUpFixture(t, db)
	const session = "catalog-import-timeout-session"
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 hour')", session, actor); err != nil {
		t.Fatal(err)
	}
	var blocked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zjmf_api_login" {
			fmt.Fprint(w, `{"status":200,"jwt":"timeout-private-jwt"}`)
			return
		}
		if r.URL.Path != "/cart/get_product_config" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			return
		}
		if blocked.Load() {
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{"status":200,"data":{"products":{"id":7,"name":"Timeout test","location_version":1,"pay_type":{"pay_type":"recurring"}},"product_pricings":[{"code":"CNY","monthly":"1.00"}],"config_groups":[],"customfields":[],"advanced":[]}}`)
	}))
	defer server.Close()
	active := true
	id, err := db.SaveSupplier(t.Context(), actor, 0, SupplierInput{Name: "Timeout upstream", Provider: supplier.ProviderZJMF, BaseURL: server.URL, Username: "account", Password: "timeout-private-password", Active: &active, SessionHash: session})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithStorageCredential(t.Context(), session, false)
	in := SupplierCatalogImportInput{ExpectedSupplierVersion: 1, PriceMultiplierBPS: 10000, Items: []SupplierCatalogImportItem{{UpstreamProductID: 7}}}
	preview, err := db.PreviewSupplierCatalogImport(ctx, actor, id, in)
	if err != nil || !preview.Items[0].Importable {
		t.Fatal("preview failed", preview, err)
	}
	in.Items[0].ExpectedUpstreamRevision = preview.Items[0].Product.Revision
	blocked.Store(true)
	start := time.Now()
	_, err = db.importSupplierCatalog(ctx, actor, id, "catalog-timeout-retry", in, 20*time.Millisecond)
	if conflictCode(t, err) != "supplier_catalog_timeout" || time.Since(start) > 2*time.Second {
		t.Fatal("read budget did not cancel promptly", err)
	}
	var products, receipts int
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM products),(SELECT count(*) FROM mutation_operations WHERE scope LIKE 'admin:supplier.catalog.import.%')`).Scan(&products, &receipts); err != nil || products != 0 || receipts != 0 {
		t.Fatal("timed out import wrote products or receipt", products, receipts, err)
	}
	blocked.Store(false)
	result, err := db.ImportSupplierCatalog(ctx, actor, id, "catalog-timeout-retry", in)
	if err != nil || len(result.Items) != 1 || result.Items[0].Status != "created" {
		t.Fatal("same key could not safely retry after timeout", result, err)
	}
}

func TestSupplierCalendarUpgrade175PreservesFixedBindings(t *testing.T) {
	db := supplierExpirySchema171(t)
	for _, name := range []string{supplierExpiryMigration172, "173_notification_deadline_recovery.sql", "174_web_push.sql", "175_supplier_catalog_import.sql"} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	supplierLifecycleUpgradeHistory(t, db)
	before := supplierLifecycleUpgradeFacts(t, db)
	checksums := supplierExpiryUpgradeMigrations(t, db, "175_supplier_catalog_import.sql")
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if before != supplierLifecycleUpgradeFacts(t, db) || checksums != supplierExpiryUpgradeMigrations(t, db, "175_supplier_catalog_import.sql") {
		t.Fatal("calendar migration changed historical facts")
	}
	var pid int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT product_id FROM supplier_product_bindings LIMIT 1").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	for _, cycle := range []string{"semiannually", "biennially", "tenly"} {
		if _, err := db.Pool.Exec(t.Context(), "INSERT INTO product_prices(product_id,billing_cycle,price_cents) VALUES($1,$2,100)", pid, cycle); err == nil {
			t.Fatal("fixed binding source-cycle guard loosened", cycle)
		}
	}
	assertNotificationUpgradeChecksums(t, db)
}
