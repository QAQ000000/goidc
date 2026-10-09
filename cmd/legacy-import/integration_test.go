package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func importTestDB(t *testing.T) *store.Store {
	t.Helper()
	db := emptyImportTestDB(t)
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db
}
func emptyImportTestDB(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("ZJMFGO_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("REQUIRE_INTEGRATION") == "1" {
			t.Fatal("isolated test DSN required")
		}
		t.Skip("requires isolated PostgreSQL test database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatal("refusing database without _test suffix")
	}
	base, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 8)
	if _, err = rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	schema := "legacy_test_" + hex.EncodeToString(raw)
	if _, err = base.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := &store.Store{Pool: pool}
	t.Cleanup(func() {
		db.Close()
		_, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		base.Close()
	})
	return db
}

func importFixture() legacy.Snapshot {
	return legacy.Snapshot{
		"customer_groups": {}, "contacts": {},
		"service_navigation_groups": {}, "service_navigation_preferences": {},
		"download_categories": {}, "downloads": {}, "download_products": {}, "customer_files": {},
		"staff": {}, "ticket_departments": {}, "ticket_statuses": {}, "tickets": {}, "ticket_replies": {}, "ticket_notes": {}, "ticket_attachments": {},
		"currencies":    {{"id": "7", "code": "CNY"}},
		"users":         {{"id": "10", "email": "import@example.test", "username": "客户", "password": "legacy-hash-10", "status": "1", "currency": "7", "credit": "100.00", "groupid": "0"}, {"id": "11", "email": "closed@example.test", "username": "关闭客户", "password": "legacy-hash-11", "status": "2", "currency": "7", "credit": "0.00", "groupid": "0"}},
		"products":      {{"id": "167", "name": "商品", "description": "说明", "type": "service"}},
		"prices":        {{"id": "14696", "relid": "167", "currency": "7", "monthly": "40.00", "quarterly": "120.00", "annually": "480.00", "onetime": "-1.00", "msetupfee": "2.00", "qsetupfee": "3.00", "asetupfee": "4.00", "osetupfee": "0.00"}},
		"orders":        {{"id": "20", "uid": "10", "invoiceid": "30", "ordernum": "IMPORT-20", "amount": "40.00", "status": "Pending", "create_time": "1700000000"}},
		"invoices":      {{"id": "30", "uid": "10", "total": "40.00", "subtotal": "40.00", "credit": "0.00", "tax": "0.00", "tax2": "0.00", "status": "Unpaid", "due_time": "1700000000"}},
		"invoice_items": {{"id": "50", "invoice_id": "30", "uid": "10", "amount": "40.00", "type": "host", "rel_id": "40", "description": "服务", "delete_time": "0"}, {"id": "51", "invoice_id": "999", "uid": "10", "amount": "10.00", "type": "host", "description": "孤儿一", "delete_time": "0"}, {"id": "52", "invoice_id": "999", "uid": "10", "amount": "15.00", "type": "host", "description": "孤儿二", "delete_time": "0"}},
		"services":      {{"id": "40", "uid": "10", "orderid": "20", "productid": "167", "domain": "test", "domainstatus": "Pending", "billingcycle": "monthly", "amount": "40.00", "firstpaymentamount": "42.00"}},
	}
}

func newImporter(snapshot legacy.Snapshot) *importer {
	return &importer{source: snapshot, report: legacy.Inspect(snapshot, "integration"), sourceName: "integration"}
}

func TestImportPreservesMoneyMappingsAndReplay(t *testing.T) {
	db := importTestDB(t)
	ctx := t.Context()
	snapshot := importFixture()
	im := newImporter(snapshot)
	if err := im.apply(ctx, db.Pool); err != nil {
		t.Fatal(err)
	}
	var archived string
	if err := db.Pool.QueryRow(ctx, "SELECT payload::text FROM legacy_source_records WHERE entity='users' AND legacy_id=10").Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(archived, "legacy-hash-10") || strings.Contains(archived, `"password"`) {
		t.Fatalf("legacy password archived: %s", archived)
	}
	var price, fee, orphan int64
	var disabled, review bool
	var maps, items, queues int
	if err := db.Pool.QueryRow(ctx, "SELECT price_cents,setup_fee_cents,active FROM products WHERE legacy_id=167").Scan(&price, &fee, &disabled); err != nil || price != 4000 || fee != 200 || disabled {
		t.Fatalf("wrong price or product sale state: %d/%v/%v", price, disabled, err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT setup_fee_cents FROM product_prices WHERE billing_cycle='quarterly'").Scan(&fee); err != nil || fee != 300 {
		t.Fatalf("missing pricing setup fee: %d/%v", fee, err)
	}
	var initial, recurring int64
	if err := db.Pool.QueryRow(ctx, "SELECT oi.total_cents,s.recurring_price_cents FROM services s JOIN order_items oi ON oi.id=s.order_item_id WHERE s.legacy_id=40").Scan(&initial, &recurring); err != nil || initial != 4200 || recurring != 4000 {
		t.Fatalf("initial and recurring money mixed: %d/%d/%v", initial, recurring, err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT amount_cents,import_review_required FROM invoices WHERE legacy_id=-999").Scan(&orphan, &review); err != nil || orphan != 2500 || !review {
		t.Fatalf("orphan total/review: %d/%v/%v", orphan, review, err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM invoice_items").Scan(&items); err != nil || items != 3 {
		t.Fatalf("invoice item loss: %d/%v", items, err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM legacy_id_map").Scan(&maps); err != nil || maps != 11 {
		t.Fatalf("ID mappings incomplete: %d/%v", maps, err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM provision_jobs").Scan(&queues); err != nil || queues != 0 {
		t.Fatal("migration must not create resources")
	}
	var uid, invoiceID int64
	if err := db.Pool.QueryRow(ctx, "SELECT u.id,i.id,i.import_review_required FROM users u JOIN invoices i ON i.user_id=u.id WHERE i.legacy_id=30").Scan(&uid, &invoiceID, &review); err != nil || !review {
		t.Fatal("historical invoice must remain under review")
	}
	_, err := db.PayInvoice(ctx, uid, invoiceID, "import-pay-test")
	assertImportReview(t, err)
	_, err = db.CreatePaymentIntent(ctx, uid, invoiceID, "mock", "import-payment", "", "import-online-test")
	assertImportReview(t, err)
	if _, err := db.Pool.Exec(ctx, "UPDATE users SET balance_cents=8000,display_name='迁移后姓名' WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE invoices SET amount_cents=2400,import_review_required=false WHERE legacy_id=-999"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE invoice_items SET description='核验后的描述' WHERE legacy_id=51"); err != nil {
		t.Fatal(err)
	}
	// Rebuild mappings from the prior importer without overwriting live state.
	if _, err := db.Pool.Exec(ctx, "DELETE FROM legacy_id_map"); err != nil {
		t.Fatal(err)
	}
	im = newImporter(snapshot)
	if err := im.apply(ctx, db.Pool); err != nil {
		t.Fatal(err)
	}
	var balance int64
	var name string
	if err := db.Pool.QueryRow(ctx, "SELECT balance_cents,display_name FROM users WHERE id=$1", uid).Scan(&balance, &name); err != nil || balance != 8000 || name != "迁移后姓名" {
		t.Fatal("replay overwrote post-migration account state")
	}
	if im.report.Replayed["users"] != 2 || im.report.Replayed["products"] != 1 || im.report.Replayed["orders"] != 1 || im.report.Replayed["invoices"] != 1 || im.report.Replayed["services"] != 1 {
		t.Fatalf("missing replay counts: %#v", im.report.Replayed)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT amount_cents,import_review_required FROM invoices WHERE legacy_id=-999").Scan(&orphan, &review); err != nil || orphan != 2400 || review {
		t.Fatal("replay overwrote reviewed orphan invoice")
	}
	if err := db.Pool.QueryRow(ctx, "SELECT description FROM invoice_items WHERE legacy_id=51").Scan(&name); err != nil || name != "核验后的描述" {
		t.Fatal("replay overwrote reviewed invoice item")
	}
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM legacy_id_map").Scan(&maps); err != nil || maps != 11 {
		t.Fatal("replay did not reconstruct complete ID map")
	}
	snapshot["users"][0]["credit"] = "200.00"
	if err := newImporter(snapshot).apply(ctx, db.Pool); err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("changed source must fail before mutations: %v", err)
	}
	var closed string
	if err := db.Pool.QueryRow(ctx, "SELECT status FROM users WHERE legacy_id=11").Scan(&closed); err != nil || closed != "disabled" {
		t.Fatal("closed legacy account enabled")
	}
}

func assertImportReview(t *testing.T, err error) {
	t.Helper()
	var conflict *store.Conflict
	if !errors.As(err, &conflict) || conflict.Code != "import_review_required" {
		t.Fatalf("expected import_review_required, got %v", err)
	}
}

func TestImportedInvoiceFinancialGuards(t *testing.T) {
	db := importTestDB(t)
	ctx := t.Context()
	if err := newImporter(importFixture()).apply(ctx, db.Pool); err != nil {
		t.Fatal(err)
	}
	var uid, iid int64
	if err := db.Pool.QueryRow(ctx, "SELECT user_id,id FROM invoices WHERE legacy_id=30").Scan(&uid, &iid); err != nil {
		t.Fatal(err)
	}
	_, err := db.PayInvoice(ctx, uid, iid, "guard-pay-test")
	assertImportReview(t, err)
	_, err = db.CreatePaymentIntent(ctx, uid, iid, "mock", "guard-payment", "", "guard-online-test")
	assertImportReview(t, err)
	if _, err = db.Pool.Exec(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,idempotency_key)
VALUES('mock','existing-payment',$1,$2,4000,'existing-intent')`, iid, uid); err != nil {
		t.Fatal(err)
	}
	_, err = db.ProcessPaymentWebhook(ctx, "mock", "guard-event", "existing-payment", iid, 4000, "succeeded", []byte(`{"status":"succeeded"}`))
	assertImportReview(t, err)
	var status string
	if err = db.Pool.QueryRow(ctx, "SELECT status FROM payment_transactions WHERE provider_payment_id='existing-payment'").Scan(&status); err != nil || status != "pending" {
		t.Fatal("blocked webhook changed payment")
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE invoices SET status='paid' WHERE id=$1", iid); err != nil {
		t.Fatal(err)
	}
	var adminID int64
	if err = db.Pool.QueryRow(ctx, "INSERT INTO users(username,email,password_hash,display_name,role) VALUES('guard-admin','guard-admin@example.test','unused','Guard admin','super_admin') RETURNING id").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	_, err = db.RefundInvoice(ctx, adminID, iid, "guard-refund-test")
	assertImportReview(t, err)
	var balance int64
	if err = db.Pool.QueryRow(ctx, "SELECT balance_cents FROM users WHERE id=$1", uid).Scan(&balance); err != nil || balance != 10000 {
		t.Fatal("blocked financial operation changed balance")
	}
	for _, table := range []string{"payment_webhook_events", "provision_jobs", "audit_events"} {
		var n int
		if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("blocked operation left %s effects: %d, %v", table, n, err)
		}
	}
	var n int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM balance_ledger WHERE entry_type IN ('payment','refund')").Scan(&n); err != nil || n != 0 {
		t.Fatal("blocked financial operation wrote ledger")
	}
	if err = db.Pool.QueryRow(ctx, "SELECT status FROM orders").Scan(&status); err != nil || status != "pending" {
		t.Fatal("blocked operation changed order")
	}
	if err = db.Pool.QueryRow(ctx, "SELECT status FROM services").Scan(&status); err != nil || status != "pending_payment" {
		t.Fatal("blocked operation changed service")
	}
	if err = db.Pool.QueryRow(ctx, "SELECT status FROM invoices WHERE id=$1", iid).Scan(&status); err != nil || status != "paid" {
		t.Fatal("blocked refund changed invoice")
	}
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM payment_transactions").Scan(&n); err != nil || n != 1 {
		t.Fatal("blocked intent created payment transaction")
	}
}

func TestLegacyEmailTemplateImportPreservesEditsOnReplay(t *testing.T) {
	db := importTestDB(t)
	ctx := t.Context()
	var adminID int64
	if err := db.Pool.QueryRow(ctx, "INSERT INTO users(username,email,password_hash,display_name,role,status) VALUES('email-import-admin','email-import-admin@example.test','unused','Email import admin','super_admin','active') RETURNING id").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	snapshot := importFixture()
	snapshot["email_templates"] = []legacy.Record{
		{"id": "501", "type": "support", "name": "旧工单提醒", "subject": "工单 {TICKETNUMBER}", "message": "旧模板正文 {{ticket}}", "disabled": "0", "language": "zh-cn", "create_time": "1700000000", "update_time": "1700000100"},
		{"id": "502", "type": "invoice", "name": "无正文模板", "subject": "旧账单", "message": "", "disabled": "0", "language": "zh-cn", "create_time": "1700000000", "update_time": "1700000100"},
	}
	im := newImporter(snapshot)
	im.fileAuthorID = adminID
	if err := im.apply(ctx, db.Pool); err != nil {
		t.Fatal(err)
	}
	var kind, body string
	var active bool
	if err := db.Pool.QueryRow(ctx, "SELECT kind,body,active FROM notification_templates WHERE legacy_id=501").Scan(&kind, &body, &active); err != nil || kind != "legacy_email_501" || body != "旧模板正文 {{ticket}}" || !active {
		t.Fatalf("imported template mismatch: %s/%s/%v/%v", kind, body, active, err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT active,body FROM notification_templates WHERE legacy_id=502").Scan(&active, &body); err != nil || active || body != "无正文模板" {
		t.Fatalf("empty template policy mismatch: %v/%s/%v", err, body, active)
	}
	var issues int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM legacy_import_issues WHERE entity='email_templates' AND legacy_id=502 AND code='empty_body_disabled'").Scan(&issues); err != nil || issues != 1 {
		t.Fatalf("missing empty template issue: %d/%v", issues, err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE notification_templates SET name='现场模板名',body='现场正文',active=true WHERE legacy_id=501"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "DELETE FROM legacy_id_map WHERE entity='email_templates'"); err != nil {
		t.Fatal(err)
	}
	replay := newImporter(snapshot)
	replay.fileAuthorID = adminID
	if err := replay.apply(ctx, db.Pool); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT name,body,active FROM notification_templates WHERE legacy_id=501").Scan(&kind, &body, &active); err != nil || kind != "现场模板名" || body != "现场正文" || !active {
		t.Fatalf("replay overwrote edited template: %s/%s/%v/%v", kind, body, active, err)
	}
	if replay.report.Replayed["email_templates"] != 2 {
		t.Fatalf("expected email template replay, got %#v", replay.report.Replayed)
	}
}

func TestImportedServiceCannotBypassReview(t *testing.T) {
	db := importTestDB(t)
	snapshot := importFixture()
	snapshot["services"][0]["domainstatus"] = "Suspended"
	if err := newImporter(snapshot).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	var uid, sid int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT user_id,id FROM services").Scan(&uid, &sid); err != nil {
		t.Fatal(err)
	}
	_, err := db.CreateRenewalInvoice(t.Context(), uid, sid, "guard-renew-test")
	assertImportReview(t, err)
	err = db.SetAutoRenew(t.Context(), uid, sid, true, false)
	assertImportReview(t, err)
	_, err = db.EnqueueServiceAction(t.Context(), uid, sid, "resume", "guard-resume-test", false)
	assertImportReview(t, err)
	var adminID int64
	if err = db.Pool.QueryRow(t.Context(), "INSERT INTO users(username,email,password_hash,display_name,role) VALUES('guard-admin','guard-admin@example.test','unused','Guard admin','super_admin') RETURNING id").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	_, err = db.EnqueueServiceAction(t.Context(), adminID, sid, "sync", "guard-sync-test", true)
	assertImportReview(t, err)
	_, err = db.RequestServiceCancellation(t.Context(), uid, sid, "immediate", "取消历史服务", "guard-cancel-test")
	assertImportReview(t, err)
	if _, err = db.Pool.Exec(t.Context(), "UPDATE services SET auto_renew=true,next_due_at=now() WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	if n, err := db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 0 {
		t.Fatalf("auto renewal processed unreviewed service: %d/%v", n, err)
	}
	for _, table := range []string{"provision_jobs", "service_cancellation_requests", "audit_events"} {
		var n int
		if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("blocked service operation wrote %s: %d/%v", table, n, err)
		}
	}
	var n int
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoices WHERE legacy_source=''").Scan(&n); err != nil || n != 0 {
		t.Fatal("unreviewed service generated payable invoice")
	}
	var jid int64
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,attempts,locked_at)
VALUES($1,'resume','old-job','running',1,now()) RETURNING id`, sid).Scan(&jid); err != nil {
		t.Fatal(err)
	}
	err = db.ProcessProvisionJob(t.Context(), store.ProvisionJob{ID: jid, ServiceID: sid, Action: "resume", Attempts: 1})
	assertImportReview(t, err)
	_, err = db.CompleteManualServiceJob(t.Context(), adminID, sid, store.ManualCompletion{JobID: jid, Note: "人工完成测试"})
	assertImportReview(t, err)
	var status string
	if err = db.Pool.QueryRow(t.Context(), "SELECT status FROM services WHERE id=$1", sid).Scan(&status); err != nil || status != "suspended" {
		t.Fatal("blocked worker changed service")
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", jid).Scan(&status); err != nil || status != "running" {
		t.Fatal("blocked worker completed job")
	}
}

func TestImportRejectsSnapshotAdditionAndRemoval(t *testing.T) {
	db := importTestDB(t)
	if err := newImporter(importFixture()).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	for _, remove := range []bool{true, false} {
		snapshot := importFixture()
		if remove {
			snapshot["invoice_items"] = snapshot["invoice_items"][:2]
		} else {
			snapshot["products"] = append(snapshot["products"], legacy.Record{"id": "168", "name": "新增商品"})
		}
		if err := newImporter(snapshot).apply(t.Context(), db.Pool); err == nil || !strings.Contains(err.Error(), "source changed") {
			t.Fatalf("modified source accepted: %v", err)
		}
	}
}

func TestImportArchivesDeletedItems(t *testing.T) {
	db := importTestDB(t)
	snapshot := importFixture()
	snapshot["invoice_items"][2]["delete_time"] = "1700000000"
	if err := newImporter(snapshot).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	var items, archived int
	var amount int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoice_items").Scan(&items); err != nil || items != 2 {
		t.Fatal("deleted invoice item imported as operative")
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM legacy_source_records WHERE entity='invoice_items'").Scan(&archived); err != nil || archived != 3 {
		t.Fatal("deleted source not archived")
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT amount_cents FROM invoices WHERE legacy_id=-999").Scan(&amount); err != nil || amount != 1000 {
		t.Fatal("deleted invoice item affects orphan total")
	}
}

func TestImportRejectsAmbiguousOrUnsupportedData(t *testing.T) {
	tests := map[string]func(legacy.Snapshot){
		"orphan owner":      func(s legacy.Snapshot) { s["invoice_items"][2]["uid"] = "11" },
		"invoice owner":     func(s legacy.Snapshot) { s["invoice_items"][0]["uid"] = "11" },
		"negative orphan":   func(s legacy.Snapshot) { s["invoice_items"][1]["amount"] = "-30.00" },
		"unsupported cycle": func(s legacy.Snapshot) { s["services"][0]["billingcycle"] = "semiannually" },
		"negative setup":    func(s legacy.Snapshot) { s["prices"][0]["msetupfee"] = "-1.00" },
		"sum overflow":      func(s legacy.Snapshot) { s["invoice_items"][1]["amount"] = "92233720368547758.07" },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			snapshot := importFixture()
			change(snapshot)
			if err := newImporter(snapshot).validate(); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
}

func TestImportTransactionRollback(t *testing.T) {
	db := importTestDB(t)
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION fail_import() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'import test failure'; END $$;
CREATE TRIGGER fail_import BEFORE INSERT ON invoices FOR EACH ROW EXECUTE FUNCTION fail_import()`); err != nil {
		t.Fatal(err)
	}
	err := newImporter(importFixture()).apply(t.Context(), db.Pool)
	if err == nil {
		t.Fatal("expected injected failure")
	}
	var count int
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM users").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial migration committed")
	}
	var status string
	if err = db.Pool.QueryRow(t.Context(), "SELECT status FROM legacy_import_runs").Scan(&status); err != nil || status != "failed" {
		t.Fatal("failed run not recorded")
	}
}

func TestImportRejectsPrecisionLoss(t *testing.T) {
	snapshot := importFixture()
	snapshot["users"][0]["credit"] = "1.001"
	if err := newImporter(snapshot).validate(); err == nil {
		t.Fatal("invalid precision silently accepted")
	}
}

// This optional rehearsal reads MySQL in a read-only transaction and imports
// only into the disposable schema created by importTestDB.
func TestLegacySourceRehearsal(t *testing.T) {
	dsn := os.Getenv("ZJMFGO_LEGACY_TEST_MYSQL_DSN")
	if dsn == "" {
		if os.Getenv("REQUIRE_LEGACY_REHEARSAL") == "1" {
			t.Fatal("read-only legacy test DSN required")
		}
		t.Skip("requires read-only legacy source")
	}
	source, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal("invalid legacy source connection")
	}
	defer source.Close()
	snapshot, err := legacy.Read(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	db := importTestDB(t)
	im := newImporter(snapshot)
	im.uploadRoot = os.Getenv("LEGACY_UPLOAD_ROOT")
	if len(snapshot["downloads"])+len(snapshot["customer_files"])+len(snapshot["staff"])+len(snapshot["tickets"]) > 0 {
		im.fileAuthorID = navigationAdmin(t, db)
	}
	if err = im.apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	reconcileTicketAttachments(t, db, snapshot, im.uploadRoot)
	for _, entity := range []string{"users", "products", "orders", "invoices", "services", "customer_groups", "contacts", "service_navigation_groups", "download_categories", "downloads", "download_products", "customer_files", "email_templates", "staff", "ticket_departments", "tickets", "ticket_replies", "ticket_notes", "ticket_attachments"} {
		var n int
		if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM "+targetTables[entity]+" WHERE legacy_id>0 AND legacy_source=$1", im.targetSource(entity)).Scan(&n); err != nil || n != len(snapshot[entity]) {
			t.Fatalf("source reconciliation failed: %s/%d/%d/%v", entity, n, len(snapshot[entity]), err)
		}
	}
	var expectedItems int
	var expectedSum int64
	for _, item := range snapshot["invoice_items"] {
		if !deleted(item) {
			expectedItems++
			expectedSum += cents(item["amount"])
		}
	}
	var items int
	var sum int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*),coalesce(sum(amount_cents),0) FROM invoice_items").Scan(&items, &sum); err != nil || items != expectedItems || sum != expectedSum {
		t.Fatalf("invoice item money/count reconciliation failed: %d/%d/%v", items, sum, err)
	}
	for _, inv := range snapshot["invoices"] {
		var amount int64
		var review bool
		if err = db.Pool.QueryRow(t.Context(), "SELECT amount_cents,import_review_required FROM invoices WHERE legacy_id=$1", sourceID(inv)).Scan(&amount, &review); err != nil || amount != cents(inv["total"]) || !review {
			t.Fatalf("invoice amount/review reconciliation failed: %d", sourceID(inv))
		}
	}
	var orphanCount, mapCount, queues int
	var preferenceCount int
	for _, r := range snapshot["service_navigation_groups"] {
		var name, icon string
		var order int64
		if err = db.Pool.QueryRow(t.Context(), "SELECT name,icon,sort_order FROM service_navigation_groups WHERE legacy_id=$1", sourceID(r)).Scan(&name, &icon, &order); err != nil {
			t.Fatal(err)
		}
		wantIcon, _ := legacy.ServiceNavigationIcon(r["fa_icon"])
		if name != strings.TrimSpace(r["groupname"]) || icon != wantIcon || order != referenceID(r, "order") {
			t.Fatal("navigation group reconciliation")
		}
	}
	for _, r := range snapshot["products"] {
		var oldGroup *int64
		if err = db.Pool.QueryRow(t.Context(), "SELECT g.legacy_id FROM products p LEFT JOIN service_navigation_groups g ON g.id=p.service_group_id WHERE p.legacy_id=$1", sourceID(r)).Scan(&oldGroup); err != nil {
			t.Fatal(err)
		}
		group := referenceID(r, "groupid")
		if index(snapshot["service_navigation_groups"])[group] != nil {
			if oldGroup == nil || *oldGroup != group {
				t.Fatal("product navigation reconciliation")
			}
		} else if oldGroup != nil {
			t.Fatal("unresolved product group mapped")
		}
	}
	for _, r := range snapshot["service_navigation_preferences"] {
		if index(snapshot["users"])[referenceID(r, "uid")] == nil || index(snapshot["service_navigation_groups"])[referenceID(r, "groupid")] == nil {
			continue
		}
		preferenceCount++
		var uid, gid int64
		var visible bool
		if err = db.Pool.QueryRow(t.Context(), "SELECT u.legacy_id,g.legacy_id,p.visible FROM service_navigation_preferences p JOIN users u ON u.id=p.user_id JOIN service_navigation_groups g ON g.id=p.group_id WHERE p.legacy_id=$1", sourceID(r)).Scan(&uid, &gid, &visible); err != nil || uid != referenceID(r, "uid") || gid != referenceID(r, "groupid") || visible != (r["is_show"] == "1") {
			t.Fatal("navigation preference reconciliation", err)
		}
	}
	var orphanSum int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*),coalesce(sum(amount_cents),0) FROM invoices WHERE legacy_id<0").Scan(&orphanCount, &orphanSum); err != nil {
		t.Fatal(err)
	}
	ticketMaps := 0
	for _, entity := range []string{"staff", "ticket_departments", "tickets", "ticket_replies", "ticket_notes", "ticket_attachments"} {
		ticketMaps += len(snapshot[entity])
	}
	expectedMappings := len(snapshot["users"]) + len(snapshot["products"]) + len(snapshot["orders"]) + len(snapshot["invoices"]) + 2*len(snapshot["services"]) + len(snapshot["customer_groups"]) + len(snapshot["contacts"]) + expectedItems + orphanCount + len(snapshot["service_navigation_groups"]) + preferenceCount + len(snapshot["download_categories"]) + len(snapshot["downloads"]) + len(snapshot["download_products"]) + len(snapshot["customer_files"]) + len(snapshot["email_templates"]) + ticketMaps
	// Product configuration keeps stable mappings for groups, product links,
	// options, quantity sub-options, and historical service selections.
	for _, entity := range []string{"product_config_groups", "product_config_links", "product_config_options", "product_config_option_subs", "product_config_option_links", "service_option_selections"} {
		expectedMappings += len(snapshot[entity])
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM legacy_id_map").Scan(&mapCount); err != nil || mapCount != expectedMappings {
		t.Fatal("incomplete legacy ID mapping")
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs").Scan(&queues); err != nil || queues != 0 {
		t.Fatal("migration created provision jobs")
	}
	replay := newImporter(snapshot)
	replay.uploadRoot = im.uploadRoot
	replay.fileAuthorID = im.fileAuthorID
	if err = replay.apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	for _, entity := range []string{"users", "products", "orders", "invoices", "services", "customer_groups", "contacts", "service_navigation_groups", "download_categories", "downloads", "download_products", "customer_files", "email_templates", "staff", "ticket_departments", "tickets", "ticket_replies", "ticket_notes", "ticket_attachments"} {
		if replay.report.Replayed[entity] != len(snapshot[entity]) {
			t.Fatalf("incomplete replay: %s", entity)
		}
	}
	if replay.report.Replayed["service_navigation_preferences"] != preferenceCount {
		t.Fatal("incomplete navigation preference replay")
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*),coalesce(sum(amount_cents),0) FROM invoice_items").Scan(&items, &sum); err != nil || items != expectedItems || sum != expectedSum {
		t.Fatal("replay changed historical invoice items")
	}
	issues := map[string]int{}
	for _, issue := range im.report.Issues {
		issues[issue.Code]++
	}
	t.Logf("source_counts=%v mapped=%v; invoice_items=%d sum_cents=%d orphan_invoices=%d orphan_sum_cents=%d mappings=%d queues=%d; replay passed; issue_counts=%v", im.report.Counts, im.report.Mapped, items, sum, orphanCount, orphanSum, mapCount, queues, issues)
}
