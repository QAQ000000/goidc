package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/db/migrations"
)

func TestMutationOperationIsolationRollbackAndConcurrency(t *testing.T) {
	db := isolatedTestStore(t)
	actor, other := topUpFixture(t, db)
	ctx := t.Context()
	hash := strings.Repeat("a", 64)
	key := "same-receipt-key"
	for _, input := range []struct {
		actor int64
		scope string
	}{{actor, "admin:save"}, {actor, "affiliate:save"}, {other, "admin:save"}} {
		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			ID int64 `json:"id"`
		}
		replayed, err := ReplayOperation(ctx, tx, input.actor, input.scope, key, hash, &out)
		if err != nil || replayed {
			t.Fatal("new scope/actor replayed", replayed, err)
		}
		if err = SaveOperation(ctx, tx, input.actor, input.scope, key, hash, map[string]any{"id": input.actor}); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		tx, err = db.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err = ReplayOperation(ctx, tx, input.actor, input.scope, key, hash, &out)
		if err != nil || !replayed || out.ID != input.actor {
			t.Fatal("receipt changed", out, replayed, err)
		}
		if _, err = ReplayOperation(ctx, tx, input.actor, input.scope, key, strings.Repeat("b", 64), &out); conflictCode(t, err) != "idempotency_conflict" {
			t.Fatal(err)
		}
		tx.Rollback(ctx)
	}

	// A receipt must roll back together with its business audit, allowing retry.
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ReplayOperation(ctx, tx, actor, "rollback", key, hash, nil); err != nil {
		t.Fatal(err)
	}
	if err = SaveOperation(ctx, tx, actor, "rollback", key, hash, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err = Audit(ctx, tx, actor, "mutation.rollback", "user", actor, nil); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var receipts, audits int
	if err = db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM mutation_operations WHERE scope='rollback'),(SELECT count(*) FROM audit_events WHERE action='mutation.rollback')`).Scan(&receipts, &audits); err != nil || receipts != 0 || audits != 0 {
		t.Fatal(receipts, audits, err)
	}

	// Race first submissions: no row exists to lock before these calls.
	start := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tx, err := db.Pool.Begin(ctx)
			if err != nil {
				errs <- err
				return
			}
			defer tx.Rollback(ctx)
			replayed, err := ReplayOperation(ctx, tx, actor, "concurrent", key, hash, nil)
			if err == nil && !replayed {
				err = Audit(ctx, tx, actor, "mutation.once", "user", actor, nil)
				if err == nil {
					err = SaveOperation(ctx, tx, actor, "concurrent", key, hash, map[string]any{"id": actor})
				}
			}
			if err == nil {
				err = tx.Commit(ctx)
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM mutation_operations WHERE scope='concurrent'),(SELECT count(*) FROM audit_events WHERE action='mutation.once')`).Scan(&receipts, &audits); err != nil || receipts != 1 || audits != 1 {
		t.Fatal(receipts, audits, err)
	}

	// A waiter must obey its context while another transaction holds the key.
	tx, err = db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = ReplayOperation(ctx, tx, actor, "blocked", key, hash, nil); err != nil {
		t.Fatal(err)
	}
	blocked, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Rollback(ctx)
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err = ReplayOperation(deadline, blocked, actor, "blocked", key, hash, nil); err == nil {
		t.Fatal("ignored cancelled lock waiter")
	}
}

func TestMutationOperationsUpgradePreservesReceipts(t *testing.T) {
	db := isolatedOrderRefundSchema153(t)
	ctx := t.Context()
	// Apply the actual remaining pre-consolidation migrations, retaining checksums.
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name < "154_" || name >= "156_" {
			continue
		}
		body, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, string(body)); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)", name, hex.EncodeToString(sum[:])); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	actor, _ := topUpFixture(t, db)
	key, hash := "legacy-shared-key", strings.Repeat("a", 64)
	// Use the same key in all old key domains. Credit text includes Unicode,
	// delimiters and whitespace to check the migration's canonical hash exactly.
	note := " 核验:冻结\n"
	for _, table := range []string{"admin_setting_operations", "affiliate_operations", "marketing_campaign_operations", "ticket_mutation_operations", "balance_topup_operations", "log_cleanup_operations", "order_batch_requests"} {
		columns, values := "actor_id,idempotency_key,request_hash,result", "$1,$2,$3,'{\"version\":7}'"
		switch table {
		case "admin_setting_operations":
			columns += ",operation"
			values += ",'save'"
		case "affiliate_operations":
			columns = "user_id,idempotency_key,request_hash,result,operation"
			values += ",'save'"
		case "balance_topup_operations":
			columns = "user_id,idempotency_key,request_hash,result"
		case "order_batch_requests":
			columns = "actor_id,idempotency_key,request_hash,action"
			values = "$1,$2,$3,'cancel'"
		}
		if _, err = db.Pool.Exec(ctx, "INSERT INTO "+table+"("+columns+") VALUES("+values+")", actor, key, hash); err != nil {
			t.Fatal(table, err)
		}
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO credit_account_operations(actor_id,idempotency_key,user_id,frozen,expected_version,version_after,note) VALUES($1,$2,$1,true,6,7,$3)`, actor, key, note); err != nil {
		t.Fatal(err)
	}
	// Exercise receipts with real order/invoice/application references as well.
	for _, query := range []string{
		`WITH o AS (INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash) VALUES('LEGACY-RECEIPT-ORDER',$1,100,$2,$3) RETURNING id)
 INSERT INTO order_operation_receipts(actor_id,action,idempotency_key,order_id,request_hash,result)
 SELECT $1,'notes',$2,id,$3,jsonb_build_object('order_id',id,'version',7) FROM o`,
		`WITH i AS (INSERT INTO invoices(invoice_no,user_id,amount_cents,invoice_kind) VALUES('LEGACY-RECEIPT-INVOICE',$1,100,'manual') RETURNING id)
 INSERT INTO manual_invoice_operations(actor_id,idempotency_key,invoice_id,action,request_hash,result)
 SELECT $1,$2,id,'create',$3,jsonb_build_object('invoice_id',id,'version',7) FROM i`,
		`WITH p AS (INSERT INTO invoice_profiles(user_id,profile_type,title) VALUES($1,'individual','Receipt test') RETURNING id),
 a AS (INSERT INTO invoice_addresses(user_id,recipient,phone,address) VALUES($1,'Receipt test','123','Test address') RETURNING id),
 i AS (INSERT INTO invoice_applications(user_id,profile_id,address_id,profile_snapshot,address_snapshot,amount_cents)
 SELECT $1,p.id,a.id,'{}','{}',100 FROM p,a RETURNING id)
 INSERT INTO invoice_application_operations(actor_id,idempotency_key,application_id,action,request_hash,result)
 SELECT $1,$2,id,'create',$3,jsonb_build_object('application_id',id,'version',7) FROM i`,
	} {
		if _, err = db.Pool.Exec(ctx, query, actor, key, hash); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx); err != nil {
		t.Fatal("second migrate", err)
	}
	if err = db.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"admin:save", "affiliate:save", "marketing", "ticket", "balance-topup", "log-cleanup", "order-batch:cancel", "credit-freeze", "order:notes", "manual-invoice", "invoice-application"} {
		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		requestHash := hash
		if scope == "credit-freeze" {
			sum := sha256.Sum256([]byte(fmt.Sprintf("%d:true:6:%s", actor, note)))
			requestHash = hex.EncodeToString(sum[:])
		}
		var out struct {
			Version int64 `json:"version"`
		}
		replayed, err := ReplayOperation(ctx, tx, actor, scope, key, requestHash, &out)
		if err != nil || !replayed || (scope != "order-batch:cancel" && out.Version != 7) {
			t.Fatal(scope, out, replayed, err)
		}
		tx.Rollback(ctx)
	}
	var removed, retained int
	if err = db.Pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=ANY($1)),
 (SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=ANY($2))`,
		[]string{"admin_setting_operations", "affiliate_operations", "marketing_campaign_operations", "ticket_mutation_operations", "order_operation_receipts", "external_refund_operations", "credit_account_operations", "balance_topup_operations", "invoice_application_operations", "manual_invoice_operations", "payment_close_anomaly_refund_operations", "log_cleanup_operations", "order_batch_requests"},
		[]string{"payment_close_operations", "payment_close_verification_requests", "service_cancellation_requests"}).Scan(&removed, &retained); err != nil || removed != 0 || retained != 3 {
		t.Fatal(removed, retained, err)
	}
}
