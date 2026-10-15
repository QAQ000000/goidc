package httpapi

import (
	"fmt"
	"testing"
)

func financeSettingsPayload(enabled bool, min, max, balance int64, requireOrder bool, version int64) map[string]any {
	return map[string]any{
		"topup_enabled":        enabled,
		"min_topup_cents":      min,
		"max_topup_cents":      max,
		"max_balance_cents":    balance,
		"require_active_order": requireOrder,
		"expected_version":     version,
	}
}

// TestTopUpHonorsFinanceSettings proves the admin finance switches are read by
// the top-up path instead of being display-only.
func TestTopUpHonorsFinanceSettings(t *testing.T) {
	db, h := testServer(t)
	customerID, _ := user(t, db, h, "finance-settings-customer", "customer")
	_, root := user(t, db, h, "finance-settings-root", "super_admin")
	balancePath := fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID)

	// Defaults from migration 110 allow top-ups unchanged.
	expect(t, request(h, "POST", balancePath, map[string]any{"amount_cents": 2000}, root, "finance-default-topup"), 201)

	// topup_enabled=false rejects even a valid amount with a distinct code.
	expect(t, request(h, "PUT", "/api/v1/admin/finance-settings", financeSettingsPayload(false, 100, 10000, 0, false, 1), root, "finance-disable-topup"), 200)
	w := request(h, "POST", balancePath, map[string]any{"amount_cents": 2000}, root, "finance-disabled-topup")
	expect(t, w, 409)
	if code := errorData(t, w)["code"]; code != "topup_disabled" {
		t.Fatalf("disabled topup code=%v", code)
	}
	var ledger int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM balance_ledger WHERE user_id=$1 AND idempotency_key='finance-disabled-topup'", customerID).Scan(&ledger); err != nil || ledger != 0 {
		t.Fatalf("disabled topup wrote ledger rows: %d err=%v", ledger, err)
	}

	// Re-enable with explicit bounds and a balance ceiling.
	expect(t, request(h, "PUT", "/api/v1/admin/finance-settings", financeSettingsPayload(true, 500, 5000, 6000, false, 2), root, "finance-enable-topup"), 200)

	w = request(h, "POST", balancePath, map[string]any{"amount_cents": 100}, root, "finance-below-min")
	expect(t, w, 409)
	if code := errorData(t, w)["code"]; code != "topup_below_minimum" {
		t.Fatalf("below-min code=%v", code)
	}
	w = request(h, "POST", balancePath, map[string]any{"amount_cents": 6000}, root, "finance-above-max")
	expect(t, w, 409)
	if code := errorData(t, w)["code"]; code != "topup_above_maximum" {
		t.Fatalf("above-max code=%v", code)
	}
	// Within range and below the ceiling: allowed.
	expect(t, request(h, "POST", balancePath, map[string]any{"amount_cents": 500}, root, "finance-allowed-topup"), 201)

	// Balance is now 2500; a top-up that would cross 6000 is refused.
	w = request(h, "POST", balancePath, map[string]any{"amount_cents": 4000}, root, "finance-balance-limit")
	expect(t, w, 409)
	if code := errorData(t, w)["code"]; code != "topup_balance_limit" {
		t.Fatalf("balance-limit code=%v", code)
	}
	expect(t, request(h, "POST", balancePath, map[string]any{"amount_cents": 3500}, root, "finance-balance-at-limit"), 201)

	// Idempotent replay of an already-committed top-up still succeeds even
	// though the settings no longer permit the amount under the ceiling.
	expect(t, request(h, "PUT", "/api/v1/admin/finance-settings", financeSettingsPayload(true, 500, 5000, 6000, false, 3), root, "finance-enable-topup-2"), 200)
	replay := request(h, "POST", balancePath, map[string]any{"amount_cents": 3500}, root, "finance-balance-at-limit")
	expect(t, replay, 200)
	if data(t, replay)["replayed"] != true {
		t.Fatalf("replay flag missing: %s", replay.Body.String())
	}
}

func TestTopUpRequiresActiveOrderWhenConfigured(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "active-order-root", "super_admin")
	expect(t, request(h, "PUT", "/api/v1/admin/finance-settings", financeSettingsPayload(true, 100, 10000, 0, true, 1), root, "active-order-settings"), 200)
	balancePath := func(customerID int64) string { return fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID) }

	// No order at all: the switch keeps refusing with its dedicated code.
	emptyID, _ := user(t, db, h, "active-order-empty-customer", "customer")
	w := request(h, "POST", balancePath(emptyID), map[string]any{"amount_cents": 1000}, root, "active-order-missing")
	expect(t, w, 409)
	if code := errorData(t, w)["code"]; code != "topup_active_order_required" {
		t.Fatalf("active-order code=%v", code)
	}

	// A paid order (payment settled, not manually reviewed) now satisfies the
	// legacy addfunds_require_order switch.
	paidID, _ := user(t, db, h, "active-order-paid-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES($1,$2,'paid',1000,$1,'fixture')`, "active-order-paid", paidID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", balancePath(paidID), map[string]any{"amount_cents": 1000}, root, "active-order-paid-allowed"), 201)

	// A cancelled order does not satisfy the rule.
	cancelledID, _ := user(t, db, h, "active-order-cancelled-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES($1,$2,'cancelled',1000,$1,'fixture')`, "active-order-cancelled", cancelledID); err != nil {
		t.Fatal(err)
	}
	w = request(h, "POST", balancePath(cancelledID), map[string]any{"amount_cents": 1000}, root, "active-order-cancelled-attempt")
	expect(t, w, 409)
	if code := errorData(t, w)["code"]; code != "topup_active_order_required" {
		t.Fatalf("cancelled-only order code=%v", code)
	}

	// A soft-deleted paid order is ignored, mirroring legacy delete_time=0.
	deletedID, _ := user(t, db, h, "active-order-deleted-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash,deleted_at) VALUES($1,$2,'paid',1000,$1,'fixture',now())`, "active-order-deleted", deletedID); err != nil {
		t.Fatal(err)
	}
	w = request(h, "POST", balancePath(deletedID), map[string]any{"amount_cents": 1000}, root, "active-order-deleted-attempt")
	expect(t, w, 409)
	if code := errorData(t, w)["code"]; code != "topup_active_order_required" {
		t.Fatalf("deleted paid order code=%v", code)
	}

	// An active order (promoted by manual review) still satisfies the rule.
	activeID, _ := user(t, db, h, "active-order-active-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES($1,$2,'active',1000,$1,'fixture')`, "active-order-active", activeID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", balancePath(activeID), map[string]any{"amount_cents": 1000}, root, "active-order-active-allowed"), 201)
}
