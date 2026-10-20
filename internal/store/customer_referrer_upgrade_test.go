package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type referrerUpgradeFixture struct {
	db                                                                    *Store
	actor, customer, unassigned, ownerA, ownerB, accountA, accountB, rule int64
	commissionInvoice, turnoverInvoice, unpaidCurrent, unpaidNone         int64
}

func referrerSchema186(t *testing.T) referrerUpgradeFixture {
	t.Helper()
	db := retiredFeaturesSchema183(t)
	for _, name := range []string{"184_retire_credit_and_contracts.sql", "185_manual_server_resources.sql", "186_customer_contact_versions.sql"} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	var boundary string
	var exists bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT max(version),to_regclass('invoice_affiliate_attributions') IS NOT NULL FROM schema_migrations").Scan(&boundary, &exists); err != nil || boundary != "186_customer_contact_versions.sql" || exists {
		t.Fatal("referral upgrade fixture must originate at genuine schema186", boundary, exists, err)
	}
	f := referrerUpgradeFixture{db: db}
	f.actor, f.customer = topUpFixture(t, db)
	user := func(name string) int64 {
		t.Helper()
		var id int64
		if err := db.Pool.QueryRow(t.Context(), "INSERT INTO users(username,email,password_hash,display_name,role) VALUES($1,$1||'@example.test','unused',$1,'customer') RETURNING id", name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.unassigned, f.ownerA, f.ownerB = user("upgrade-unassigned"), user("upgrade-promoter-a"), user("upgrade-promoter-b")
	account := func(owner int64) int64 {
		t.Helper()
		var id int64
		if err := db.Pool.QueryRow(t.Context(), "INSERT INTO affiliate_accounts(user_id,program_id,status,approved_at) SELECT $1,id,'approved','2025-01-01T00:00:00Z' FROM affiliate_programs ORDER BY id LIMIT 1 RETURNING id", owner).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.accountA, f.accountB = account(f.ownerA), account(f.ownerB)
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM affiliate_rule_sets ORDER BY id LIMIT 1").Scan(&f.rule); err != nil {
		t.Fatal(err)
	}
	var link, click int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO affiliate_links(account_id,code) VALUES($1,'upgradeReferralCode') RETURNING id", f.accountB).Scan(&link); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO affiliate_clicks(link_id,session_hash,registered_user_id,converted_at) VALUES($1,repeat('a',64),$2,'2025-01-02T00:00:00Z') RETURNING id", link, f.customer).Scan(&click); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO affiliate_attributions(user_id,account_id,link_id,click_id,first_clicked_at,registered_at,source_snapshot,version,created_at)
 VALUES($1,$2,$3,$4,'2025-01-01T00:00:00Z','2026-10-01T00:00:00Z','{"historic":"retain exactly"}',9,'2025-01-02T00:00:00Z')`, f.customer, f.accountB, link, click); err != nil {
		t.Fatal(err)
	}
	// Historical financial facts belong to A although the current referral is B.
	// registered_at is deliberately later than invoice creation: the upgrade
	// must preserve old Queue semantics for unpaid invoices rather than invent
	// a new temporal cutoff for existing attribution rows.
	var product int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO products(name,slug,price_cents,stock) VALUES('Historical referral product','referral-upgrade-product',1000,10) RETURNING id").Scan(&product); err != nil {
		t.Fatal(err)
	}
	paid := func(name string) (invoice, order, item int64) {
		t.Helper()
		if err := db.Pool.QueryRow(t.Context(), "INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash,created_at) VALUES($1,$2,'paid',1000,$1,repeat('b',64),'2025-06-01T00:00:00Z') RETURNING id", name, f.customer).Scan(&order); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(t.Context(), "INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'Historical referral product','monthly',1,1000,0,1000) RETURNING id", order, product).Scan(&item); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(t.Context(), "INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,paid_at,created_at,version) VALUES($1,$2,$3,'paid',1000,'2025-06-02T00:00:00Z','2025-06-01T00:00:00Z',7) RETURNING id", name, f.customer, order).Scan(&invoice); err != nil {
			t.Fatal(err)
		}
		return
	}
	invoice, order, item := paid("referral-financial-commission")
	f.commissionInvoice = invoice
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO affiliate_commission_entries(account_id,referred_user_id,order_id,invoice_id,order_item_id,product_id,event_type,amount_cents,basis_cents,rate_bps,available_at,rule_snapshot,source_snapshot,idempotency_key,created_at)
 VALUES($1,$2,$3,$4,$5,$6,'first_purchase',100,1000,1000,'2025-06-09T00:00:00Z','{"historical_rule":true}','{"historical_source":true}','referral-historical-commission','2025-06-02T00:00:00Z')`, f.accountA, f.customer, order, invoice, item, product); err != nil {
		t.Fatal(err)
	}
	f.turnoverInvoice, _, _ = paid("referral-financial-turnover")
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO affiliate_turnover_entries(invoice_id,account_id,rule_set_id,basis_cents,paid_at,created_at) VALUES($1,$2,$3,1000,'2025-06-02T00:00:00Z','2025-06-02T00:00:00Z')", f.turnoverInvoice, f.accountA, f.rule); err != nil {
		t.Fatal(err)
	}
	for n, customer := range []int64{f.customer, f.unassigned} {
		var id int64
		if err := db.Pool.QueryRow(t.Context(), "INSERT INTO invoices(invoice_no,user_id,amount_cents,created_at,version) VALUES($1,$2,1000,'2025-01-01T00:00:00Z',4) RETURNING id", fmt.Sprintf("referral-unpaid-%d", n), customer).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			f.unpaidCurrent = id
		} else {
			f.unpaidNone = id
		}
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO affiliate_withdrawals(account_id,amount_cents,fee_cents,payout_cents,payout_method,payout_target_ciphertext,payout_target_aad,payout_target_masked,idempotency_key,submitted_at,created_at,updated_at)
 VALUES($1,50,0,50,'bank',decode('aabb','hex'),'historical-aad','****1234','referral-historical-withdrawal','2025-06-10T00:00:00Z','2025-06-10T00:00:00Z','2025-06-10T00:00:00Z')`, f.accountA); err != nil {
		t.Fatal(err)
	}
	return f
}

func referrerUpgradeFacts(t *testing.T, db *Store) string {
	t.Helper()
	var value string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'attributions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM affiliate_attributions x),
 'accounts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM affiliate_accounts x),
 'clicks',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM affiliate_clicks x),
 'commissions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM affiliate_commission_entries x),
 'turnover',(SELECT jsonb_agg(to_jsonb(x) ORDER BY invoice_id) FROM affiliate_turnover_entries x),
 'withdrawals',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM affiliate_withdrawals x),
 'events',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM affiliate_commission_events x),
 'migrations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY version) FROM schema_migrations x WHERE version<='186_customer_contact_versions.sql'))::text`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value + retiredFeaturesHistoricalFacts(t, db)
}

func TestCustomerReferrerUpgrade186PreservesFinancialOwnership(t *testing.T) {
	f := referrerSchema186(t)
	before := referrerUpgradeFacts(t, f.db)
	for range 2 {
		if err := f.db.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		if referrerUpgradeFacts(t, f.db) != before {
			t.Fatal("referral upgrade rewrote existing attribution, money, commission, withdrawal, invoice timestamps, receipts or checksums")
		}
		for _, tc := range []struct {
			invoice int64
			account *int64
			basis   string
		}{{f.commissionInvoice, &f.accountA, "legacy_financial"}, {f.turnoverInvoice, &f.accountA, "legacy_financial"}, {f.unpaidCurrent, &f.accountB, "legacy_current"}, {f.unpaidNone, nil, "legacy_current"}} {
			var account *int64
			var basis string
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT account_id,capture_basis FROM invoice_affiliate_attributions WHERE invoice_id=$1", tc.invoice).Scan(&account, &basis); err != nil || basis != tc.basis || (account == nil) != (tc.account == nil) || (account != nil && *account != *tc.account) {
				t.Fatal("incorrect referral upgrade snapshot", tc, account, basis, err)
			}
		}
		var complete bool
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT NOT EXISTS(SELECT 1 FROM invoices i LEFT JOIN invoice_affiliate_attributions birth ON birth.invoice_id=i.id WHERE birth.invoice_id IS NULL) AND NOT EXISTS(SELECT 1 FROM customer_referral_states WHERE version<>1)").Scan(&complete); err != nil || !complete {
			t.Fatal("upgrade did not initialize every invoice, or changed referral state versions", complete, err)
		}
	}
	// Moving the current referral never rewrites the frozen old unpaid rows.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE affiliate_attributions SET account_id=$2 WHERE user_id=$1", f.customer, f.accountA); err != nil {
		t.Fatal(err)
	}
	var oldAccount int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT account_id FROM invoice_affiliate_attributions WHERE invoice_id=$1", f.unpaidCurrent).Scan(&oldAccount); err != nil || oldAccount != f.accountB {
		t.Fatal("existing unpaid invoice followed later referrer change", oldAccount, err)
	}
	for _, query := range []string{
		"UPDATE invoice_affiliate_attributions SET source_snapshot='{}' WHERE invoice_id=$1",
		"DELETE FROM invoice_affiliate_attributions WHERE invoice_id=$1",
		fmt.Sprintf("UPDATE invoices SET user_id=%d WHERE id=$1", f.unassigned),
	} {
		if _, err := f.db.Pool.Exec(t.Context(), query, f.unpaidCurrent); err == nil {
			t.Fatal("immutable referral fact accepted a rewrite", query)
		}
	}
	assertNotificationUpgradeChecksums(t, f.db)
}

func TestCustomerReferrerUpgrade186RejectsConflictingFinancialOwnershipAtomically(t *testing.T) {
	f := referrerSchema186(t)
	if _, err := f.db.Pool.Exec(t.Context(), "INSERT INTO affiliate_turnover_entries(invoice_id,account_id,rule_set_id,basis_cents,paid_at) VALUES($1,$2,$3,1000,'2025-06-02T00:00:00Z')", f.commissionInvoice, f.accountB, f.rule); err != nil {
		t.Fatal(err)
	}
	before := referrerUpgradeFacts(t, f.db)
	err := f.db.Migrate(t.Context())
	if err == nil || !strings.Contains(err.Error(), "conflicting historical affiliate invoice ownership") {
		t.Fatal("ambiguous historical account ownership was guessed", err)
	}
	if referrerUpgradeFacts(t, f.db) != before {
		t.Fatal("failed referral migration changed historical facts or migration receipts")
	}
	var untouched bool
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT max(version)='186_customer_contact_versions.sql'
 AND to_regclass('customer_referral_states') IS NULL
 AND to_regclass('invoice_affiliate_attributions') IS NULL
 AND (SELECT attnotnull FROM pg_attribute WHERE attrelid='affiliate_attributions'::regclass AND attname='link_id')
 FROM schema_migrations`).Scan(&untouched); err != nil || !untouched {
		t.Fatal("failed referral upgrade partially applied schema", untouched, err)
	}
}

func TestCustomerReferrerUpgrade186PreservesAdministratorHistory(t *testing.T) {
	// Write genuine pre-split identities and commerce before schema182 installs
	// its customer-only new-write guards. Never disable them or fabricate users
	// for administrators in a post-split fixture.
	db := adminOrderReceiptSchema178(t)
	for _, name := range []string{"179_admin_order_mutation_receipts.sql", "180_customer_group_pricing.sql", "181_auth_login_portal.sql"} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	var historicalOwner, historicalPromoter, customer, account int64
	for _, entry := range []struct {
		name, role string
		id         *int64
	}{
		{"referral-legacy-admin-owner", "super_admin", &historicalOwner},
		{"referral-legacy-admin-promoter", "super_admin", &historicalPromoter},
		{"referral-legacy-customer", "customer", &customer},
	} {
		if err := db.Pool.QueryRow(t.Context(), "INSERT INTO users(username,email,password_hash,display_name,role) VALUES($1,$1||'@example.test','unused',$1,$2) RETURNING id", entry.name, entry.role).Scan(entry.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO affiliate_accounts(user_id,program_id,status) SELECT $1,id,'approved' FROM affiliate_programs ORDER BY id LIMIT 1 RETURNING id", historicalPromoter).Scan(&account); err != nil {
		t.Fatal(err)
	}
	var link int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO affiliate_links(account_id,code) VALUES($1,'legacyAdminReferral') RETURNING id", account).Scan(&link); err != nil {
		t.Fatal(err)
	}
	var administratorInvoice, customerInvoice int64
	for n, owner := range []int64{historicalOwner, customer} {
		var click, invoice int64
		if err := db.Pool.QueryRow(t.Context(), "INSERT INTO affiliate_clicks(link_id,session_hash,registered_user_id,converted_at) VALUES($1,$2,$3,now()) RETURNING id", link, strings.Repeat(fmt.Sprint(n+1), 64), owner).Scan(&click); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), "INSERT INTO affiliate_attributions(user_id,account_id,link_id,click_id,first_clicked_at,source_snapshot) VALUES($1,$2,$3,$4,'2025-01-01T00:00:00Z','{\"legacy_administrator_promoter\":true}')", owner, account, link, click); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(t.Context(), "INSERT INTO invoices(invoice_no,user_id,invoice_kind,amount_cents,status,created_at,paid_at) VALUES($1,$2,'manual',1200,$3,'2025-02-01T00:00:00Z',CASE WHEN $3::text='paid' THEN '2025-02-02T00:00:00Z'::timestamptz END) RETURNING id", fmt.Sprintf("legacy-admin-referral-%d", n), owner, []string{"paid", "unpaid"}[n]).Scan(&invoice); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			administratorInvoice = invoice
			if _, err := db.Pool.Exec(t.Context(), "INSERT INTO affiliate_turnover_entries(invoice_id,account_id,basis_cents,paid_at) VALUES($1,$2,1200,'2025-02-02T00:00:00Z')", invoice, account); err != nil {
				t.Fatal(err)
			}
		} else {
			customerInvoice = invoice
		}
	}
	for _, name := range []string{"182_separate_administrator_accounts.sql", "183_customer_group_membership_versions.sql", "184_retire_credit_and_contracts.sql", "185_manual_server_resources.sql", "186_customer_contact_versions.sql"} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	var boundary string
	if err := db.Pool.QueryRow(t.Context(), "SELECT max(version) FROM schema_migrations").Scan(&boundary); err != nil || boundary != "186_customer_contact_versions.sql" {
		t.Fatal("historical administrator fixture must stop at genuine schema186", boundary, err)
	}
	before := referrerUpgradeFacts(t, db)
	for range 2 {
		if err := db.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		if referrerUpgradeFacts(t, db) != before {
			t.Fatal("referrer upgrade rewrote pre-split administrator financial or referral facts")
		}
		var compatible bool
		if err := db.Pool.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM administrators WHERE id IN ($1,$2))=2
 AND NOT EXISTS(SELECT 1 FROM users WHERE id IN ($1,$2))
 AND EXISTS(SELECT 1 FROM invoice_affiliate_attributions WHERE invoice_id=$3 AND user_id=$1 AND referrer_user_id=$2 AND account_id=$4 AND capture_basis='legacy_financial')
 AND EXISTS(SELECT 1 FROM invoice_affiliate_attributions WHERE invoice_id=$5 AND user_id=$6 AND referrer_user_id=$2 AND account_id=$4 AND capture_basis='legacy_current')
 AND NOT EXISTS(SELECT 1 FROM customer_referral_states WHERE user_id IN ($1,$2))`, historicalOwner, historicalPromoter, administratorInvoice, account, customerInvoice, customer).Scan(&compatible); err != nil || !compatible {
			t.Fatal("historical identities were dropped, reassigned or fabricated as customers", compatible, err)
		}
	}
	// Broad historical identity references must not re-open administrator buying.
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO invoices(invoice_no,user_id,invoice_kind,amount_cents) VALUES('referral-new-admin-invoice',$1,'manual',1200)", historicalOwner); err == nil {
		t.Fatal("historical principal compatibility permitted new administrator commerce")
	}
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO affiliate_accounts(user_id,program_id) SELECT $1,id FROM affiliate_programs ORDER BY id LIMIT 1", historicalOwner); err == nil {
		t.Fatal("historical principal compatibility permitted a new administrator affiliate account")
	}
	if referrerUpgradeFacts(t, db) != before {
		t.Fatal("rejected administrator commerce changed historical facts")
	}
	assertNotificationUpgradeChecksums(t, db)
}

func TestCustomerReferrerRejectsSnapshotIsolation(t *testing.T) {
	f := referrerSchema186(t)
	if err := f.db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	facts := func() string {
		t.Helper()
		var snapshots string
		if err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'births',(SELECT jsonb_agg(to_jsonb(x) ORDER BY invoice_id) FROM invoice_affiliate_attributions x),
 'states',(SELECT jsonb_agg(to_jsonb(x) ORDER BY user_id) FROM customer_referral_states x))::text`).Scan(&snapshots); err != nil {
			t.Fatal(err)
		}
		return referrerUpgradeFacts(t, f.db) + snapshots
	}
	for _, isolation := range []pgx.TxIsoLevel{pgx.RepeatableRead, pgx.Serializable} {
		for _, scenario := range []string{"attributed_invoice", "unattributed_invoice", "referral_insert", "referral_update"} {
			t.Run(string(isolation)+"/"+scenario, func(t *testing.T) {
				tx, err := f.db.Pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: isolation})
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(t.Context())
				var oldAccount int64
				if err := tx.QueryRow(t.Context(), "SELECT account_id FROM affiliate_attributions WHERE user_id=$1", f.customer).Scan(&oldAccount); err != nil {
					t.Fatal(err)
				}
				if scenario != "referral_update" {
					// Commit a genuine change after the snapshot is established.
					// Taking the graph lock cannot refresh either snapshot. The
					// direct update case leaves its tuple unchanged so PostgreSQL
					// does not reject it earlier with a tuple-serialization error.
					next := f.accountA
					if oldAccount == next {
						next = f.accountB
					}
					if _, err := f.db.Pool.Exec(t.Context(), "UPDATE affiliate_attributions SET account_id=$2 WHERE user_id=$1", f.customer, next); err != nil {
						t.Fatal(err)
					}
				}
				before := facts()
				switch scenario {
				case "attributed_invoice", "unattributed_invoice":
					owner := f.customer
					if scenario == "unattributed_invoice" {
						owner = f.unassigned
					}
					_, err = tx.Exec(t.Context(), "INSERT INTO invoices(invoice_no,user_id,amount_cents) VALUES($1,$2,1000)", "snapshot-rejected-"+string(isolation)+"-"+scenario, owner)
				case "referral_insert":
					_, err = tx.Exec(t.Context(), "INSERT INTO affiliate_attributions(user_id,account_id,status,first_clicked_at) VALUES($1,$2,'active',now())", f.unassigned, f.accountA)
				case "referral_update":
					_, err = tx.Exec(t.Context(), "UPDATE affiliate_attributions SET status='unassigned' WHERE user_id=$1", f.customer)
				}
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "affiliate_referral_requires_read_committed" {
					t.Fatalf("snapshot-isolated %s writer did not fail explicitly: %v", scenario, err)
				}
				if err := tx.Rollback(t.Context()); err != nil {
					t.Fatal(err)
				}
				if facts() != before {
					t.Fatal("rejected snapshot-isolated writer left an invoice, referral, version or birth fact")
				}
			})
		}
	}
}
