package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

func TestFinancialSettingsContracts(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "financial-settings-admin", "super_admin")
	created := request(h, "POST", "/api/v1/admin/currencies", map[string]any{
		"code": "USD", "name": "美元", "prefix": "$", "suffix": "", "format": "prefix_amount", "exchange_rate": 7.2, "active": true,
	}, admin, "currency-create-1")
	if created.Code != 201 {
		t.Fatalf("create currency: %d %s", created.Code, created.Body.String())
	}
	var createdBody struct {
		Data struct {
			ID      int64 `json:"id"`
			Version int64 `json:"version"`
		} `json:"data"`
	}
	if err := json.NewDecoder(created.Body).Decode(&createdBody); err != nil {
		t.Fatal(err)
	}
	id, version := createdBody.Data.ID, createdBody.Data.Version
	page := request(h, "GET", "/api/v1/admin/currencies?limit=10&offset=0", nil, admin, "")
	if page.Code != 200 || !containsJSON(page.Body.Bytes(), "USD") {
		t.Fatalf("currency page: %d %s", page.Code, page.Body.String())
	}
	stale := request(h, "PATCH", fmt.Sprintf("/api/v1/admin/currencies/%d", id), map[string]any{
		"code": "USD", "name": "美元更新", "prefix": "$", "suffix": "", "format": "prefix_amount", "exchange_rate": 7.3, "active": true, "expected_version": version,
	}, admin, "currency-update-1")
	if stale.Code != 200 {
		t.Fatalf("currency update: %d %s", stale.Code, stale.Body.String())
	}
	conflict := request(h, "PATCH", fmt.Sprintf("/api/v1/admin/currencies/%d", id), map[string]any{
		"code": "USD", "name": "覆盖", "prefix": "$", "suffix": "", "format": "prefix_amount", "exchange_rate": 7.4, "active": true, "expected_version": version,
	}, admin, "currency-update-stale")
	if conflict.Code != 409 {
		t.Fatalf("currency stale update: %d %s", conflict.Code, conflict.Body.String())
	}
	for path, values := range map[string][2]any{
		"/api/v1/admin/finance-settings":  {map[string]any{"topup_enabled": true, "min_topup_cents": 100, "max_topup_cents": 10000, "max_balance_cents": 0, "require_active_order": false, "expected_version": 1}, "finance-settings-1"},
		"/api/v1/admin/invoice-settings":  {map[string]any{"enabled": true, "rate_percent": 1.5, "expected_version": 1}, "invoice-settings-1"},
		"/api/v1/admin/contract-settings": {map[string]any{"enabled": true, "expected_version": 1}, "contract-settings-1"},
	} {
		w := request(h, "PUT", path, values[0], admin, values[1].(string))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	shipping := request(h, "POST", "/api/v1/admin/invoice-shipping-methods", map[string]any{"name": "顺丰", "price_cents": 1200, "active": true}, admin, "shipping-create-1")
	if shipping.Code != 201 {
		t.Fatalf("shipping create: %d %s", shipping.Code, shipping.Body.String())
	}
	var shippingBody struct {
		Data struct {
			ID      int64 `json:"id"`
			Version int64 `json:"version"`
		} `json:"data"`
	}
	if err := json.NewDecoder(shipping.Body).Decode(&shippingBody); err != nil {
		t.Fatal(err)
	}
	invoice := request(h, "GET", "/api/v1/admin/invoice-settings", nil, admin, "")
	if invoice.Code != 200 || !containsJSON(invoice.Body.Bytes(), "顺丰") {
		t.Fatalf("shipping list: %d %s", invoice.Code, invoice.Body.String())
	}
	removed := request(h, "DELETE", fmt.Sprintf("/api/v1/admin/invoice-shipping-methods/%d", shippingBody.Data.ID), map[string]any{"expected_version": shippingBody.Data.Version}, admin, "shipping-delete-1")
	if removed.Code != 200 {
		t.Fatalf("shipping delete: %d %s", removed.Code, removed.Body.String())
	}
	credit := request(h, "GET", "/api/v1/admin/credit-settings", nil, admin, "")
	if credit.Code != 200 {
		t.Fatalf("credit settings: %d %s", credit.Code, credit.Body.String())
	}
}

func containsJSON(body []byte, needle string) bool { return bytes.Contains(body, []byte(needle)) }
