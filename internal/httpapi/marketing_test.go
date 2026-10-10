package httpapi

import (
	"fmt"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func marketingCountRows(t *testing.T, db *store.Store, table string) int {
	t.Helper()
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestMarketingCampaignLifecycle(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "marketing-admin", "super_admin")
	customerID, _ := user(t, db, h, "marketing-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET email='marketing-customer@example.test',email_verified=true WHERE id=$1`, customerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO customer_profiles(customer_id,receive_marketing,country,language) VALUES($1,true,'CN','zh_CN')`, customerID); err != nil {
		t.Fatal(err)
	}

	payload := map[string]any{
		"title":    "新客户活动",
		"mode":     "customer",
		"channel":  "inbox",
		"subject":  "本月活动",
		"body":     "您好，{{display_name}}，欢迎查看本月活动。",
		"audience": map[string]any{"mode": "customer", "country": "CN", "language": "zh_CN"},
	}
	created := request(h, "POST", "/api/v1/admin/marketing", payload, admin, "marketing-create-1")
	expect(t, created, 201)
	id := int64(data(t, created)["id"].(float64))
	if data(t, created)["version"] != float64(1) {
		t.Fatalf("unexpected draft: %v", data(t, created))
	}
	// Replaying a create does not create a second campaign.
	replayed := request(h, "POST", "/api/v1/admin/marketing", payload, admin, "marketing-create-1")
	expect(t, replayed, 200)
	if data(t, replayed)["replayed"] != true {
		t.Fatalf("expected idempotent replay: %v", data(t, replayed))
	}
	if got := marketingCountRows(t, db, "marketing_campaigns"); got != 1 {
		t.Fatalf("campaign count=%d", got)
	}

	count := request(h, "POST", "/api/v1/admin/marketing/audience-count", map[string]any{"mode": "customer", "country": "CN"}, admin, "")
	expect(t, count, 200)
	if data(t, count)["total"] != float64(1) {
		t.Fatalf("audience count: %v", data(t, count))
	}

	update := map[string]any{
		"title": "新客户活动（更新）", "mode": "customer", "channel": "inbox", "subject": "本月活动",
		"body": "更新后的正文", "audience": map[string]any{"mode": "customer", "country": "CN"}, "expected_version": 1,
	}
	updated := request(h, "PATCH", fmt.Sprintf("/api/v1/admin/marketing/%d", id), update, admin, "marketing-update-1")
	expect(t, updated, 200)
	if data(t, updated)["version"] != float64(2) {
		t.Fatalf("unexpected updated version: %v", data(t, updated))
	}
	conflict := request(h, "PATCH", fmt.Sprintf("/api/v1/admin/marketing/%d", id), update, admin, "marketing-update-2")
	expect(t, conflict, 409)

	sent := request(h, "POST", fmt.Sprintf("/api/v1/admin/marketing/%d/send", id), map[string]any{"expected_version": 2}, admin, "marketing-send-1")
	expect(t, sent, 202)
	if data(t, sent)["recipient_count"] != float64(1) || data(t, sent)["status"] != "completed" {
		t.Fatalf("unexpected send result: %v", data(t, sent))
	}
	if got := marketingCountRows(t, db, "marketing_campaign_recipients"); got != 1 {
		t.Fatalf("recipient count=%d", got)
	}
	if got := marketingCountRows(t, db, "admin_inbox_messages"); got != 1 {
		t.Fatalf("inbox count=%d", got)
	}
	repeat := request(h, "POST", fmt.Sprintf("/api/v1/admin/marketing/%d/send", id), map[string]any{"expected_version": 2}, admin, "marketing-send-1")
	expect(t, repeat, 200)
	if data(t, repeat)["replayed"] != true || marketingCountRows(t, db, "marketing_campaign_recipients") != 1 {
		t.Fatalf("send was not idempotent: %v", data(t, repeat))
	}
}
