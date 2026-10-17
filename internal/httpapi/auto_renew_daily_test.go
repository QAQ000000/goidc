package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"io"
	"log/slog"
	"testing"
)

func renewalPassFixture(t *testing.T, count int) (*store.Store, int64, []int64) {
	t.Helper()
	db, h := testServer(t)
	id, customer := user(t, db, h, "audit-renew-customer", "customer")
	pid := product(t, db, count+1)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET balance_cents=100000 WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	version, item := add(t, h, customer, pid, count)
	w := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, customer, "audit-renew-order")
	expect(t, w, 201)
	invoice := int64(data(t, w)["invoice_id"].(float64))
	if _, err := db.PayInvoice(t.Context(), id, invoice, "audit-initial-payment"); err != nil {
		t.Fatal(err)
	}
	for range count {
		job, err := db.ClaimProvisionJob(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err = db.ProcessProvisionJob(t.Context(), *job); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET auto_renew=true,next_due_at=now()+interval '1 hour' WHERE user_id=$1", id); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Pool.Query(t.Context(), "SELECT id FROM services WHERE user_id=$1 ORDER BY id", id)
	if err != nil {
		t.Fatal(err)
	}
	var services []int64
	for rows.Next() {
		var sid int64
		if err = rows.Scan(&sid); err != nil {
			t.Fatal(err)
		}
		services = append(services, sid)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return db, id, services
}

func TestAutoRenewFullPassContinuesPastCreateFailures(t *testing.T) {
	db, _, services := renewalPassFixture(t, 21)
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO service_configuration_changes(
service_id,user_id,product_revision,service_version,billing_cycle,due_at,old_recurring_cents,new_recurring_cents,
amount_cents,credit_cents,old_snapshot,new_snapshot,option_ids,option_quantities,status,new_billing_cycle)
SELECT id,user_id,1,1,'monthly',now(),1234,1334,100,0,'[]','[]','[]','{}','unpaid','monthly'
FROM services WHERE id=ANY($1)`, services[:20]); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 1; round++ {
		n, err := db.ProcessAutoRenewals(t.Context(), 20)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("round=%d processed=%d", round, n)
	}
	paid, unpaid, total := autoRenewInvoiceStats(t, db, services[20])
	_, reason, _ := autoRenewState(t, db, services[0])
	t.Logf("first blocked service error=%q; healthy service=%d paid=%d unpaid=%d total=%d", reason, services[20], paid, unpaid, total)
	if paid != 1 {
		t.Errorf("21st healthy service never attempted after a full pass: first 20 have pre-invoice failures, healthy paid=%d", paid)
	}
}

func TestAutoRenewDailyPassSerializesWorkersAndRecoversInterruptedRun(t *testing.T) {
	db, uid, services := renewalPassFixture(t, 1)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// A prior worker died after recording a start. It must not suppress recovery.
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO scheduler_runs(task_key) VALUES('automatic_renewals')"); err != nil {
		t.Fatal(err)
	}
	// A successful batch from the previous worker is not a completed full pass.
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO scheduler_runs(task_key,status,finished_at,duration_ms) VALUES('automatic_renewals','succeeded',now(),0)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET auto_renew_error='previous failure' WHERE id=$1", services[0]); err != nil {
		t.Fatal(err)
	}
	type result struct {
		count int
		err   error
	}
	done := make(chan result, 2)
	for range 2 {
		go func() { n, e := db.RunDailyAutoRenewals(t.Context(), log); done <- result{n, e} }()
	}
	total := 0
	for range 2 {
		r := <-done
		if r.err != nil {
			t.Fatal(r.err)
		}
		total += r.count
	}
	if total != 1 {
		t.Fatalf("concurrent workers paid %d renewals", total)
	}
	var completed int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM scheduler_runs WHERE task_key='automatic_renewals' AND status='succeeded' AND is_full_pass").Scan(&completed); err != nil || completed != 1 {
		t.Fatalf("completed=%d err=%v", completed, err)
	}
	_, message, _ := autoRenewState(t, db, services[0])
	if message != "" {
		t.Fatalf("paid renewal left stale error %q", message)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=now()+interval '1 hour' WHERE user_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	if n, err := db.RunDailyAutoRenewals(t.Context(), log); err != nil || n != 0 {
		t.Fatalf("same-day repeat count=%d err=%v", n, err)
	}
	// Move only our disposable history to yesterday to exercise the next day.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE scheduler_runs SET started_at=started_at-interval '1 day',finished_at=finished_at-interval '1 day' WHERE task_key='automatic_renewals'"); err != nil {
		t.Fatal(err)
	}
	if n, err := db.RunDailyAutoRenewals(t.Context(), log); err != nil || n != 1 {
		t.Fatalf("next-day count=%d err=%v", n, err)
	}
}
