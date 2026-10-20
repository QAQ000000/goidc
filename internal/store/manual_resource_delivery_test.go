package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/provision"
)

type manualResourceDeliveryFixture struct {
	notificationFixture
	credentialContext context.Context
	resource          int64
	order             CheckoutResult
	services          []int64
	inputs            []ManualCompletion
}

func newManualResourceDeliveryFixture(t *testing.T, quantity int) manualResourceDeliveryFixture {
	t.Helper()
	f := manualResourceDeliveryFixture{notificationFixture: lifecycleBusinessFixture(t)}
	f.exec(t, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('manual-delivery-session',$1,now()+interval '1 hour')`, f.actor)
	f.credentialContext = WithStorageCredential(f.ctx, "manual-delivery-session", false)
	created, _, err := f.db.CreateManualResource(f.credentialContext, f.actor, "manual-delivery-inventory", ManualResourceInput{
		Name: "Manual delivery fixture", UpstreamID: "fixture-resource-id", Address: "203.0.113.119",
		Username: "fixture-private-user", Password: "fixture-private-password", CostCents: 1234,
		UpstreamExpiresAt: "2027-01-01T00:00:00+08:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.resource = created["id"].(int64)
	f.order, _ = lifecycleCheckout(t, f.notificationFixture, quantity)
	f.exec(t, `UPDATE services SET provision_mode='manual' WHERE order_id=$1`, f.order.OrderID)
	f.exec(t, `UPDATE users SET balance_cents=100000 WHERE id=$1`, f.customer)
	if _, err = f.db.PayInvoice(f.ctx, f.customer, f.order.InvoiceID, "manual-delivery-payment"); err != nil {
		t.Fatal(err)
	}
	for range quantity {
		job := lifecycleRunJob(t, f.notificationFixture)
		var version int64
		if err = f.db.Pool.QueryRow(f.ctx, "SELECT version FROM services WHERE id=$1", job.ServiceID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		f.services = append(f.services, job.ServiceID)
		f.inputs = append(f.inputs, ManualCompletion{JobID: job.ID, Note: "bound existing inventory", ManualResourceID: f.resource, ResourceExpectedVersion: 1, ExpectedVersion: version})
	}
	return f
}

func (f manualResourceDeliveryFixture) facts(t *testing.T) string {
	t.Helper()
	var out string
	if err := f.db.Pool.QueryRow(f.ctx, `SELECT jsonb_build_object(
 'resources',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM manual_resources x),
 'bindings',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]'::jsonb) FROM manual_resource_bindings x),
 'services',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM services x),
 'jobs',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM provision_jobs x),
 'events',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM notification_events x),
 'deliveries',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM notification_deliveries x),
 'audits',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_events x),
 'receipts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY scope,idempotency_key) FROM mutation_operations x)
 )::text`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func manualResourceDeliveryErrorCode(t *testing.T, err error) string {
	t.Helper()
	var conflictErr *Conflict
	if errors.As(err, &conflictErr) {
		return conflictErr.Code
	}
	var businessErr *AccountBusinessError
	if errors.As(err, &businessErr) {
		return businessErr.Code
	}
	t.Fatalf("expected a business rejection, got %v", err)
	return ""
}

func TestManualResourceDeliveryConcurrentBindingAndReplay(t *testing.T) {
	f := newManualResourceDeliveryFixture(t, 2)
	var inventorySecret []byte
	if err := f.db.Pool.QueryRow(f.ctx, "SELECT credentials_encrypted FROM manual_resources WHERE id=$1", f.resource).Scan(&inventorySecret); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(f.ctx)
	var blockerPID int32
	if err = blocker.QueryRow(f.ctx, "SELECT pg_backend_pid() FROM manual_resources WHERE id=$1 FOR UPDATE", f.resource).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	type completion struct {
		index int
		out   ServiceActionResult
		err   error
	}
	completed := make(chan completion, 2)
	for index := range f.services {
		go func(index int) {
			out, err := f.db.CompleteManualServiceJob(f.credentialContext, f.actor, f.services[index], f.inputs[index])
			completed <- completion{index, out, err}
		}(index)
	}
	// Observe both real PostgreSQL waits; the test must exercise resource
	// contention rather than two sequential completions scheduled by Go.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked int
		// PostgreSQL may queue the second tuple waiter behind the first
		// waiter rather than directly behind our blocking transaction.
		if err = f.db.Pool.QueryRow(f.ctx, `WITH RECURSIVE waiters(pid) AS (
 SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))
 UNION SELECT a.pid FROM pg_stat_activity a JOIN waiters w ON w.pid=ANY(pg_blocking_pids(a.pid))
) SELECT count(*) FROM waiters`, blockerPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("both deliveries did not reach the inventory row lock")
		}
		select {
		case result := <-completed:
			t.Fatalf("delivery %d returned before reaching inventory contention: %v", result.index, result.err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = blocker.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	results := []completion{<-completed, <-completed}
	winner := -1
	for _, result := range results {
		if result.err == nil {
			if winner != -1 || result.out.Replayed {
				t.Fatal("multiple delivery completions claimed one inventory resource", results)
			}
			winner = result.index
		} else if code := manualResourceDeliveryErrorCode(t, result.err); code != "manual_resource_version_conflict" && code != "manual_resource_unavailable" {
			t.Fatal("unexpected contending delivery result", result.err)
		}
	}
	if winner < 0 {
		t.Fatal("neither delivery completed", results)
	}
	loser := 1 - winner
	var state, jobState, username, upstream, address string
	var version, resourceVersion int64
	var serviceSecret []byte
	if err = f.db.Pool.QueryRow(f.ctx, `SELECT s.status,s.version,s.username,s.upstream_id,s.resource_info->>'dedicated_ip',s.credentials_encrypted,
 j.status,r.version FROM services s JOIN provision_jobs j ON j.id=$2 CROSS JOIN manual_resources r WHERE s.id=$1 AND r.id=$3`, f.services[winner], f.inputs[winner].JobID, f.resource).Scan(&state, &version, &username, &upstream, &address, &serviceSecret, &jobState, &resourceVersion); err != nil || state != "active" || jobState != "succeeded" || version != f.inputs[winner].ExpectedVersion+1 || resourceVersion != 2 || username != "fixture-private-user" || upstream != "fixture-resource-id" || address != "203.0.113.119" {
		t.Fatal("inventory delivery did not atomically update existing service/job facts", state, jobState, version, resourceVersion, err)
	}
	secret, err := provision.Unseal(f.db.ProvisionEncryptionKey, f.services[winner], serviceSecret)
	if err != nil || secret.Password != "fixture-private-password" || bytes.Equal(serviceSecret, inventorySecret) {
		t.Fatal("inventory credentials were not resealed for service identity", err)
	}
	if _, err = provision.Unseal(f.db.ProvisionEncryptionKey, f.services[loser], serviceSecret); err == nil {
		t.Fatal("service credential ciphertext accepted a different service identity")
	}
	if err = f.db.Pool.QueryRow(f.ctx, `SELECT s.status,j.status,s.version FROM services s JOIN provision_jobs j ON j.id=$2 WHERE s.id=$1`, f.services[loser], f.inputs[loser].JobID).Scan(&state, &jobState, &version); err != nil || jobState != "waiting_manual" || state == "active" || version != f.inputs[loser].ExpectedVersion {
		t.Fatal("losing delivery partially changed service/job", state, jobState, version, err)
	}
	if count := f.count(t, "SELECT count(*) FROM manual_resource_bindings WHERE released_at IS NULL"); count != 1 {
		t.Fatal("inventory was double bound", count)
	}
	lifecycleEventCount(t, f.notificationFixture, "service.provisioned", 1)
	before := f.facts(t)
	if replay, err := f.db.CompleteManualServiceJob(f.credentialContext, f.actor, f.services[winner], f.inputs[winner]); err != nil || !replay.Replayed || f.facts(t) != before {
		t.Fatal("same completed intent did not replay without new facts", replay, err)
	}
	changed := f.inputs[winner]
	changed.Note = "different completed delivery intent"
	if _, err = f.db.CompleteManualServiceJob(f.credentialContext, f.actor, f.services[winner], changed); manualResourceDeliveryErrorCode(t, err) != "delivery_conflict" || f.facts(t) != before {
		t.Fatal("changed completed intent was accepted", err)
	}
	// The username belongs on the delivered service, but neither credential
	// belongs in receipts or job/audit payloads.
	var leak bool
	if err = f.db.Pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE result::text LIKE '%fixture-private%')
 OR EXISTS(SELECT 1 FROM audit_events WHERE payload::text LIKE '%fixture-private%')
 OR EXISTS(SELECT 1 FROM mutation_operations WHERE result::text LIKE '%fixture-private%')`).Scan(&leak); err != nil || leak {
		t.Fatal("delivery secrets leaked into job, audit or receipt", err)
	}
	f.exec(t, "DELETE FROM sessions WHERE token_hash='manual-delivery-session'")
	if _, err = f.db.CompleteManualServiceJob(f.credentialContext, f.actor, f.services[winner], f.inputs[winner]); manualResourceDeliveryErrorCode(t, err) != "unauthorized" || f.facts(t) != before {
		t.Fatal("revoked credential recovered a completed delivery", err)
	}
}

func TestManualResourceDeliveryAuditFailureRollsBackAllFacts(t *testing.T) {
	f := newManualResourceDeliveryFixture(t, 1)
	before := f.facts(t)
	f.exec(t, `CREATE FUNCTION reject_manual_delivery_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='service.manual.completed' THEN RAISE EXCEPTION 'injected manual delivery audit failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_manual_delivery_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_manual_delivery_audit()`)
	if _, err := f.db.CompleteManualServiceJob(f.credentialContext, f.actor, f.services[0], f.inputs[0]); err == nil {
		t.Fatal("audit failure committed manual delivery")
	}
	if after := f.facts(t); after != before {
		t.Fatal("failed delivery left a binding, credential, service/job version, notification or receipt")
	}
	f.exec(t, "DROP TRIGGER reject_manual_delivery_audit ON audit_events")
	if _, err := f.db.CompleteManualServiceJob(f.credentialContext, f.actor, f.services[0], f.inputs[0]); err != nil {
		t.Fatal("rolled back inventory could not be delivered again", err)
	}
	if count := f.count(t, "SELECT count(*) FROM manual_resource_bindings"); count != 1 {
		t.Fatal("retry after rollback duplicated binding", count)
	}
	lifecycleEventCount(t, f.notificationFixture, "service.provisioned", 1)
}

func TestManualResourceDeliveryGuardsAreAtomic(t *testing.T) {
	f := newManualResourceDeliveryFixture(t, 1)
	var initialStatus string
	var initialCipher []byte
	if err := f.db.Pool.QueryRow(f.ctx, "SELECT status FROM services WHERE id=$1", f.services[0]).Scan(&initialStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(f.ctx, "SELECT credentials_encrypted FROM manual_resources WHERE id=$1", f.resource).Scan(&initialCipher); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, code string
		mutate     func()
		input      func(*ManualCompletion)
		noSession  bool
	}{
		{name: "missing credential", code: "unauthorized", noSession: true},
		{name: "service version", code: "stale_service", input: func(in *ManualCompletion) { in.ExpectedVersion++ }},
		{name: "inventory version", code: "manual_resource_version_conflict", input: func(in *ManualCompletion) { in.ResourceExpectedVersion++ }},
		{name: "missing version", code: "invalid_delivery", input: func(in *ManualCompletion) { in.ExpectedVersion = 0 }},
		{name: "identity override", code: "invalid_delivery", input: func(in *ManualCompletion) { in.UpstreamID = "override" }},
		{name: "username override", code: "invalid_delivery", input: func(in *ManualCompletion) { in.Username = "override" }},
		{name: "nonmanual", code: "invalid_delivery_state", mutate: func() { f.exec(t, "UPDATE services SET provision_mode='simulation' WHERE id=$1", f.services[0]) }},
		{name: "job pending", code: "invalid_delivery_state", mutate: func() { f.exec(t, "UPDATE provision_jobs SET status='pending' WHERE id=$1", f.inputs[0].JobID) }},
		{name: "active service", code: "invalid_delivery_state", mutate: func() { f.exec(t, "UPDATE services SET status='active' WHERE id=$1", f.services[0]) }},
		{name: "unreviewed migration", code: "import_review_required", mutate: func() { f.exec(t, "UPDATE services SET import_review_required=true WHERE id=$1", f.services[0]) }},
		{name: "unreviewed order", code: "order_not_deliverable", mutate: func() { f.exec(t, "UPDATE orders SET review_status='pending' WHERE id=$1", f.order.OrderID) }},
		{name: "unpaid invoice", code: "order_not_deliverable", mutate: func() { f.exec(t, "UPDATE invoices SET status='unpaid' WHERE id=$1", f.order.InvoiceID) }},
		{name: "cancelled order", code: "order_not_deliverable", mutate: func() { f.exec(t, "UPDATE orders SET status='cancelled' WHERE id=$1", f.order.OrderID) }},
		{name: "disabled resource", code: "manual_resource_unavailable", mutate: func() { f.exec(t, "UPDATE manual_resources SET status='disabled' WHERE id=$1", f.resource) }},
		{name: "retired resource", code: "manual_resource_unavailable", mutate: func() { f.exec(t, "UPDATE manual_resources SET status='retired' WHERE id=$1", f.resource) }},
		{name: "missing upstream identity", code: "manual_resource_identity_required", mutate: func() { f.exec(t, "UPDATE manual_resources SET upstream_id='' WHERE id=$1", f.resource) }},
		{name: "invalid ciphertext", code: "manual_resource_credentials_invalid", mutate: func() {
			f.exec(t, "UPDATE manual_resources SET credentials_encrypted='broken'::bytea WHERE id=$1", f.resource)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.exec(t, "UPDATE services SET provision_mode='manual',status=$2,import_review_required=false WHERE id=$1", f.services[0], initialStatus)
			f.exec(t, "UPDATE provision_jobs SET status='waiting_manual',action='create' WHERE id=$1", f.inputs[0].JobID)
			f.exec(t, "UPDATE orders SET status='paid',review_status='approved' WHERE id=$1", f.order.OrderID)
			f.exec(t, "UPDATE invoices SET status='paid' WHERE id=$1", f.order.InvoiceID)
			f.exec(t, "UPDATE manual_resources SET status='available',upstream_id='fixture-resource-id',credentials_encrypted=$2 WHERE id=$1", f.resource, initialCipher)
			input := f.inputs[0]
			if tc.input != nil {
				tc.input(&input)
			}
			if tc.mutate != nil {
				tc.mutate()
			}
			ctx := f.credentialContext
			if tc.noSession {
				ctx = f.ctx
			}
			before := f.facts(t)
			if _, err := f.db.CompleteManualServiceJob(ctx, f.actor, f.services[0], input); manualResourceDeliveryErrorCode(t, err) != tc.code || f.facts(t) != before {
				t.Fatal("invalid inventory delivery was not rejected atomically", tc.code, err)
			}
		})
	}
	t.Run("noncreate", func(t *testing.T) {
		// Job identity is immutable. Settle the fixture's old task, then use
		// the existing action and worker flows to create a real manual suspend
		// task rather than changing a create task's action with direct SQL.
		f.exec(t, "UPDATE provision_jobs SET status='cancelled' WHERE id=$1", f.inputs[0].JobID)
		f.exec(t, "UPDATE services SET status='active',provision_mode='manual',import_review_required=false WHERE id=$1", f.services[0])
		queued, err := f.db.EnqueueServiceAction(f.credentialContext, f.actor, f.services[0], "suspend", "manual-resource-noncreate-task", true)
		if err != nil {
			t.Fatal("could not create real noncreate delivery task", err)
		}
		job := lifecycleRunJob(t, f.notificationFixture)
		if job.ID != queued.JobID || job.Action != "suspend" {
			t.Fatal("wrong noncreate delivery task claimed", job)
		}
		input := f.inputs[0]
		input.JobID = job.ID
		if err = f.db.Pool.QueryRow(f.ctx, "SELECT version FROM services WHERE id=$1", f.services[0]).Scan(&input.ExpectedVersion); err != nil {
			t.Fatal(err)
		}
		before := f.facts(t)
		if _, err = f.db.CompleteManualServiceJob(f.credentialContext, f.actor, f.services[0], input); manualResourceDeliveryErrorCode(t, err) != "invalid_delivery_state" || f.facts(t) != before {
			t.Fatal("noncreate task accepted inventory or changed delivery facts", err)
		}
	})
}

func TestManualResourceDeliveryCredentialExpiryAfterResourceAndAuditWait(t *testing.T) {
	for _, phase := range []string{"resource", "audit"} {
		t.Run(phase, func(t *testing.T) {
			f := newManualResourceDeliveryFixture(t, 1)
			observer := certificationLockObserver(t, f.db)
			blocker, err := f.db.Pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(f.ctx)
			fragment := "SELECT status,upstream_id,address,credentials_encrypted,version FROM manual_resources"
			if phase == "resource" {
				_, err = blocker.Exec(f.ctx, "SELECT id FROM manual_resources WHERE id=$1 FOR UPDATE", f.resource)
			} else {
				// All provisional service, binding and notification writes happen
				// before the final audit; expiry here must roll all of them back.
				// A table lock would instead block the earlier impersonation
				// lookup made by credential validation, so gate only this audit.
				f.exec(t, `CREATE FUNCTION wait_manual_delivery_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='service.manual.completed' THEN PERFORM pg_advisory_xact_lock(70108195); END IF; RETURN NEW; END $$;
 CREATE TRIGGER wait_manual_delivery_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_manual_delivery_audit()`)
				_, err = blocker.Exec(f.ctx, "SELECT pg_advisory_xact_lock(70108195)")
				fragment = "INSERT INTO audit_events"
			}
			if err != nil {
				t.Fatal(err)
			}
			var expires time.Time
			if err = f.db.Pool.QueryRow(f.ctx, "UPDATE sessions SET expires_at=clock_timestamp()+interval '2 seconds' WHERE token_hash='manual-delivery-session' RETURNING expires_at").Scan(&expires); err != nil {
				t.Fatal(err)
			}
			// Snapshot before the audit lock starts blocking the observer.
			var factsBefore string
			if phase == "resource" {
				factsBefore = f.facts(t)
			}
			finished := make(chan error, 1)
			go func() {
				_, err := f.db.CompleteManualServiceJob(f.credentialContext, f.actor, f.services[0], f.inputs[0])
				finished <- err
			}()
			waitCertificationQueryLock(t, observer, fragment)
			for {
				var expired bool
				if err = observer.QueryRow(f.ctx, "SELECT clock_timestamp()>$1", expires).Scan(&expired); err != nil {
					t.Fatal(err)
				}
				if expired {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err = blocker.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("expired delivery remained blocked")
			}
			if manualResourceDeliveryErrorCode(t, err) != "unauthorized" {
				t.Fatal("expired credential committed inventory delivery", phase, err)
			}
			if phase == "resource" && f.facts(t) != factsBefore {
				t.Fatal("expiry after inventory row wait left delivery facts")
			}
			var bound, active, completed, advanced, notifications int
			if err = f.db.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM manual_resource_bindings),
 (SELECT count(*) FROM services WHERE status='active' OR octet_length(credentials_encrypted)>0),
 (SELECT count(*) FROM provision_jobs WHERE status='succeeded'),
 (SELECT count(*) FROM manual_resources WHERE version<>1),
 (SELECT count(*) FROM notification_events WHERE kind='service.provisioned')`).Scan(&bound, &active, &completed, &advanced, &notifications); err != nil || bound+active+completed+advanced+notifications != 0 {
				t.Fatal("expiry after delivery wait did not roll back provisional facts", phase, bound, active, completed, advanced, notifications, err)
			}
		})
	}
}
