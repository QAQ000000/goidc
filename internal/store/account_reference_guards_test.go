package store

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func requireAccountReferenceViolation(t *testing.T, err error, kind, table, column string) {
	t.Helper()
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23503" ||
		pgerr.Message != kind+" account required for "+table+"."+column ||
		pgerr.TableName != table || pgerr.ColumnName != column ||
		pgerr.ConstraintName != "account_reference_"+column {
		t.Fatalf("expected typed %s reference violation for %s.%s; got %#v (%v)", kind, table, column, pgerr, err)
	}
}

func TestAccountSeparationTypedReferenceGuards(t *testing.T) {
	db := isolatedTestStore(t)
	var customer, administrator int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name) VALUES('guard-client','guard-client@test.invalid','hash','客户') RETURNING id`).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO administrators(username,email,password_hash,display_name,role) VALUES('guard-staff','guard-staff@test.invalid','hash','管理员','super_admin') RETURNING id`).Scan(&administrator); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Pool.Query(t.Context(), `SELECT c.relname,t.tgargs FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid JOIN pg_class c ON c.oid=t.tgrelid WHERE p.proname='enforce_account_reference' AND c.relnamespace=current_schema()::regnamespace AND NOT t.tgisinternal ORDER BY c.relname,t.tgname`)
	if err != nil {
		t.Fatal(err)
	}
	type guard struct{ table, column, kind string }
	var guards []guard
	seen := map[string]string{}
	for rows.Next() {
		var table string
		var args []byte
		if err := rows.Scan(&table, &args); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		parts := bytes.Split(args, []byte{0})
		if len(parts) != 3 || len(parts[2]) != 0 {
			rows.Close()
			t.Fatalf("invalid typed-reference trigger on %s: %q", table, args)
		}
		g := guard{table, string(parts[0]), string(parts[1])}
		guards = append(guards, g)
		seen[g.table+"."+g.column] = g.kind
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	// These are independent security boundaries, including the less prominent
	// profile, credit, renewal, contract and staff-review paths.
	for field, kind := range map[string]string{
		"carts.user_id": "customer", "orders.user_id": "customer", "invoices.user_id": "customer",
		"services.user_id": "customer", "tickets.user_id": "customer", "customer_contacts.customer_id": "customer",
		"customer_profiles.customer_id": "customer", "customer_group_members.customer_id": "customer",
		"customer_files.customer_id": "customer", "payment_transactions.user_id": "customer", "balance_ledger.user_id": "customer",
		"service_navigation_preferences.user_id": "customer", "service_configuration_changes.user_id": "customer",
		"traffic_orders.user_id": "customer", "promo_code_redemptions.user_id": "customer", "promo_upgrade_redemptions.user_id": "customer",
		"invoice_profiles.user_id": "customer", "invoice_addresses.user_id": "customer", "invoice_applications.user_id": "customer",
		"contracts.customer_id": "customer", "certification_profiles.user_id": "customer", "certification_submissions.user_id": "customer",
		"affiliate_accounts.user_id": "customer", "affiliate_attributions.user_id": "customer", "affiliate_commission_entries.referred_user_id": "customer",
		"customer_credit_accounts.user_id": "customer", "customer_credit_events.user_id": "customer", "credit_invoice_payments.user_id": "customer",
		"credit_repayments.user_id": "customer", "balance_topups.user_id": "customer", "balance_topup_settlements.user_id": "customer",
		"external_refunds.user_id": "customer", "payment_close_operations.user_id": "customer", "payment_close_verification_requests.user_id": "customer",
		"payment_close_anomaly_refunds.user_id": "customer", "auto_renewal_invoice_sources.user_id": "customer", "ticket_ratings.customer_id": "customer",
		"admin_ticket_creation_receipts.customer_id": "customer", "sales_settlement_items.customer_id": "customer", "mutation_operations.customer_id": "customer",
		"users.sales_owner_id": "administrator", "admin_ticket_departments.user_id": "administrator", "tickets.assignee_id": "administrator",
		"tickets.created_by": "administrator", "orders.created_by": "administrator", "orders.deleted_by": "administrator",
		"ticket_notes.author_id": "administrator", "ticket_transfer_logs.actor_id": "administrator", "ticket_ratings.staff_id": "administrator",
		"customer_files.uploader_id": "administrator", "content_articles.author_id": "administrator", "content_images.uploader_id": "administrator",
		"downloads.author_id": "administrator", "promo_codes.created_by": "administrator", "order_settings_receipts.actor_id": "administrator",
		"invoice_applications.reviewer_id": "administrator", "invoice_application_documents.uploader_id": "administrator", "certification_reviews.reviewer_id": "administrator",
		"customer_credit_events.actor_id": "administrator", "credit_repayments.actor_id": "administrator", "credit_invoice_payments.refund_actor_id": "administrator",
		"external_refunds.requested_by": "administrator", "external_refunds.reviewed_by": "administrator", "payment_close_operations.actor_id": "administrator",
		"payment_close_verification_requests.actor_id": "administrator", "payment_close_verification_decisions.actor_id": "administrator",
		"sales_settlement_items.sales_owner_id": "administrator", "sales_settlements.actor_id": "administrator",
		"report_expenses.recorded_by": "administrator", "report_expense_voids.recorded_by": "administrator",
	} {
		if seen[field] != kind {
			t.Errorf("missing %s boundary for %s (got %q)", kind, field, seen[field])
		}
	}
	for _, g := range guards {
		t.Run(g.table+"/"+g.column, func(t *testing.T) {
			wrong := administrator
			if g.kind == "administrator" {
				wrong = customer
			} else if g.kind != "customer" {
				t.Fatalf("unknown account kind %q", g.kind)
			}
			// The reference trigger must reject the actual mutation before unrelated
			// required fields or association checks, with the intended SQLSTATE.
			_, err := db.Pool.Exec(t.Context(), "INSERT INTO "+pgx.Identifier{g.table}.Sanitize()+"("+pgx.Identifier{g.column}.Sanitize()+") VALUES($1)", wrong)
			requireAccountReferenceViolation(t, err, g.kind, g.table, g.column)
		})
	}
	// Shared credentials, audit actors and inbox recipients must continue to
	// reference either concrete type through the immutable principal.
	if seen["payment_reconciliation_checks.actor_id"] != "" {
		t.Fatal("manual payment query actors must accept both account kinds")
	}
	for n, id := range []int64{customer, administrator} {
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 day')`, strings.Repeat(fmt.Sprint(n+1), 64), id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO audit_events(actor_id,action,entity_type,entity_id) VALUES($1,'reference.test','account',$1::bigint::text)`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO admin_inbox_messages(user_id,subject,body) VALUES($1,'共享通知','消息')`, id); err != nil {
			t.Fatal(err)
		}
		// The actor can be the paying customer or an authorized administrator;
		// the payment's commercial ownership remains the actual customer.
		var invoice, payment int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,amount_cents) VALUES($1,$2,100) RETURNING id`, fmt.Sprintf("SHARED-QUERY-%d", n), customer).Scan(&invoice); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents) VALUES('epay_alipay',$1,$2,$3,100) RETURNING id`, fmt.Sprintf("shared-query-%d", n), invoice, customer).Scan(&payment); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO payment_reconciliation_checks(payment_id,actor_id,source) VALUES($1,$2,'manual')`, payment, id); err != nil {
			t.Fatal("manual payment query rejected valid account actor", err)
		}
	}
}

func TestAccountSeparationTypedReferencesPreserveHistoricalOwnership(t *testing.T) {
	db := adminOrderReceiptSchema178(t)
	for _, name := range []string{"179_admin_order_mutation_receipts.sql", "180_customer_group_pricing.sql", "181_auth_login_portal.sql"} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	var customer, administrator int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name) VALUES('legacy-guard-client','legacy-guard-client@test.invalid','hash','客户') RETURNING id`).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role) VALUES('legacy-guard-staff','legacy-guard-staff@test.invalid','hash','管理员','super_admin') RETURNING id`).Scan(&administrator); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO carts(user_id) VALUES($1)`, administrator); err != nil {
		t.Fatal(err)
	}
	var ticket int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO tickets(ticket_no,user_id,subject,body,assignee_id,created_by) VALUES('legacy-typed-guard',$1,'历史引用','历史内容',$1,$1) RETURNING id`, customer).Scan(&ticket); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE carts SET user_id=user_id,version=version+1 WHERE user_id=$1`, administrator); err != nil {
		t.Fatal("unchanged historical administrator ownership rejected", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE tickets SET assignee_id=assignee_id,created_by=created_by,subject='保留历史处理人' WHERE id=$1`, ticket); err != nil {
		t.Fatal("unchanged historical staff reference rejected", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO carts(user_id) VALUES($1)`, customer); err != nil {
		t.Fatal("customer cart rejected", err)
	}
	_, err := db.Pool.Exec(t.Context(), `UPDATE carts SET user_id=$1 WHERE user_id=$2`, administrator, customer)
	requireAccountReferenceViolation(t, err, "customer", "carts", "user_id")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE tickets SET assignee_id=$1,created_by=$1 WHERE id=$2`, administrator, ticket); err != nil {
		t.Fatal("administrator staff reference rejected", err)
	}
	_, err = db.Pool.Exec(t.Context(), `UPDATE tickets SET assignee_id=$1 WHERE id=$2`, customer, ticket)
	requireAccountReferenceViolation(t, err, "administrator", "tickets", "assignee_id")
	_, err = db.Pool.Exec(t.Context(), `UPDATE tickets SET created_by=$1 WHERE id=$2`, customer, ticket)
	requireAccountReferenceViolation(t, err, "administrator", "tickets", "created_by")
}
