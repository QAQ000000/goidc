package httpapi

import (
	"fmt"
	"testing"
)

func TestAdminTicketRulesVersionedIdempotentCRUD(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "ticket-rules-root", "super_admin")

	page := request(h, "GET", "/api/v1/admin/ticket-statuses?limit=10&offset=0", nil, root, "")
	expect(t, page, 200)
	if got := data(t, page)["total"].(float64); got < 5 {
		t.Fatalf("builtin ticket statuses=%v", got)
	}
	statusBody := map[string]any{"slug": "escalated", "title": "升级处理", "sort_order": 20, "active": true}
	created := request(h, "POST", "/api/v1/admin/ticket-statuses", statusBody, root, "ticket-status-create")
	expect(t, created, 200)
	createdData := data(t, created)
	id := int64(createdData["id"].(float64))
	if int64(createdData["version"].(float64)) != 1 {
		t.Fatalf("created version=%v", createdData["version"])
	}
	replay := request(h, "POST", "/api/v1/admin/ticket-statuses", statusBody, root, "ticket-status-create")
	expect(t, replay, 200)
	if !data(t, replay)["replayed"].(bool) || int64(data(t, replay)["id"].(float64)) != id {
		t.Fatalf("status replay=%s", replay.Body.String())
	}
	conflictBody := map[string]any{"slug": "other", "title": "不同请求", "sort_order": 20, "active": true}
	expect(t, request(h, "POST", "/api/v1/admin/ticket-statuses", conflictBody, root, "ticket-status-create"), 409)

	path := fmt.Sprintf("/api/v1/admin/ticket-statuses/%d", id)
	update := map[string]any{"title": "升级处理（更新）", "sort_order": 21, "active": true, "expected_version": 1}
	updated := request(h, "PATCH", path, update, root, "ticket-status-update")
	expect(t, updated, 200)
	if int64(data(t, updated)["version"].(float64)) != 2 {
		t.Fatalf("updated version=%v", data(t, updated)["version"])
	}
	expect(t, request(h, "PATCH", path, update, root, "ticket-status-update"), 200)
	expect(t, request(h, "PATCH", path, update, root, "ticket-status-stale"), 409)

	rule := request(h, "POST", "/api/v1/admin/ticket-rules", map[string]any{"blocked_keywords": []string{"退款"}, "auto_reply": "请提供订单号", "active": true}, root, "ticket-rule-create")
	expect(t, rule, 201)
	ruleData := data(t, rule)
	ruleID := int64(ruleData["id"].(float64))
	rulePath := fmt.Sprintf("/api/v1/admin/ticket-rules/%d", ruleID)
	ruleUpdate := map[string]any{"blocked_keywords": []string{"退款", "发票"}, "auto_reply": "请提供订单号", "active": true, "expected_version": 1}
	expect(t, request(h, "PATCH", rulePath, ruleUpdate, root, "ticket-rule-update"), 200)
	expect(t, request(h, "PATCH", rulePath, ruleUpdate, root, "ticket-rule-stale"), 409)
	expect(t, request(h, "DELETE", rulePath, map[string]any{"expected_version": 2}, root, "ticket-rule-delete"), 200)
	expect(t, request(h, "DELETE", rulePath, map[string]any{"expected_version": 2}, root, "ticket-rule-delete"), 200)

	var audits int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action IN ('ticket.status.created','ticket.status.updated','ticket.rule.created','ticket.rule.updated','ticket.rule.deleted')`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits < 5 {
		t.Fatalf("audit events=%d", audits)
	}
}
