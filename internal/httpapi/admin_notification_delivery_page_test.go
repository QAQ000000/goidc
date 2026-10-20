package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAdminNotificationDeliveryPageScopeAndValidation(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "delivery-page-root", "super_admin")
	staffID, staff := user(t, db, h, "delivery-page-staff", "admin")
	owner, customer := user(t, db, h, "delivery-page-owner", "customer")
	other, _ := user(t, db, h, "delivery-page-other", "customer")
	contentRole(t, db, staffID, "notifications.view")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", owner, staffID); err != nil {
		t.Fatal(err)
	}
	const endpoint = "/api/v1/admin/notification-deliveries/page"
	expect(t, request(h, "GET", endpoint, nil, "", ""), http.StatusUnauthorized)
	expect(t, request(h, "GET", endpoint, nil, customer, ""), http.StatusUnauthorized)
	for _, value := range []string{"", "0", "-1", "+1", " 1", "1 ", "1x", "1.0", "9223372036854775808"} {
		expect(t, request(h, "GET", endpoint+"?customer_id="+url.QueryEscape(value), nil, staff, ""), http.StatusBadRequest)
	}
	expect(t, request(h, "GET", fmt.Sprintf("%s?customer_id=%d&customer_id=%d", endpoint, owner, owner), nil, staff, ""), http.StatusBadRequest)
	for _, id := range []int64{other, staffID, 9223372036854775807} {
		expect(t, request(h, "GET", fmt.Sprintf("%s?customer_id=%d", endpoint, id), nil, staff, ""), http.StatusNotFound)
	}
	for _, query := range []string{"channel=bot", "channel=mail", "status=sent", "status=dead_letter", "event_kind=ticket.created", "event_kind=marketing.sent", "from=2026-02-30", "from=2026-02-03&to=2026-02-01", "channel=email&channel=sms", "status=pending&status=unknown"} {
		expect(t, request(h, "GET", endpoint+"?"+query, nil, root, ""), http.StatusBadRequest)
	}
	expect(t, request(h, "GET", fmt.Sprintf("%s?customer_id=%d", endpoint, owner), nil, staff, ""), http.StatusOK)
	// A read-only projection must reauthorize at the next request, including
	// explicit customer scope and notification permission revocation.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=NULL WHERE id=$1", owner); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", fmt.Sprintf("%s?customer_id=%d", endpoint, owner), nil, staff, ""), http.StatusNotFound)
	contentRole(t, db, staffID, "customers.view")
	expect(t, request(h, "GET", endpoint, nil, staff, ""), http.StatusForbidden)
}

func TestAdminNotificationDeliveryPageFiltersPaginationAndPrivacy(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "delivery-filters-root", "super_admin")
	staffID, staff := user(t, db, h, "delivery-filters-staff", "admin")
	owner, _ := user(t, db, h, "delivery-filters-owner", "customer")
	other, _ := user(t, db, h, "delivery-filters-other", "customer")
	contentRole(t, db, staffID, "notifications.view")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", owner, staffID); err != nil {
		t.Fatal(err)
	}
	insert := func(user int64, kind, channel, status, created string) int64 {
		t.Helper()
		var invoice, event, delivery int64
		if err := db.Pool.QueryRow(t.Context(), "INSERT INTO invoices(invoice_no,user_id,status,amount_cents) VALUES('delivery-'||gen_random_uuid()::text,$1,'paid',1) RETURNING id", user).Scan(&invoice); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO notification_events(kind,dedupe_key,user_id,invoice_id,payload)
 VALUES($1,gen_random_uuid()::text,$2,$3,'{"secret":"private-delivery-payload"}') RETURNING id`, kind, user, invoice).Scan(&event); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO notification_deliveries(event_id,route_key,channel,status,target,subject,body,code,provider_message_id,created_at)
 VALUES($1,'fixture-'||gen_random_uuid()::text,$2,$3,'private-delivery-target','private-delivery-subject','private-delivery-body','fixture_safe_code','safe-message-id',$4) RETURNING id`, event, channel, status, created).Scan(&delivery); err != nil {
			t.Fatal(err)
		}
		return delivery
	}
	for _, channel := range []string{"telegram", "web_push", "inbox", "mail", "sms"} {
		insert(owner, "invoice.paid", channel, "accepted", "2026-01-01T16:00:00Z")
	}
	insert(owner, "invoice.overdue", "telegram", "unknown", "2026-01-01T15:59:59Z")
	insert(other, "invoice.paid", "telegram", "accepted", "2026-01-01T16:00:00Z")
	get := func(query, cookie string) map[string]any {
		t.Helper()
		w := request(h, "GET", "/api/v1/admin/notification-deliveries/page?"+query, nil, cookie, "")
		expect(t, w, http.StatusOK)
		if strings.Contains(w.Body.String(), "private-delivery-") {
			t.Fatal("history exposed private event or message fields")
		}
		result := data(t, w)
		allowed := map[string]bool{"id": true, "event_id": true, "user_id": true, "event_kind": true, "channel": true, "instance_id": true, "status": true, "code": true, "attempts": true, "provider_message_id": true, "created_at": true, "expires_at": true}
		for _, raw := range result["items"].([]any) {
			for key := range raw.(map[string]any) {
				if !allowed[key] {
					t.Fatalf("unapproved delivery projection field: %s", key)
				}
			}
		}
		return result
	}
	page := get("limit=2&offset=1", staff)
	if page["total"] != float64(6) || page["limit"] != float64(2) || page["offset"] != float64(1) || len(page["items"].([]any)) != 2 {
		t.Fatal("filtered pagination", page)
	}
	channels := map[string]int{}
	for _, raw := range get("limit=100", staff)["items"].([]any) {
		row := raw.(map[string]any)
		if row["user_id"] != float64(owner) {
			t.Fatal("foreign customer delivery leaked")
		}
		channels[row["channel"].(string)]++
	}
	for _, channel := range []string{"telegram", "web_push", "inbox", "email", "sms"} {
		if channels[channel] < 1 {
			t.Fatalf("missing channel %s", channel)
		}
	}
	for _, query := range []string{"event_kind=invoice.overdue&status=unknown&channel=telegram", "channel=web_push&status=accepted", "channel=email&status=archived", "channel=sms&status=archived"} {
		if result := get(query, staff); result["total"] != float64(1) {
			t.Fatal("projection-aware filter", query, result)
		}
	}
	if result := get("from=2026-01-02&to=2026-01-02", staff); result["total"] != float64(5) {
		t.Fatal("Shanghai boundary excluded or included wrong deliveries", result)
	}
	if result := get(fmt.Sprintf("customer_id=%d&limit=100", owner), root); result["total"] != float64(6) {
		t.Fatal("explicit customer filter", result)
	}
	if result := get("offset=999", staff); result["total"] != float64(6) || len(result["items"].([]any)) != 0 {
		t.Fatal("empty page lost total", result)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=NULL WHERE id=$1", owner); err != nil {
		t.Fatal(err)
	}
	if result := get("limit=100", staff); result["total"] != float64(0) || len(result["items"].([]any)) != 0 {
		t.Fatal("revoked customer scope retained deliveries", result)
	}
}

func TestAdminNotificationDeliveryPageLegacyStatusProjection(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "delivery-legacy-root", "super_admin")
	owner, _ := user(t, db, h, "delivery-legacy-owner", "customer")
	var invoice, event int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO invoices(invoice_no,user_id,status,amount_cents) VALUES('delivery-legacy-invoice',$1,'paid',1) RETURNING id", owner).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO notification_events(kind,dedupe_key,user_id,invoice_id,payload)
 VALUES('invoice.paid','delivery-legacy-event',$1,$2,'{}') RETURNING id`, owner, invoice).Scan(&event); err != nil {
		t.Fatal(err)
	}
	var mail, sms int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind,status,attempts)
 VALUES('private-delivery-address@example.test','private-delivery-subject','private-delivery-body','payment_success','sent',3) RETURNING id`).Scan(&mail); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO sms_outbox(recipient,body_encrypted,kind,status,attempts,expires_at)
 VALUES('+8613800138000','private-delivery-key'::bytea,'payment_success','dead_letter',5,now()) RETURNING id`).Scan(&sms); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO notification_deliveries(event_id,route_key,channel,status,legacy_email_outbox_id,legacy_sms_outbox_id)
 VALUES($1,'mail','mail','pending',$2,NULL),($1,'sms','sms','pending',NULL,$3)`, event, mail, sms); err != nil {
		t.Fatal(err)
	}
	const endpoint = "/api/v1/admin/notification-deliveries/page"
	for _, tc := range []struct {
		query, status string
		attempts      float64
	}{{"channel=email&status=accepted", "accepted", 3}, {"channel=sms&status=failed", "failed", 5}} {
		w := request(h, "GET", endpoint+"?"+tc.query, nil, root, "")
		expect(t, w, http.StatusOK)
		page := data(t, w)
		if page["total"] != float64(1) || len(page["items"].([]any)) != 1 {
			t.Fatal("legacy projected filter", page)
		}
		row := page["items"].([]any)[0].(map[string]any)
		if row["status"] != tc.status || row["attempts"] != tc.attempts || strings.Contains(w.Body.String(), "private-delivery-") {
			t.Fatal("legacy status or privacy", row)
		}
	}
	existing, err := db.NotificationDeliveriesForAdmin(t.Context(), rootID, 20, 0)
	if err != nil || len(existing) != 2 || existing[0].Status != "failed" || existing[1].Status != "accepted" {
		t.Fatal("existing notification center projection changed", existing, err)
	}
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM email_outbox WHERE id=$1", mail); err != nil {
		t.Fatal(err)
	}
	w := request(h, "GET", endpoint+"?channel=email&status=archived", nil, root, "")
	expect(t, w, http.StatusOK)
	page := data(t, w)
	if page["total"] != float64(1) || page["items"].([]any)[0].(map[string]any)["code"] != "legacy_log_removed" {
		t.Fatal("deleted legacy outbox did not remain archived", page)
	}
}
