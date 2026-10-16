package store

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestTicketNotificationDeliveryTransactions(t *testing.T) {
	db := isolatedTestStore(t)
	ctx := t.Context()
	staff, owner := topUpFixture(t, db)
	db.ProvisionEncryptionKey = []byte("01234567890123456789012345678901")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := db.Pool.Exec(ctx, `UPDATE users SET email_verified=true,phone='+8613800138000',phone_verified=true WHERE id=$1;`, owner)
	must(err)
	_, err = db.Pool.Exec(ctx, `UPDATE users SET phone_verified=true WHERE id=$1`, owner)
	must(err)
	_, err = db.Pool.Exec(ctx, `UPDATE users SET email_verified=true WHERE id=$1`, staff)
	must(err)
	_, err = db.Pool.Exec(ctx, `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled) VALUES('ticket-mail','mail','smtp','Local test',true),('ticket-sms','sms','http','Local SMS',true); UPDATE notification_templates SET active=true WHERE kind LIKE 'ticket_%';`)
	must(err)
	_, err = db.Pool.Exec(ctx, `INSERT INTO user_ticket_notification_preferences(user_id,sms_enabled) VALUES($1,true)`, owner)
	must(err)
	_, err = db.Pool.Exec(ctx, `INSERT INTO customer_contacts(customer_id,email,status,support_emails) VALUES($1,'TOPUP-CUSTOMER@example.test','active',true),($1,'inactive@example.test','inactive',true),($1,'contact@example.test','active',true)`, owner)
	must(err)
	var ticket, reply int64
	must(db.Pool.QueryRow(ctx, `INSERT INTO tickets(ticket_no,user_id,subject,body,assignee_id) VALUES('TN-1',$1,'Public subject','Public create',$2) RETURNING id`, owner, staff).Scan(&ticket))
	must(db.Pool.QueryRow(ctx, `INSERT INTO ticket_replies(ticket_id,author_id,body) VALUES($1,$2,'Public reply') RETURNING id`, ticket, staff).Scan(&reply))
	queue := func(kind string, rid, actor int64) error {
		tx, e := db.Pool.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if e = db.QueueTicketNotification(ctx, tx, ticket, rid, actor, kind, "https://billing.example.test"); e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	must(queue("customer_created", 0, owner))
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- queue("staff_replied", reply, staff) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		must(e)
	}
	var events, queued, duplicates, unsubscribed int
	must(db.Pool.QueryRow(ctx, `SELECT count(*) FROM ticket_notification_events`).Scan(&events))
	if events != 2 {
		t.Fatalf("events=%d", events)
	}
	must(db.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE outcome='queued'),count(*) FILTER(WHERE reason='duplicate_address'),count(*) FILTER(WHERE reason='unsubscribed') FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.kind='staff_replied'`).Scan(&queued, &duplicates, &unsubscribed))
	if queued != 3 || duplicates != 1 || unsubscribed != 0 {
		t.Fatalf("decisions queued=%d duplicate=%d unsubscribed=%d", queued, duplicates, unsubscribed)
	}
	var customerURL, staffURL string
	must(db.Pool.QueryRow(ctx, `SELECT d.rendered_body FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.kind='staff_replied' AND d.channel='email' AND d.recipient_kind='user'`).Scan(&customerURL))
	must(db.Pool.QueryRow(ctx, `SELECT d.rendered_body FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.kind='customer_created' AND d.channel='email'`).Scan(&staffURL))
	if !strings.Contains(customerURL, fmt.Sprintf("https://billing.example.test/console/tickets/%d", ticket)) || !strings.Contains(staffURL, fmt.Sprintf("https://billing.example.test/admin/tickets/%d", ticket)) {
		t.Fatalf("incorrect URLs customer=%q staff=%q", customerURL, staffURL)
	}
	// Existing workers consume the exact persisted payload, no external channels are contacted.
	email, e := db.ClaimEmailJob(ctx)
	must(e)
	if email.Body == "" || email.ChannelInstanceID != "ticket-mail" {
		t.Fatalf("email=%+v", email)
	}
	must(db.CompleteEmailJob(ctx, *email, nil))
	sms, e := db.ClaimSMSJob(ctx)
	must(e)
	if sms.Body == "" || sms.ChannelInstanceID != "ticket-sms" {
		t.Fatalf("sms=%+v", sms)
	}
	must(db.CompleteSMSJob(ctx, *sms, nil))
	var sent int
	must(db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM email_outbox WHERE status='sent')+(SELECT count(*) FROM sms_outbox WHERE status='sent')`).Scan(&sent))
	if sent != 2 {
		t.Fatalf("sent=%d", sent)
	}
	// A database delivery failure must roll back its event with the caller transaction.
	var reply2 int64
	must(db.Pool.QueryRow(ctx, `INSERT INTO ticket_replies(ticket_id,author_id,body) VALUES($1,$2,'Second public reply') RETURNING id`, ticket, staff).Scan(&reply2))
	_, err = db.Pool.Exec(ctx, `CREATE FUNCTION reject_ticket_delivery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER reject_ticket_delivery BEFORE INSERT ON ticket_notification_deliveries FOR EACH ROW EXECUTE FUNCTION reject_ticket_delivery()`)
	must(err)
	if e = queue("staff_replied", reply2, staff); e == nil {
		t.Fatal("injected failure accepted")
	}
	var exists bool
	must(db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_notification_events WHERE reply_id=$1)`, reply2).Scan(&exists))
	if exists {
		t.Fatal("failed event committed")
	}
	_, err = db.Pool.Exec(ctx, `DROP TRIGGER reject_ticket_delivery ON ticket_notification_deliveries`)
	must(err)
	db.ProvisionEncryptionKey = nil
	if e = queue("staff_replied", reply2, staff); e == nil {
		t.Fatal("encryption failure accepted")
	}
	must(db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_notification_events WHERE reply_id=$1)`, reply2).Scan(&exists))
	if exists {
		t.Fatal("encryption failure committed event")
	}
	db.ProvisionEncryptionKey = []byte("01234567890123456789012345678901")
	_, err = db.Pool.Exec(ctx, `UPDATE user_ticket_notification_preferences SET email_enabled=false,sms_enabled=false,version=version+1 WHERE user_id=$1`, owner)
	must(err)
	_, err = db.Pool.Exec(ctx, `DELETE FROM notification_templates WHERE kind='ticket_staff_replied'`)
	must(err)
	must(queue("staff_replied", reply2, staff))
	must(db.Pool.QueryRow(ctx, `SELECT count(*) FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.reply_id=$1 AND d.recipient_kind='user' AND d.reason='unsubscribed' AND d.preference_version=2`, reply2).Scan(&sent))
	if sent != 2 {
		t.Fatalf("preference snapshots=%d", sent)
	}
	must(db.Pool.QueryRow(ctx, `SELECT count(*) FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.reply_id=$1 AND d.reason='missing_template'`, reply2).Scan(&sent))
	if sent != 2 {
		t.Fatalf("missing-template decisions=%d", sent)
	}
	// Inactive owners remain visible as explicit skipped decisions; contacts keep their own status.
	_, err = db.Pool.Exec(ctx, `UPDATE users SET status='disabled' WHERE id=$1`, owner)
	must(err)
	var inactiveReply int64
	must(db.Pool.QueryRow(ctx, `INSERT INTO ticket_replies(ticket_id,author_id,body) VALUES($1,$2,'Inactive owner') RETURNING id`, ticket, staff).Scan(&inactiveReply))
	must(queue("staff_replied", inactiveReply, staff))
	must(db.Pool.QueryRow(ctx, `SELECT count(*) FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.reply_id=$1 AND d.recipient_kind='user' AND reason='inactive_recipient'`, inactiveReply).Scan(&sent))
	if sent != 2 {
		t.Fatalf("inactive owner decisions=%d", sent)
	}
	// The idcsmart gateway requires an approved per-kind template ID; no default template is substituted.
	_, err = db.Pool.Exec(ctx, `UPDATE users SET status='active' WHERE id=$1`, owner)
	must(err)
	_, err = db.Pool.Exec(ctx, `UPDATE user_ticket_notification_preferences SET sms_enabled=true WHERE user_id=$1`, owner)
	must(err)
	_, err = db.Pool.Exec(ctx, `INSERT INTO notification_templates(channel,kind,name,subject,body,active) VALUES('sms','ticket_staff_replied','Test','Test','Short reply',true); UPDATE channel_instances SET implementation_id='idcsmart' WHERE id='ticket-sms'`)
	must(err)
	var templateReply int64
	must(db.Pool.QueryRow(ctx, `INSERT INTO ticket_replies(ticket_id,author_id,body) VALUES($1,$2,'Missing provider template') RETURNING id`, ticket, staff).Scan(&templateReply))
	must(queue("staff_replied", templateReply, staff))
	must(db.Pool.QueryRow(ctx, `SELECT count(*) FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.reply_id=$1 AND reason='missing_provider_template'`, templateReply).Scan(&sent))
	if sent != 1 {
		t.Fatalf("missing provider decisions=%d", sent)
	}
	// The database and verification endpoint both require canonical E.164; equivalent
	// numbers with interior separators cannot acquire a stored verified identity.
	_, err = db.Pool.Exec(ctx, `UPDATE users SET phone='+86 13800138000' WHERE id=$1`, owner)
	if err == nil {
		t.Fatal("database accepted noncanonical phone")
	}

}

func TestTicketNotificationStaffEligibilityAndSkips(t *testing.T) {
	db := isolatedTestStore(t)
	ctx := t.Context()
	root, owner := topUpFixture(t, db)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var role, department, staff, wrong, disabled, ticket int64
	must(db.Pool.QueryRow(ctx, `INSERT INTO admin_roles(name,permissions) VALUES('ticket support',ARRAY['tickets.manage']) RETURNING id`).Scan(&role))
	must(db.Pool.QueryRow(ctx, `INSERT INTO ticket_departments(name,slug) VALUES('Support','support') RETURNING id`).Scan(&department))
	must(db.Pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name,role,admin_role_id,email_verified) VALUES('legal','legal@example.test','unused','Legal','admin',$1,true) RETURNING id`, role).Scan(&staff))
	must(db.Pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name,role,admin_role_id,email_verified) VALUES('wrong','wrong@example.test','unused','Wrong','admin',$1,true) RETURNING id`, role).Scan(&wrong))
	must(db.Pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name,role,admin_role_id,email_verified,status) VALUES('disabled','disabled@example.test','unused','Disabled','admin',$1,true,'disabled') RETURNING id`, role).Scan(&disabled))
	_, err := db.Pool.Exec(ctx, `INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)`, staff, department)
	must(err)
	must(db.Pool.QueryRow(ctx, `INSERT INTO tickets(ticket_no,user_id,subject,body,department_id,assignee_id) VALUES('TN-SCOPE',$1,'subject','body',$2,$3) RETURNING id`, owner, department, staff).Scan(&ticket))
	queue := func(rid, actor int64, kind string) error {
		tx, e := db.Pool.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if e = db.QueueTicketNotification(ctx, tx, ticket, rid, actor, kind, "https://billing.example.test"); e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	must(queue(0, owner, "customer_created"))
	var count int
	must(db.Pool.QueryRow(ctx, `SELECT count(*) FROM ticket_notification_deliveries WHERE recipient_id=$1 AND reason='inactive_template'`, staff).Scan(&count))
	if count != 1 {
		t.Fatalf("assigned eligibility count=%d", count)
	}
	must(db.Pool.QueryRow(ctx, `SELECT count(*) FROM ticket_notification_deliveries WHERE recipient_id<>$1`, staff).Scan(&count))
	if count != 0 {
		t.Fatal("non-assignee received assigned event")
	}
	// Invalid assignment falls back to all currently authorized staff; the wrong-department and disabled users stay excluded.
	_, err = db.Pool.Exec(ctx, `UPDATE tickets SET assignee_id=$2 WHERE id=$1`, ticket, wrong)
	must(err)
	var reply int64
	must(db.Pool.QueryRow(ctx, `INSERT INTO ticket_replies(ticket_id,author_id,body) VALUES($1,$2,'customer reply') RETURNING id`, ticket, owner).Scan(&reply))
	must(queue(reply, owner, "customer_replied"))
	must(db.Pool.QueryRow(ctx, `SELECT count(*) FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.reply_id=$1 AND d.recipient_id IN ($2,$3)`, reply, wrong, disabled).Scan(&count))
	if count != 0 {
		t.Fatal("illegal staff received fallback")
	}
	must(db.Pool.QueryRow(ctx, `SELECT count(DISTINCT recipient_id) FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.reply_id=$1`, reply).Scan(&count))
	if count != 2 {
		t.Fatalf("fallback count=%d", count)
	}
	// NULL department requires the same scope function, never blanket admin eligibility.
	_, err = db.Pool.Exec(ctx, `UPDATE tickets SET department_id=NULL,assignee_id=NULL WHERE id=$1`, ticket)
	must(err)
	must(db.Pool.QueryRow(ctx, `INSERT INTO ticket_replies(ticket_id,author_id,body) VALUES($1,$2,'null department reply') RETURNING id`, ticket, owner).Scan(&reply))
	must(queue(reply, owner, "customer_replied"))
	must(db.Pool.QueryRow(ctx, `SELECT count(DISTINCT recipient_id) FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.reply_id=$1 AND recipient_id=$2`, reply, root).Scan(&count))
	if count != 1 {
		t.Fatal("super admin missing for NULL department")
	}
	must(db.Pool.QueryRow(ctx, `SELECT count(*) FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.reply_id=$1 AND recipient_id=$2`, reply, staff).Scan(&count))
	if count != 0 {
		t.Fatal("NULL department scope bypassed")
	}
}
