package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestAdministratorAccountHistoryUpgrade181IsReadOnlyAndSuperOnly(t *testing.T) {
	db := adminOrderReceiptSchema178(t)
	for _, name := range []string{"179_admin_order_mutation_receipts.sql", "180_customer_group_pricing.sql", "181_auth_login_portal.sql"} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	actor, customer := topUpFixture(t, db)
	var owner int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role) VALUES('historical-commerce-owner','historical-owner@test.invalid','never-expose-password-proof','历史员工','super_admin') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	f := notificationFixture{db: db, ctx: t.Context()}
	resources := lifecycleFixtureResources(t, f, owner, "administrator-audit")
	var secondOrder int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash,notes) VALUES('historical-second',$1,123,'historical-second','fixture','never-expose-private-note') RETURNING id`, owner).Scan(&secondOrder); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,note) VALUES($1,'topup',100,100,'manual','never-expose-ledger-note')`, owner)
	f.exec(t, `UPDATE services SET username='never-expose-service-user',resource_info='{"console_url":"https://never-expose-console.test"}' WHERE id=$1`, resources.service)
	for _, id := range []int64{actor, customer} {
		f.exec(t, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 day')`, fmt.Sprint("history-session-", id), id)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	session := fmt.Sprint("history-session-", actor)
	before := supplierLifecycleUpgradeFacts(t, db)
	result, err := db.AdministratorAccountHistory(t.Context(), actor, owner, session, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result["read_only"] != true || result["account"].(map[string]any)["id"] != owner {
		t.Fatal("missing historical account identity or read-only marker")
	}
	for name, total := range map[string]int64{"orders": 2, "invoices": 1, "services": 1, "ledger": 1} {
		page := result[name].(map[string]any)
		if page["total"] != total || len(page["items"].([]map[string]any)) != 1 {
			t.Fatalf("unexpected historical %s page: %#v", name, page)
		}
	}
	if result["orders"].(map[string]any)["items"].([]map[string]any)[0]["id"] != secondOrder {
		t.Fatal("history is not ordered by descending immutable ID")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"never-expose", "password", "checkout_url", "console_url", "supplier_capabilities", "config_snapshot", "purchase_fields"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("historical audit exposed %q", forbidden)
		}
	}
	pageTwo, err := db.AdministratorAccountHistory(t.Context(), actor, owner, session, 1, 1)
	if err != nil || pageTwo["orders"].(map[string]any)["items"].([]map[string]any)[0]["id"] != resources.order {
		t.Fatal("history pagination did not preserve older records", err)
	}
	if _, err := db.ReadAdminOrder(t.Context(), actor, resources.order); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("audit access expanded the ordinary order scope", err)
	}
	if _, err := db.ReadInvoice(t.Context(), actor, resources.invoice, InvoiceReadOptions{Admin: true}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("audit access expanded the ordinary invoice scope", err)
	}
	for kind, id := range map[string]int64{"order": resources.order, "invoice": resources.invoice, "service": resources.service} {
		err := db.WithTx(t.Context(), func(tx pgx.Tx) error { return LockAdminResourceCustomer(t.Context(), tx, actor, kind, id) })
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("historical %s gained customer mutation scope: %v", kind, err)
		}
	}
	if supplierLifecycleUpgradeFacts(t, db) != before {
		t.Fatal("historical audit changed business facts")
	}
	for _, input := range []struct {
		actor   int64
		session string
	}{{actor, "revoked"}, {customer, fmt.Sprint("history-session-", customer)}} {
		_, err := db.AdministratorAccountHistory(t.Context(), input.actor, owner, input.session, 10, 0)
		var denied *AccountBusinessError
		if !errors.As(err, &denied) || denied.Status != 403 {
			t.Fatal("history accepted a non-super or invalid session", err)
		}
	}
	if _, err := db.AdministratorAccountHistory(t.Context(), actor, customer, session, 10, 0); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("customer account accepted as administrator history target", err)
	}
	f.exec(t, `UPDATE administrators SET status='disabled' WHERE id=$1`, actor)
	if _, err := db.AdministratorAccountHistory(t.Context(), actor, owner, session, 10, 0); err == nil {
		t.Fatal("disabled super administrator retained audit access")
	}
}
