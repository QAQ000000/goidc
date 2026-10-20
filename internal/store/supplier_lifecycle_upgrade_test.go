package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5/pgconn"
)

const supplierLifecycleMigration170 = "170_supplier_lifecycle.sql"

type supplierLifecycleUpgradeFixture struct {
	account  int64
	services []int64
	jobs     []int64
}

func supplierLifecycleSchema169(t *testing.T) *Store {
	t.Helper()
	db := cancellationUpgradeSchema168(t)
	applyNotificationUpgradeMigration(t, db, cancellationSchema169)
	var boundary string
	var has170 bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT max(version),
to_regprocedure('preserve_supplier_lifecycle_request()') IS NOT NULL FROM schema_migrations`).Scan(&boundary, &has170); err != nil || boundary != cancellationSchema169 || has170 {
		t.Fatal("fixture must originate at genuine schema169", boundary, has170, err)
	}
	return db
}

func supplierLifecycleUpgradeHistory(t *testing.T, db *Store) supplierLifecycleUpgradeFixture {
	t.Helper()
	f := supplierLifecycleUpgradeFixture{}
	actor, customer := topUpFixture(t, db)
	if _, _, err := db.TopUp(t.Context(), actor, customer, 8000, "lifecycle-upgrade-funding", "isolated schema169 funding"); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO supplier_accounts(name,provider,base_url,username,credentials_encrypted,version)
VALUES('Legacy lifecycle account',$1,'https://lifecycle-upgrade.example.test','legacy',''::bytea,8) RETURNING id`, supplier.ProviderZJMF).Scan(&f.account); err != nil {
		t.Fatal(err)
	}
	var product, binding, order, item int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO products(name,slug,price_cents,stock,provision_mode)
VALUES('Legacy lifecycle product','legacy-lifecycle-product',1000,10,'supplier') RETURNING id`).Scan(&product); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO supplier_product_bindings(product_id,supplier_id,provider,upstream_product_id,upstream_revision,billing_cycle,currency,maximum_cost_cents,custom_fields_encrypted)
VALUES($1,$2,$3,17,'legacy-lifecycle-revision','monthly','CNY',1000,''::bytea) RETURNING id`, product, f.account, supplier.ProviderZJMF).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash,status,review_status)
VALUES('LIFECYCLE-UPGRADE-O',$1,7000,'lifecycle-upgrade-order','fixture','active','approved') RETURNING id`, customer).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents)
VALUES($1,$2,'Legacy lifecycle product','monthly',7,1000,0,7000) RETURNING id`, order, product).Scan(&item); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := json.Marshal(supplierSnapshot{SupplierID: f.account, BindingID: binding, Provider: supplier.ProviderZJMF})
	for n, state := range []string{"pending", "running", "waiting_manual", "waiting_verification", "succeeded", "failed", "cancelled"} {
		var service, job int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,provision_mode,delivery_snapshot,upstream_id,unit_number,next_due_at)
VALUES($1,$2,$3,$4,'Legacy lifecycle resource',$5,'monthly','active','supplier',$6,$7,$8,clock_timestamp()+interval '1 month') RETURNING id`, customer, order, item, product, fmt.Sprintf("lifecycle-upgrade-%d.example.test", n), snapshot, fmt.Sprint(42+n), n+1).Scan(&service); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,attempts,locked_at,external_started_at,result)
VALUES($1,'suspend',$2,$3,2,CASE WHEN $3='running' THEN clock_timestamp() END,
CASE WHEN $3 IN ('running','waiting_verification','succeeded') THEN clock_timestamp() END,
'{"upstream_status":"legacy","invoice_id":90}'::jsonb) RETURNING id`, service, "legacy-lifecycle-"+state, state).Scan(&job); err != nil {
			t.Fatal(err)
		}
		f.services, f.jobs = append(f.services, service), append(f.jobs, job)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE supplier_accounts SET running_job_id=$2 WHERE id=$1`, f.account, f.jobs[1]); err != nil {
		t.Fatal(err)
	}
	return f
}

func supplierLifecycleUpgradeFacts(t *testing.T, db *Store) string {
	t.Helper()
	var facts string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
'accounts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM supplier_accounts x),
'bindings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM supplier_product_bindings x),
'services',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM services x),
'jobs',(SELECT jsonb_agg(to_jsonb(x)-'correlation_id' ORDER BY id) FROM provision_jobs x),
'orders',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM orders x),
'items',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM order_items x),
'users',`+historicalAccountSnapshotRows(t, db)+`,
'invoices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoices x),
'payments',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM payment_transactions x),
'ledger',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM balance_ledger x))::text`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	return facts
}

func TestSupplierLifecycleUpgrade169PreservesHistory(t *testing.T) {
	db := supplierLifecycleSchema169(t)
	supplierLifecycleUpgradeHistory(t, db)
	before := supplierLifecycleUpgradeFacts(t, db)
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if actual := supplierLifecycleUpgradeFacts(t, db); actual != before {
		t.Fatal("schema169->170 changed original requests, resources, account lease or financial facts")
	}
	if err := db.Migrate(t.Context()); err != nil || supplierLifecycleUpgradeFacts(t, db) != before {
		t.Fatal("repeated migration changed original facts", err)
	}
	assertNotificationUpgradeChecksums(t, db)
}

func TestSupplierLifecycleRequestDatabaseGuards(t *testing.T) {
	db := supplierLifecycleSchema169(t)
	f := supplierLifecycleUpgradeHistory(t, db)
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var manualJob int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,request_payload)
VALUES($1,'terminate','lifecycle-manual-valid','waiting_manual','{"manual_lifecycle":true}') RETURNING id`, f.services[4]).Scan(&manualJob); err != nil {
		t.Fatal("valid manual marker rejected", err)
	}
	invalid := []struct{ action, payload string }{
		{"suspend", `{"manual_lifecycle":false}`},
		{"suspend", `{"manual_lifecycle":"true"}`},
		{"terminate", `{"manual_lifecycle":true,"extra":1}`},
		{"create", `{"manual_lifecycle":true}`},
		{"resume", `{"billing_suspension_job_id":1}`},
		{"resume", `{"billing_suspension_job_id":0,"billing_invoice_id":2}`},
		{"resume", `{"billing_suspension_job_id":1.5,"billing_invoice_id":2}`},
		{"resume", `{"billing_suspension_job_id":"1","billing_invoice_id":2}`},
		{"resume", `{"billing_suspension_job_id":9223372036854775808,"billing_invoice_id":2}`},
		{"resume", `{"manual_lifecycle":false,"billing_suspension_job_id":1,"billing_invoice_id":2}`},
		{"suspend", `{"system_overdue_suspension":false,"billing_invoice_id":2}`},
		{"suspend", `{"system_overdue_suspension":true,"billing_invoice_id":0}`},
		{"suspend", `{"system_overdue_suspension":true,"billing_invoice_id":9223372036854775808}`},
		{"terminate", `{"system_overdue_suspension":true,"billing_invoice_id":2}`},
		{"resume", `true`},
		{"resume", `[]`},
	}
	for n, item := range invalid {
		_, err := db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,request_payload)
VALUES($1,$2,$3,'succeeded',$4::jsonb)`, f.services[4], item.action, fmt.Sprintf("lifecycle-invalid-%d", n), item.payload)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "provision_jobs_password_request_check" {
			t.Fatalf("malformed action/payload was not rejected by request CHECK: action=%s payload=%s err=%v", item.action, item.payload, err)
		}
	}
	for n, payload := range []string{
		`{"billing_suspension_job_id":1,"billing_invoice_id":2}`,
		`{"manual_lifecycle":true,"billing_suspension_job_id":9223372036854775807,"billing_invoice_id":9223372036854775807}`,
	} {
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,request_payload)
VALUES($1,'resume',$2,'succeeded',$3::jsonb)`, f.services[4], fmt.Sprintf("lifecycle-valid-resume-%d", n), payload); err != nil {
			t.Fatal("valid billing recovery payload rejected", err)
		}
	}
	for n, payload := range []string{
		`{"system_overdue_suspension":true,"billing_invoice_id":2}`,
		`{"manual_lifecycle":true,"system_overdue_suspension":true,"billing_invoice_id":9223372036854775807}`,
	} {
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,request_payload)
VALUES($1,'suspend',$2,'succeeded',$3::jsonb)`, f.services[4], fmt.Sprintf("lifecycle-valid-suspend-%d", n), payload); err != nil {
			t.Fatal("valid overdue source payload rejected", err)
		}
	}
	before := supplierLifecycleUpgradeFacts(t, db)
	for _, query := range []string{
		`UPDATE provision_jobs SET action='sync' WHERE id=$1`,
		`UPDATE provision_jobs SET service_id=` + fmt.Sprint(f.services[5]) + ` WHERE id=$1`,
		`UPDATE provision_jobs SET idempotency_key='replacement-lifecycle-identity' WHERE id=$1`,
		`UPDATE provision_jobs SET request_payload='{}' WHERE id=$1`,
		`UPDATE provision_jobs SET request_payload='{"manual_lifecycle":true,"system_overdue_suspension":true,"billing_invoice_id":2}' WHERE id=$1`,
	} {
		if _, err := db.Pool.Exec(t.Context(), query, manualJob); err == nil || supplierLifecycleUpgradeFacts(t, db) != before {
			t.Fatal("lifecycle request identity was mutated", query, err)
		}
	}
	var legacy int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,attempts,locked_at)
VALUES($1,'terminate','lifecycle-legacy-running','running',1,clock_timestamp()) RETURNING id`, f.services[5]).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE provision_jobs SET status='waiting_manual',locked_at=NULL,
request_payload='{"manual_lifecycle":true}' WHERE id=$1`, legacy); err != nil {
		t.Fatal("undispatched legacy job could not enter manual responsibility", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE provision_jobs SET status='waiting_manual',locked_at=NULL,
request_payload='{"manual_lifecycle":true}' WHERE id=$1`, f.jobs[1]); err == nil {
		t.Fatal("dispatched historical write was reclassified as unsent manual work")
	}
}
