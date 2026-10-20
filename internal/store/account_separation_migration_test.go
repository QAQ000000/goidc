package store

import (
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"strings"
	"testing"
)

func TestAccountSeparationUpgrade181PreservesHistory(t *testing.T) {
	db := adminOrderReceiptSchema178(t)
	for _, name := range []string{"179_admin_order_mutation_receipts.sql", "180_customer_group_pricing.sql", "181_auth_login_portal.sql"} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	var administrator, customer int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role,balance_cents) VALUES('separate-root','separate@test.invalid','historical-password','历史管理员','super_admin',1234) RETURNING id`).Scan(&administrator); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,sales_owner_id) VALUES('separate-client','client@test.invalid','customer-password','客户',$1) RETURNING id`, administrator).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO carts(user_id) VALUES($1),($2)`, administrator, customer); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 day')`, strings.Repeat("f", 64), administrator); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload) VALUES($1,'account.test','user',$2::bigint::text,'{}')`, administrator, customer); err != nil {
		t.Fatal(err)
	}
	checksums := supplierExpiryUpgradeMigrations(t, db, "181_auth_login_portal.sql")
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	var hash string
	var balance int64
	var owner int64
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE role<>'customer'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("administrators remained in users", count, err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT password_hash,balance_cents FROM administrators WHERE id=$1`, administrator).Scan(&hash, &balance); err != nil || hash != "historical-password" || balance != 1234 {
		t.Fatal("administrator changed", err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT sales_owner_id FROM users WHERE id=$1`, customer).Scan(&owner); err != nil || owner != administrator {
		t.Fatal("sales assignment changed", err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM carts WHERE user_id=ANY($1)`, []int64{administrator, customer}).Scan(&count); err != nil || count != 2 {
		t.Fatal("history lost", err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='account.test'`, administrator).Scan(&count); err != nil || count != 1 {
		t.Fatal("audit lost", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES('forbidden-administrator',$1,'pending',0,'blocked-admin','hash')`, administrator); err == nil {
		t.Fatal("administrator became customer owner")
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role) VALUES('wrong-role','wrong@test.invalid','hash','错误','super_admin')`); err == nil {
		t.Fatal("users accepted administrator")
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if checksums != supplierExpiryUpgradeMigrations(t, db, "181_auth_login_portal.sql") {
		t.Fatal("historical checksums changed")
	}
	if err := db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAccountSeparationPrincipalsAndIndependentUniqueness(t *testing.T) {
	db := isolatedTestStore(t)
	var a, c int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO administrators(username,email,password_hash,display_name,role) VALUES('same','same@test.invalid','hash','管理员','super_admin') RETURNING id`).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name) VALUES('same','same@test.invalid','hash','客户') RETURNING id`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Fatal("principal IDs collided")
	}
	var n int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM account_principals`).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO users(id,username,email,password_hash,display_name) OVERRIDING SYSTEM VALUE VALUES($1,'collision','collision@test.invalid','hash','错误')`, a); err == nil {
		t.Fatal("cross-kind principal collision allowed")
	} else {
		var violation *pgconn.PgError
		if !errors.As(err, &violation) || violation.Code != "23505" || violation.ConstraintName != "account_principals_pkey" {
			t.Fatalf("wrong collision rejection: %v", err)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE account_principals SET kind='customer' WHERE id=$1`, a); err == nil {
		t.Fatal("principal kind mutable")
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE administrators SET id=id+999 WHERE id=$1`, a); err == nil {
		t.Fatal("administrator ID mutable")
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO carts(user_id) VALUES($1)`, a); err == nil {
		t.Fatal("administrator got customer cart")
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO users(username,email,password_hash,display_name) VALUES('same','same@test.invalid','hash','客户') ON CONFLICT(username) DO UPDATE SET display_name=excluded.display_name`); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM account_principals`).Scan(&n); err != nil || n != 2 {
		t.Fatal("upsert leaked a principal", n, err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE account_identities SET status='disabled' WHERE id=$1`, a); err == nil {
		t.Fatal("mixed projection writable")
	}
}
