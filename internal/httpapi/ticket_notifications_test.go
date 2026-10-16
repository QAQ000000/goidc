package httpapi

import (
	"fmt"
	"testing"
)

func TestTicketNotificationHTTPReplayPreferencesAndPrivacy(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "ticket-notify-customer", "customer")
	_, other := user(t, db, h, "ticket-notify-other", "customer")
	_, staff := user(t, db, h, "ticket-notify-staff", "super_admin")
	get := request(h, "GET", "/api/v1/me/ticket-notification-preferences", nil, customer, "")
	expect(t, get, 200)
	if p := data(t, get); p["email_enabled"] != true || p["sms_enabled"] != false || p["version"] != float64(1) {
		t.Fatalf("defaults: %#v", p)
	}
	pref := map[string]any{"email_enabled": true, "sms_enabled": true, "expected_version": 1}
	expect(t, request(h, "PATCH", "/api/v1/me/ticket-notification-preferences", pref, customer, ""), 200)
	expect(t, request(h, "PATCH", "/api/v1/me/ticket-notification-preferences", pref, customer, ""), 409)
	in := map[string]any{"subject": "通知工单", "body": "公开正文", "priority": "normal", "idempotency_key": "ticket-create-replay"}
	created := request(h, "POST", "/api/v1/tickets", in, customer, "")
	expect(t, created, 201)
	id := data(t, created)["id"]
	again := request(h, "POST", "/api/v1/tickets", in, customer, "")
	expect(t, again, 201)
	if data(t, again)["id"] != id {
		t.Fatal("creation replay created ticket")
	}
	in["body"] = "其他正文"
	expect(t, request(h, "POST", "/api/v1/tickets", in, customer, ""), 409)
	path := fmt.Sprintf("/api/v1/tickets/%.0f", id)
	reply := map[string]any{"body": "客户回复", "version": 1, "idempotency_key": "ticket-reply-replay"}
	first := request(h, "POST", path+"/replies", reply, customer, "")
	expect(t, first, 200)
	repeat := request(h, "POST", path+"/replies", reply, customer, "")
	expect(t, repeat, 200)
	if data(t, first)["reply_id"] != data(t, repeat)["reply_id"] {
		t.Fatal("reply replay created another reply")
	}
	expect(t, request(h, "POST", path+"/replies", reply, other, ""), 404)
	expect(t, request(h, "POST", path+"/replies", map[string]any{"body": "客服公开回复", "version": 2}, staff, ""), 200)
	summary := request(h, "GET", path+"/notifications", nil, customer, "")
	expect(t, summary, 200)
	for _, raw := range data(t, summary)["items"].([]any) {
		item := raw.(map[string]any)
		if item["address"] != nil || item["recipient_kind"] != "user" {
			t.Fatalf("privacy: %#v", item)
		}
	}
	expect(t, request(h, "GET", path+"/notifications", nil, other, ""), 404)
	var replies, events int
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ticket_replies WHERE ticket_id=$1),(SELECT count(*) FROM ticket_notification_events WHERE ticket_id=$1)`, int64(id.(float64))).Scan(&replies, &events); err != nil {
		t.Fatal(err)
	}
	if replies != 2 || events != 3 {
		t.Fatalf("replies=%d events=%d", replies, events)
	}
	// A transactional delivery failure must also remove the ticket, receipt and audit.
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_ticket_notification() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected notification failure'; END $$; CREATE TRIGGER reject_ticket_notification BEFORE INSERT ON ticket_notification_events FOR EACH ROW EXECUTE FUNCTION reject_ticket_notification()`); err != nil {
		t.Fatal(err)
	}
	failIn := map[string]any{"subject": "rollback-notify", "body": "正文", "priority": "normal", "idempotency_key": "ticket-create-rollback"}
	expect(t, request(h, "POST", "/api/v1/tickets", failIn, customer, ""), 500)
	var count int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM tickets WHERE user_id=$1 AND subject='rollback-notify'`, customerID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback count=%d err=%v", count, err)
	}
}

func TestTicketNotificationReplayUsesCurrentAuthority(t *testing.T) {
	db, h := testServer(t)
	ownerID, owner := user(t, db, h, "notify-authority-owner", "customer")
	otherID, _ := user(t, db, h, "notify-authority-other", "customer")
	staffID, staff := user(t, db, h, "notify-authority-staff", "admin")
	contentRole(t, db, staffID, "tickets.view", "tickets.manage", "notifications.view")
	var dept int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO ticket_departments(name,slug) VALUES('权限部门','notify-authority') RETURNING id`).Scan(&dept); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET ticket_scope='departments' WHERE id=$1`, staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)`, staffID, dept); err != nil {
		t.Fatal(err)
	}
	in := map[string]any{"subject": "权限", "body": "正文", "priority": "normal", "department_id": dept, "idempotency_key": "authority-create"}
	created := request(h, "POST", "/api/v1/tickets", in, owner, "")
	expect(t, created, 201)
	id := int64(data(t, created)["id"].(float64))
	path := fmt.Sprintf("/api/v1/tickets/%d", id)
	reply := map[string]any{"body": "公开回复", "version": 1, "idempotency_key": "authority-reply"}
	expect(t, request(h, "POST", path+"/replies", reply, staff, ""), 200)
	// Link one synthetic outbox to the real delivery; no worker or external I/O.
	var outboxID int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind) VALUES('private@example.test','scope trace','body','ticket_customer_created') RETURNING id`).Scan(&outboxID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE ticket_notification_deliveries SET email_outbox_id=$2 WHERE id=(SELECT d.id FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.ticket_id=$1 AND d.channel='email' ORDER BY d.id LIMIT 1)`, id, outboxID); err != nil {
		t.Fatal(err)
	}
	visibleLog := request(h, "GET", fmt.Sprintf("/api/v1/admin/logs/email?ticket_id=%d", id), nil, staff, "")
	expect(t, visibleLog, 200)
	if data(t, visibleLog)["total"] != float64(1) {
		t.Fatalf("missing ticket log: %#v", data(t, visibleLog))
	}
	if _, err := db.Pool.Exec(t.Context(), `DELETE FROM admin_ticket_departments WHERE user_id=$1`, staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", path+"/replies", reply, staff, ""), 404)
	expect(t, request(h, "GET", path+"/notifications", nil, staff, ""), 404)
	for _, suffix := range []string{"", fmt.Sprintf("?ticket_id=%d", id)} {
		hidden := request(h, "GET", "/api/v1/admin/logs/email"+suffix, nil, staff, "")
		expect(t, hidden, 200)
		if data(t, hidden)["total"] != float64(0) {
			t.Fatalf("revoked log leaked: %#v", data(t, hidden))
		}
	}

	if _, err := db.Pool.Exec(t.Context(), `UPDATE tickets SET user_id=$2 WHERE id=$1`, id, otherID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/tickets", in, owner, ""), 404)
	if _, err := db.Pool.Exec(t.Context(), `UPDATE tickets SET user_id=$2 WHERE id=$1`, id, ownerID); err != nil {
		t.Fatal(err)
	}
	second := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "目标", "body": "正文", "priority": "normal"}, owner, "")
	expect(t, second, 201)
	if _, err := db.Pool.Exec(t.Context(), `UPDATE tickets SET merged_ticket_id=$2 WHERE id=$1`, id, int64(data(t, second)["id"].(float64))); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/tickets", in, owner, ""), 404)
	expect(t, request(h, "POST", path+"/replies", reply, owner, ""), 404)
}

func TestTicketNotificationConcurrentKeyedWrites(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "notify-concurrent", "customer")
	create := map[string]any{"subject": "并发通知", "body": "正文", "priority": "normal", "idempotency_key": "concurrent-create"}
	codes := make(chan int, 4)
	for range 4 {
		go func() { codes <- request(h, "POST", "/api/v1/tickets", create, customer, "").Code }()
	}
	for range 4 {
		if code := <-codes; code != 201 {
			t.Fatalf("create status %d", code)
		}
	}
	var id int64
	var count int
	if err := db.Pool.QueryRow(t.Context(), `SELECT min(id),count(*) FROM tickets WHERE subject='并发通知'`).Scan(&id, &count); err != nil || count != 1 {
		t.Fatalf("creation count=%d err=%v", count, err)
	}
	reply := map[string]any{"body": "并发回复", "version": 1, "idempotency_key": "concurrent-reply"}
	path := fmt.Sprintf("/api/v1/tickets/%d/replies", id)
	for range 4 {
		go func() { codes <- request(h, "POST", path, reply, customer, "").Code }()
	}
	for range 4 {
		if code := <-codes; code != 200 {
			t.Fatalf("reply status %d", code)
		}
	}
	var version, events int
	if err := db.Pool.QueryRow(t.Context(), `SELECT version,(SELECT count(*) FROM ticket_replies WHERE ticket_id=$1),(SELECT count(*) FROM ticket_notification_events WHERE ticket_id=$1) FROM tickets WHERE id=$1`, id).Scan(&version, &count, &events); err != nil {
		t.Fatal(err)
	}
	if version != 2 || count != 1 || events != 2 {
		t.Fatalf("version=%d replies=%d events=%d", version, count, events)
	}
}
