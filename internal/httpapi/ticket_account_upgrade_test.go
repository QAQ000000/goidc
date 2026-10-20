package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// Historical administrators could own tickets before accounts were physically
// separated. Keep those tickets usable without granting customer privileges.
func TestTicketAccountUpgrade181PreservesHistoricalAdministratorTickets(t *testing.T) {
	db, h := testServer(t)
	resetHistoricalAccountTestSchema(t, db, "181_auth_login_portal.sql")
	hash, err := bcrypt.GenerateFromPassword([]byte("historical-ticket-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	var roleID, ownerID, staffID, customerID, departmentID int64
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO admin_roles(name,permissions) VALUES('历史工单所有者','{}') RETURNING id`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	for _, account := range []struct {
		name, role string
		adminRole  *int64
		id         *int64
	}{
		{"historical-ticket-owner", "admin", &roleID, &ownerID},
		{"historical-ticket-handler", "super_admin", nil, &staffID},
		{"historical-ticket-customer", "customer", nil, &customerID},
	} {
		if err = db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role,admin_role_id,email_verified) VALUES($1,$1||'@example.test',$2,$1,$3,$4,true) RETURNING id`, account.name, string(hash), account.role, account.adminRole).Scan(account.id); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO ticket_departments(name,slug) VALUES('历史工单部门','historical-account-ticket') RETURNING id`).Scan(&departmentID); err != nil {
		t.Fatal(err)
	}
	var legacyTicket, customerTicket int64
	for _, ticket := range []struct {
		number string
		owner  int64
		id     *int64
	}{
		{"HISTORICAL-ADMIN-TICKET", ownerID, &legacyTicket},
		{"CUSTOMER-CONTROL-TICKET", customerID, &customerTicket},
	} {
		if err = db.Pool.QueryRow(t.Context(), `INSERT INTO tickets(ticket_no,user_id,department_id,subject,body) VALUES($1,$2,$3,$1,'升级前的公开正文') RETURNING id`, ticket.number, ticket.owner, departmentID).Scan(ticket.id); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	login := func(name, portal string) string {
		t.Helper()
		credentials := map[string]string{"username": name, "password": "historical-ticket-password", "portal": portal}
		if portal == "customer" {
			delete(credentials, "username")
			credentials["identifier"] = name + "@example.test"
		}
		w := request(h, http.MethodPost, "/api/v1/auth/login", credentials, "", "")
		expect(t, w, http.StatusOK)
		c := w.Result().Cookies()[0]
		return c.Name + "=" + c.Value
	}
	owner := login("historical-ticket-owner", "admin")
	staff := login("historical-ticket-handler", "admin")
	customer := login("historical-ticket-customer", "customer")

	// Both list APIs must retain the mixed historical owners and their names.
	list := request(h, http.MethodGet, "/api/v1/admin/tickets", nil, staff, "")
	expect(t, list, http.StatusOK)
	items := listData(t, list)
	if len(items) != 2 {
		t.Fatalf("historical tickets disappeared: %s", list.Body.String())
	}
	seen := map[int64]string{}
	for _, item := range items {
		seen[int64(item["id"].(float64))] = item["customer_name"].(string)
	}
	if seen[legacyTicket] != "historical-ticket-owner" || seen[customerTicket] != "historical-ticket-customer" {
		t.Fatalf("wrong historical owner identities: %#v", seen)
	}
	for _, query := range []string{"", "?q=historical-ticket-owner"} {
		page := request(h, http.MethodGet, "/api/v1/admin/tickets/page"+query, nil, staff, "")
		expect(t, page, http.StatusOK)
		want := float64(2)
		if query != "" {
			want = 1
		}
		if data(t, page)["total"] != want {
			t.Fatalf("historical page missing owner: %s", page.Body.String())
		}
	}
	legacyPath := fmt.Sprintf("/api/v1/tickets/%d", legacyTicket)
	detail := request(h, http.MethodGet, legacyPath, nil, owner, "")
	expect(t, detail, http.StatusOK)
	if data(t, detail)["admin_access"] != false {
		t.Fatal("historical ownership granted staff permissions")
	}
	expect(t, request(h, http.MethodGet, legacyPath, nil, customer, ""), http.StatusNotFound)
	// Resolving mixed owner profiles must not widen live route or department access.
	expect(t, request(h, http.MethodGet, "/api/v1/admin/tickets", nil, owner, ""), http.StatusForbidden)
	scopedID, scoped := user(t, db, h, "historical-ticket-scoped", "admin")
	contentRole(t, db, scopedID, "tickets.view", "tickets.manage")
	if _, err = db.Pool.Exec(t.Context(), `UPDATE administrators SET ticket_scope='departments',ticket_unassigned=false WHERE id=$1`, scopedID); err != nil {
		t.Fatal(err)
	}
	hidden := request(h, http.MethodGet, "/api/v1/admin/tickets", nil, scoped, "")
	expect(t, hidden, http.StatusOK)
	if len(listData(t, hidden)) != 0 {
		t.Fatal("historical owner bypassed department filter")
	}
	hiddenPage := request(h, http.MethodGet, "/api/v1/admin/tickets/page", nil, scoped, "")
	expect(t, hiddenPage, http.StatusOK)
	if data(t, hiddenPage)["total"] != float64(0) {
		t.Fatal("historical owner bypassed paginated department filter")
	}
	expect(t, request(h, http.MethodGet, legacyPath, nil, scoped, ""), http.StatusNotFound)
	expect(t, request(h, http.MethodPost, legacyPath+"/replies", map[string]any{"body": "无权回复", "version": 1}, scoped, ""), http.StatusNotFound)

	// A staff reply must commit the reply, event, and recipient decisions together.
	// Replaying it preserves the same reply instead of emitting another event.
	for _, ticket := range []struct {
		id, owner int64
		link      string
	}{
		{legacyTicket, ownerID, "/admin/tickets/"},
		{customerTicket, customerID, "/console/tickets/"},
	} {
		path := fmt.Sprintf("/api/v1/tickets/%d/replies", ticket.id)
		input := map[string]any{"body": "升级后的客服回复", "version": 1, "idempotency_key": fmt.Sprintf("historical-account-reply-%d", ticket.id)}
		first := request(h, http.MethodPost, path, input, staff, "")
		expect(t, first, http.StatusOK)
		again := request(h, http.MethodPost, path, input, staff, "")
		expect(t, again, http.StatusOK)
		reply := int64(data(t, first)["reply_id"].(float64))
		if data(t, again)["reply_id"] != float64(reply) {
			t.Fatal("reply replay duplicated the message")
		}
		var replies, events, decisions, version int64
		if err = db.Pool.QueryRow(t.Context(), `SELECT version,(SELECT count(*) FROM ticket_replies WHERE ticket_id=$1),(SELECT count(*) FROM ticket_notification_events WHERE ticket_id=$1),(SELECT count(*) FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.ticket_id=$1 AND e.reply_id=$2 AND d.user_id=$3) FROM tickets WHERE id=$1`, ticket.id, reply, ticket.owner).Scan(&version, &replies, &events, &decisions); err != nil {
			t.Fatal(err)
		}
		if version != 2 || replies != 1 || events != 1 || decisions != 2 {
			t.Fatalf("reply/event/recipient facts mismatch: version=%d replies=%d events=%d decisions=%d", version, replies, events, decisions)
		}
		var body string
		if err = db.Pool.QueryRow(t.Context(), `SELECT rendered_body FROM ticket_notification_deliveries WHERE user_id=$1 AND channel='email' AND event_id=(SELECT id FROM ticket_notification_events WHERE reply_id=$2)`, ticket.owner, reply).Scan(&body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body, fmt.Sprintf("http://localhost:3088%s%d", ticket.link, ticket.id)) {
			t.Fatalf("wrong recipient portal in notification: %q", body)
		}
	}
	// The historical owner's existing compatibility access and normal customer
	// ownership both remain valid for subsequent public replies.
	for _, ticket := range []struct {
		id     int64
		cookie string
	}{{legacyTicket, owner}, {customerTicket, customer}} {
		response := request(h, http.MethodPost, fmt.Sprintf("/api/v1/tickets/%d/replies", ticket.id), map[string]any{"body": "所有者回复", "version": 2}, ticket.cookie, "")
		expect(t, response, http.StatusOK)
	}

	// Showing a historical owner must not make it a selectable customer or allow
	// new customer tickets, carts, or orders to be created for that administrator.
	choices := request(h, http.MethodGet, "/api/v1/admin/tickets/create-options?kind=customers&q=historical-ticket-owner", nil, staff, "")
	expect(t, choices, http.StatusOK)
	if data(t, choices)["total"] != float64(0) {
		t.Fatal("historical administrator became a selectable customer")
	}
	expect(t, request(h, http.MethodPost, "/api/v1/admin/tickets", map[string]any{"customer_id": ownerID, "department_id": departmentID, "subject": "不允许的新客户工单", "body": "正文", "priority": "normal"}, staff, "historical-account-new-ticket"), http.StatusNotFound)
	for _, path := range []string{"/api/v1/tickets", "/api/v1/orders"} {
		expect(t, request(h, http.MethodPost, path, map[string]any{}, owner, "historical-account-no-commerce"), http.StatusUnauthorized)
	}
	expect(t, request(h, http.MethodGet, "/api/v1/cart", nil, owner, ""), http.StatusUnauthorized)
	if _, err = db.Pool.Exec(t.Context(), `INSERT INTO carts(user_id) VALUES($1)`, ownerID); err == nil {
		t.Fatal("historical administrator gained a new customer cart")
	}
	var customerCopies, carts, orders, tickets int64
	if err = db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM users WHERE id=$1),(SELECT count(*) FROM carts WHERE user_id=$1),(SELECT count(*) FROM orders WHERE user_id=$1),(SELECT count(*) FROM tickets WHERE user_id=$1)`, ownerID).Scan(&customerCopies, &carts, &orders, &tickets); err != nil {
		t.Fatal(err)
	}
	if customerCopies != 0 || carts != 0 || orders != 0 || tickets != 1 {
		t.Fatalf("historical account acquired customer records: copies=%d carts=%d orders=%d tickets=%d", customerCopies, carts, orders, tickets)
	}
}
