package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestChannelInstancesCatalogueAndSecretLifecycle(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "channel-admin", "super_admin")
	catalogue := request(h, http.MethodGet, "/api/v1/admin/channel-catalogue", nil, admin, "")
	expect(t, catalogue, http.StatusOK)
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(catalogue.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	descriptors := envelope.Data
	if len(descriptors) < 4 {
		t.Fatalf("catalogue too small: %d", len(descriptors))
	}
	instances := request(h, http.MethodGet, "/api/v1/admin/channel-instances?category=sms", nil, admin, "")
	expect(t, instances, http.StatusOK)
	rows := listData(t, instances)
	if len(rows) != 1 || rows[0]["id"] != "sms-default" {
		t.Fatalf("unexpected migrated SMS instance: %#v", rows)
	}
	legacy := request(h, http.MethodPut, "/api/v1/admin/channel-instances/sms-default", map[string]any{
		"display_name": "默认短信", "enabled": false, "config": map[string]any{"endpoint": "https://sms.example.test/legacy"}, "expected_version": int64(rows[0]["version"].(float64)),
	}, admin, "")
	expect(t, legacy, http.StatusOK)
	var endpoint string
	if err := db.Pool.QueryRow(context.Background(), "SELECT endpoint FROM sms_settings WHERE id=1").Scan(&endpoint); err != nil || endpoint != "https://sms.example.test/legacy" {
		t.Fatalf("legacy SMS settings were not synchronized: %q / %v", endpoint, err)
	}
	created := request(h, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{
		"category": "sms", "implementation_id": "generic_http_json", "display_name": "测试短信", "enabled": false,
		"config": map[string]any{"endpoint": "https://sms.example.test/send"}, "secret": "test-channel-secret",
	}, admin, "")
	expect(t, created, http.StatusCreated)
	item := data(t, created)
	if item["secret_configured"] != true {
		t.Fatalf("secret was not recorded: %#v", item)
	}
	var sealed []byte
	if err := db.Pool.QueryRow(context.Background(), "SELECT secret_ciphertext FROM channel_instances WHERE id=$1", item["id"]).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if len(sealed) == 0 || bytes.Contains(sealed, []byte("test-channel-secret")) {
		t.Fatal("channel secret was stored in plaintext")
	}
	updated := request(h, http.MethodPut, "/api/v1/admin/channel-instances/"+item["id"].(string), map[string]any{
		"display_name": "测试短信二", "enabled": false, "config": map[string]any{"endpoint": "https://sms.example.test/v2"}, "expected_version": int64(item["version"].(float64)),
	}, admin, "")
	expect(t, updated, http.StatusOK)
	if data(t, updated)["display_name"] != "测试短信二" {
		t.Fatal(updated.Body.String())
	}
	stale := request(h, http.MethodPut, "/api/v1/admin/channel-instances/"+item["id"].(string), map[string]any{"display_name": "过期", "expected_version": int64(item["version"].(float64))}, admin, "")
	expect(t, stale, http.StatusConflict)
	deleted := request(h, http.MethodDelete, "/api/v1/admin/channel-instances/"+item["id"].(string), map[string]any{"expected_version": int64(data(t, updated)["version"].(float64))}, admin, "")
	expect(t, deleted, http.StatusOK)
}

func TestPaymentIntentBindsSelectedChannelInstance(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "channel-payment-admin", "super_admin")
	uid, customer := user(t, db, h, "channel-payment-customer", "customer")
	invalid := request(h, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{
		"category": "payment", "implementation_id": "epay", "display_name": "缺少密钥", "enabled": true,
		"config": map[string]any{"api_url": "http://127.0.0.1:82", "pid": "1000", "types": []string{"alipay"}},
	}, admin, "")
	expect(t, invalid, http.StatusConflict)
	created := request(h, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{
		"id": "payment-second-test", "category": "payment", "implementation_id": "epay", "display_name": "第二支付商户", "enabled": true,
		"config": map[string]any{"api_url": "http://127.0.0.1:82", "pid": "1000", "types": []string{"alipay"}},
		"secret": "second-payment-secret",
	}, admin, "")
	expect(t, created, http.StatusCreated)
	instanceID := data(t, created)["id"].(string)
	invoiceID := manualInvoiceCreate(t, h, admin, manualInvoiceInput(uid), "channel-payment-invoice")
	intent := request(h, http.MethodPost, fmt.Sprintf("/api/v1/invoices/%d/payments", invoiceID), map[string]any{
		"provider": "epay_alipay", "channel_instance_id": instanceID, "expected_version": 1,
	}, customer, "channel-payment-intent")
	expect(t, intent, http.StatusCreated)
	var bound string
	var snapshot []byte
	if err := db.Pool.QueryRow(t.Context(), "SELECT channel_instance_id,credential_snapshot FROM payment_transactions WHERE provider_payment_id=$1", data(t, intent)["provider_payment_id"].(string)).Scan(&bound, &snapshot); err != nil {
		t.Fatal(err)
	}
	if bound != instanceID || len(snapshot) == 0 {
		t.Fatalf("payment was not bound to selected instance: id=%q snapshot=%d", bound, len(snapshot))
	}
}
