package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/config"
)

func TestReportExpenseValidation(t *testing.T) {
	valid := reportExpenseInput{100, "2024-02-29", " hosting ", " bank ", " test://receipt ", " note "}
	if err := validateReportExpense(&valid); err != nil || valid.Category != "hosting" {
		t.Fatalf("valid expense rejected: %+v err=%v", valid, err)
	}
	for _, edit := range []func(*reportExpenseInput){func(v *reportExpenseInput) { v.AmountCents = 0 }, func(v *reportExpenseInput) { v.AmountCents = 100000000001 }, func(v *reportExpenseInput) { v.OccurredOn = "2024-02-30" }, func(v *reportExpenseInput) { v.Category = "" }, func(v *reportExpenseInput) { v.Source = "" }, func(v *reportExpenseInput) { v.EvidenceRef = "" }, func(v *reportExpenseInput) { v.Note = "invalid\x00note" }, func(v *reportExpenseInput) { v.Category = strings.Repeat("中", 81) }} {
		in := valid
		edit(&in)
		if validateReportExpense(&in) == nil {
			t.Fatalf("invalid expense accepted: %+v", in)
		}
	}
}

func TestReportExpensesPermissionsAppendOnlyReplayAndAudit(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "expense-root", "super_admin")
	staffID, staff := user(t, db, h, "expense-reader", "admin")
	_, customer := user(t, db, h, "expense-customer", "customer")
	path := "/api/v1/admin/reports/expenses"
	in := map[string]any{"amount_cents": 1234, "occurred_on": "2024-02-01", "category": "机房", "source": "银行", "evidence_ref": "test://expense-receipt", "note": "manual cost"}
	expect(t, request(h, "POST", path, in, "", "expense-no-session"), 401)
	expect(t, request(h, "POST", path, in, customer, "expense-customer"), 403)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['reports.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", path, in, staff, "expense-reader-denied"), 403)
	w := request(h, "POST", path, in, root, "expense-create-0001")
	expect(t, w, 201)
	id := int64(data(t, w)["id"].(float64))
	w = request(h, "POST", path, in, root, "expense-create-0001")
	expect(t, w, 200)
	if data(t, w)["replayed"] != true {
		t.Fatalf("missing replay: %s", w.Body.String())
	}
	in["amount_cents"] = 1235
	expect(t, request(h, "POST", path, in, root, "expense-create-0001"), 409)
	in["amount_cents"] = 1234
	w = request(h, "GET", path+"?year=2024&month=2", nil, staff, "")
	expect(t, w, 200)
	items := data(t, w)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["recorded_by_name"] != "expense-root" || data(t, w)["manageable"] != false {
		t.Fatalf("expense ledger: %s", w.Body.String())
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	w = request(h, "GET", path+"?year=2024", nil, staff, "")
	expect(t, w, 403)
	if errorData(t, w)["code"] != "global_expenses_forbidden" {
		t.Fatalf("global scope error: %s", w.Body.String())
	}
	for _, q := range []string{fmt.Sprintf("UPDATE report_expenses SET amount_cents=99 WHERE id=%d", id), fmt.Sprintf("DELETE FROM report_expenses WHERE id=%d", id)} {
		if _, err := db.Pool.Exec(t.Context(), q); err == nil {
			t.Fatal("database allowed expense fact mutation")
		}
	}
	voidPath := fmt.Sprintf("%s/%d/void", path, id)
	expect(t, request(h, "POST", voidPath, map[string]any{"expected_version": 9, "note": "wrong version"}, root, "expense-stale-0001"), 409)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			codes <- request(h, "POST", voidPath, map[string]any{"expected_version": 1, "note": "correct duplicate"}, root, fmt.Sprintf("expense-void-%d", n)).Code
		}(n)
	}
	wg.Wait()
	close(codes)
	var success, conflict int
	for code := range codes {
		if code == 201 {
			success++
		} else if code == 409 {
			conflict++
		} else {
			t.Fatalf("unexpected concurrent void status %d", code)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("void concurrency success=%d conflict=%d", success, conflict)
	}
	w = request(h, "GET", path+"?year=2024", nil, root, "")
	expect(t, w, 200)
	item := data(t, w)["items"].([]any)[0].(map[string]any)
	if item["status"] != "voided" || item["version"] != float64(2) || item["void_note"] != "correct duplicate" {
		t.Fatalf("append-only void: %#v", item)
	}
	var facts, voids, audits int
	if err := db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM report_expenses),(SELECT count(*) FROM report_expense_voids),(SELECT count(*) FROM audit_events WHERE action IN ('report_expense.created','report_expense.voided'))").Scan(&facts, &voids, &audits); err != nil || facts != 1 || voids != 1 || audits != 2 {
		t.Fatalf("facts=%d voids=%d audits=%d err=%v", facts, voids, audits, err)
	}
	w = request(h, "GET", "/api/v1/admin/reports/annual-cash?year=2024", nil, root, "")
	expect(t, w, 200)
	if data(t, w)["total"].(map[string]any)["manual_expense_cents"] != float64(0) {
		t.Fatalf("void expense still in active costs: %s", w.Body.String())
	}
	// Role revocation applies to an otherwise valid idempotent replay.
	for _, query := range []string{"UPDATE admin_roles SET permissions=ARRAY['reports.view','finance.manage'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", "UPDATE users SET customer_scope='all' WHERE id=$1"} {
		if _, err := db.Pool.Exec(t.Context(), query, staffID); err != nil {
			t.Fatal(err)
		}
	}
	in["evidence_ref"] = "test://expense-staff-receipt"
	w = request(h, "POST", path, in, staff, "expense-staff-create")
	expect(t, w, 201)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['reports.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", path, in, staff, "expense-staff-create"), 403)
}

func TestReportExpenseConcurrentSourceDedupAndRevokedSessionReplay(t *testing.T) {
	db, h := testServer(t)
	uid, cookie := user(t, db, h, "expense-concurrent-root", "super_admin")
	path := "/api/v1/admin/reports/expenses"
	in := map[string]any{"amount_cents": 100, "occurred_on": "2024-02-01", "category": "hosting", "source": "bank", "evidence_ref": "bank://same-source", "note": "concurrent"}
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			codes <- request(h, "POST", path, in, cookie, fmt.Sprintf("expense-concurrent-%d", n)).Code
		}(n)
	}
	wg.Wait()
	close(codes)
	success, conflict := 0, 0
	for code := range codes {
		switch code {
		case 201:
			success++
		case 409:
			conflict++
		default:
			t.Fatalf("concurrent create status %d", code)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("dedup success=%d conflict=%d", success, conflict)
	}
	var id int64
	var key string
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM report_expenses").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT idempotency_key FROM mutation_operations WHERE actor_id=$1 AND scope='report-expense'", uid).Scan(&key); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", fmt.Sprintf("%s/%d/void", path, id), map[string]any{"expected_version": 1, "note": "wrong source corrected"}, cookie, "expense-concurrent-void"), 201)
	w := request(h, "POST", path, in, cookie, "expense-after-void")
	expect(t, w, 409)
	if errorData(t, w)["code"] != "report_expense_duplicate" {
		t.Fatal(w.Body.String())
	}
	// Simulate authorization middleware having run just before logout. The
	// transaction itself must reject both a replay and a new write.
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	server := New(db, config.Config{}, nil)
	secret := "zjmfgo_" + strings.Repeat("a", 64)
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO api_keys(user_id,name,prefix,secret_hash,revoked_at) VALUES($1,'expense-revoked','zjmfgo_aaaaaaaaaaaaaaaa',$2,clock_timestamp())`, uid, tokenHash(secret)); err != nil {
		t.Fatal(err)
	}
	for _, bearer := range []string{"", secret} {
		for _, requestKey := range []string{key, "expense-session-new"} {
			body, _ := json.Marshal(in)
			r := httptest.NewRequest("POST", path, bytes.NewReader(body))
			if bearer == "" {
				r.Header.Set("Cookie", cookie)
			} else {
				r.Header.Set("Authorization", "Bearer "+bearer)
			}
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", requestKey)
			r = r.WithContext(context.WithValue(r.Context(), userKey, identity{ID: uid, Role: "super_admin"}))
			out := httptest.NewRecorder()
			server.createReportExpense(out, r)
			expect(t, out, 401)
		}
	}
	var facts, audits, operations int
	if err := db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM report_expenses),(SELECT count(*) FROM audit_events WHERE action IN ('report_expense.created','report_expense.voided')),(SELECT count(*) FROM mutation_operations WHERE scope='report-expense')").Scan(&facts, &audits, &operations); err != nil || facts != 1 || audits != 2 || operations != 2 {
		t.Fatalf("failed/revoked writes escaped rollback facts=%d audits=%d operations=%d err=%v", facts, audits, operations, err)
	}
}
