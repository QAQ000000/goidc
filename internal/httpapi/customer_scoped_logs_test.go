package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type customerLogFixture struct {
	db                                                *store.Store
	h                                                 http.Handler
	staff, owner, other, allowed, denied              int64
	staffCookie, rootCookie, ownerCookie, otherCookie string
}

func newCustomerLogFixture(t *testing.T) customerLogFixture {
	t.Helper()
	db, h := testServer(t)
	// Every connection uses UTC, proving log day boundaries are selected by the
	// application rather than inherited from a PostgreSQL session's timezone.
	config := db.Pool.Config()
	config.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	db.Pool.Close()
	db.Pool = pool
	var timezone string
	if err = db.Pool.QueryRow(t.Context(), "SHOW timezone").Scan(&timezone); err != nil || timezone != "UTC" {
		t.Fatal("customer log fixture must use a UTC PostgreSQL session", err)
	}
	f := customerLogFixture{db: db, h: h}
	f.staff, f.staffCookie = user(t, db, h, "customer-log-reader", "admin")
	_, f.rootCookie = user(t, db, h, "customer-log-root", "super_admin")
	f.owner, f.ownerCookie = user(t, db, h, "customer-log-owner", "customer")
	f.other, f.otherCookie = user(t, db, h, "customer-log-other", "customer")
	contentRole(t, db, f.staff, "audit.view", "notifications.view")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false,sales_enabled=true,ticket_scope='departments',ticket_unassigned=false WHERE id=$1", f.staff); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", f.owner, f.staff); err != nil {
		t.Fatal(err)
	}
	for index, target := range []*int64{&f.allowed, &f.denied} {
		if err := db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES($1,$2) RETURNING id", fmt.Sprintf("客户日志部门%d", index), fmt.Sprintf("customer-log-%d", index)).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)", f.staff, f.allowed); err != nil {
		t.Fatal(err)
	}
	return f
}

func expectCustomerLogIDs(t *testing.T, result *httptest.ResponseRecorder, want ...int64) {
	t.Helper()
	expect(t, result, http.StatusOK)
	page := data(t, result)
	items := page["items"].([]any)
	want = append([]int64{}, want...)
	slices.SortFunc(want, func(a, b int64) int {
		if a > b {
			return -1
		}
		if a < b {
			return 1
		}
		return 0
	})
	if page["total"] != float64(len(want)) || len(items) != len(want) {
		t.Fatalf("customer log page count mismatch: total=%v items=%d want=%d", page["total"], len(items), len(want))
	}
	for index, item := range items {
		row := item.(map[string]any)
		if int64(row["id"].(float64)) != want[index] {
			t.Fatal("customer log page included the wrong business record")
		}
		if _, exists := row["payload"]; exists {
			t.Fatal("customer audit query exposed raw payload")
		}
	}
	if strings.Contains(result.Body.String(), "private-customer-log-body") {
		t.Fatal("customer log query exposed message or audit content")
	}
}

func TestCustomerScopedLogsValidateCustomerAndPermissions(t *testing.T) {
	f := newCustomerLogFixture(t)
	for _, path := range []string{"/api/v1/admin/logs", "/api/v1/admin/logs/email", "/api/v1/admin/logs/sms", "/api/v1/admin/logs/inbox"} {
		for _, value := range []string{"", "0", "-1", "+1", " 1", "1 ", "1x", "1.0", "9223372036854775808"} {
			result := request(f.h, "GET", path+"?customer_id="+url.QueryEscape(value), nil, f.staffCookie, "")
			expect(t, result, http.StatusBadRequest)
			if errorData(t, result)["code"] != "invalid_customer" {
				t.Fatal("invalid customer filter returned the wrong error")
			}
		}
		expect(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&customer_id=%d", path, f.owner, f.other), nil, f.staffCookie, ""), http.StatusBadRequest)
		var missingBody string
		for _, id := range []int64{f.other, f.staff, 9223372036854775807} {
			result := request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d", path, id), nil, f.staffCookie, "")
			expect(t, result, http.StatusNotFound)
			failure := errorData(t, result)
			body := fmt.Sprint(failure["code"], failure["message"])
			if missingBody != "" && missingBody != body {
				t.Fatal("missing and unauthorized customers were distinguishable")
			}
			missingBody = body
		}
		expect(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d", path, f.owner), nil, f.staffCookie, ""), http.StatusOK)
		for _, dates := range []string{
			"from=2026-02-30", "to=2026-2-01", "from=2026-10-02&to=2026-10-01",
			"from=2026-10-01&from=2026-10-01", "to=2026-10-01&to=2026-10-02",
			"from=&from=2026-10-01", "to=&to=",
		} {
			result := request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d&%s", path, f.owner, dates), nil, f.staffCookie, "")
			expect(t, result, http.StatusBadRequest)
			if errorData(t, result)["code"] != "invalid_log_date" {
				t.Fatal("invalid log date filter returned the wrong error")
			}
		}
	}
	t.Run("ShanghaiDayBoundariesInUTCSession", func(t *testing.T) {
		customerLogShanghaiBoundaries(t, f)
	})
	t.Run("CustomerScopeRevokedAfterPrecheck", func(t *testing.T) {
		customerLogScopeRevokedAfterPrecheck(t, f)
	})
	contentRole(t, f.db, f.staff, "customers.view")
	for _, path := range []string{"/api/v1/admin/logs", "/api/v1/admin/logs/email", "/api/v1/admin/logs/sms", "/api/v1/admin/logs/inbox"} {
		expect(t, request(f.h, "GET", fmt.Sprintf("%s?customer_id=%d", path, f.owner), nil, f.staffCookie, ""), http.StatusForbidden)
	}
}

func customerLogScopeRevokedAfterPrecheck(t *testing.T, f customerLogFixture) {
	t.Helper()
	filters := url.Values{"customer_id": {fmt.Sprint(f.owner)}, "q": {"customer-log-shanghai-boundary"}}
	for _, channel := range []string{"audit", "email", "sms", "inbox"} {
		t.Run(channel, func(t *testing.T) {
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", f.owner, f.staff); err != nil {
				t.Fatal(err)
			}
			read := func(db *store.Store) (map[string]any, error) {
				switch channel {
				case "audit":
					return db.AdminAuditLogPage(t.Context(), filters, f.staff, 25, 0)
				case "email":
					return db.AccountEmailLog(t.Context(), filters, f.staff, 25, 0)
				case "sms":
					return db.AccountSMSLog(t.Context(), filters, f.staff, 25, 0)
				default:
					return db.AccountInboxLog(t.Context(), filters, f.staff, 25, 0)
				}
			}
			before, err := read(f.db)
			if err != nil || fmt.Sprint(before["total"]) != "4" {
				t.Fatal("scope revocation fixture requires four initially visible records", before, err)
			}
			// The first Store query authorizes the customer. A one-connection pool
			// then waits for AfterRelease to revoke ownership before any following
			// count/data query can acquire that connection. The independent fixture
			// pool performs the transfer, without depending on the query's SQL text.
			config := f.db.Pool.Config()
			config.MaxConns = 1
			previousRelease := config.AfterRelease
			var once sync.Once
			revoked := make(chan error, 1)
			config.AfterRelease = func(connection *pgx.Conn) bool {
				once.Do(func() {
					_, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=NULL WHERE id=$1", f.owner)
					revoked <- err
				})
				return previousRelease == nil || previousRelease(connection)
			}
			pool, err := pgxpool.NewWithConfig(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			after, err := read(&store.Store{Pool: pool})
			if transferErr := <-revoked; transferErr != nil {
				t.Fatal(transferErr)
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(after)
			if err != nil || fmt.Sprint(after["total"]) != "0" || !strings.Contains(string(encoded), `"items":[]`) {
				t.Fatal("customer logs exposed data after customer scope was revoked", after, err)
			}
		})
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", f.owner, f.staff); err != nil {
		t.Fatal(err)
	}
}

func customerLogShanghaiBoundaries(t *testing.T, f customerLogFixture) {
	t.Helper()
	resources := customerLogResources(t, f)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	end := start.AddDate(0, 0, 1)
	instants := []time.Time{start.Add(-time.Microsecond), start, end.Add(-time.Microsecond), end}
	byChannel := map[string][]int64{}
	for index, instant := range instants {
		var audit, inbox int64
		if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,created_at)
 VALUES($1,'customer-log-shanghai-boundary','user',$2,$3) RETURNING id`, f.staff, fmt.Sprint(f.owner), instant).Scan(&audit); err != nil {
			t.Fatal(err)
		}
		if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO admin_inbox_messages(user_id,subject,body,created_at)
 VALUES($1,'customer-log-shanghai-boundary','private-customer-log-body',$2) RETURNING id`, f.owner, instant).Scan(&inbox); err != nil {
			t.Fatal(err)
		}
		if index == 1 || index == 2 {
			byChannel[""] = append(byChannel[""], audit)
			byChannel["/inbox"] = append(byChannel["/inbox"], inbox)
		}
		for _, channel := range []string{"email", "sms"} {
			var id int64
			var err error
			if channel == "email" {
				err = f.db.Pool.QueryRow(t.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind,created_at)
 VALUES('date-fixture@example.test','customer-log-shanghai-boundary','private-customer-log-body','customer-log-shanghai-boundary',$1) RETURNING id`, instant).Scan(&id)
			} else {
				err = f.db.Pool.QueryRow(t.Context(), `INSERT INTO sms_outbox(recipient,body_encrypted,kind,expires_at,created_at)
 VALUES('+8613800138000','private-customer-log-body'::bytea,'customer-log-shanghai-boundary',now()+interval '1 day',$1) RETURNING id`, instant).Scan(&id)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Pool.Exec(t.Context(), `INSERT INTO notification_delivery_events(service_id,user_id,channel,kind,stage,due_date,`+channel+`_outbox_id)
 VALUES($1,$2,$3,'service_expiry_reminder',1,$4,$5)`, resources[0].service, f.owner, channel, start.AddDate(0, 0, index).Format("2006-01-02"), id); err != nil {
				t.Fatal(err)
			}
			if index == 1 || index == 2 {
				byChannel["/"+channel] = append(byChannel["/"+channel], id)
			}
		}
	}
	for _, channel := range []string{"", "/email", "/sms", "/inbox"} {
		path := fmt.Sprintf("/api/v1/admin/logs%s?customer_id=%d&q=customer-log-shanghai-boundary&from=2026-10-01&to=2026-10-01", channel, f.owner)
		expectCustomerLogIDs(t, request(f.h, "GET", path, nil, f.staffCookie, ""), byChannel[channel]...)
		// Global queries retain the same date semantics and pagination contract.
		path = fmt.Sprintf("/api/v1/admin/logs%s?q=customer-log-shanghai-boundary&from=2026-10-01&to=2026-10-01", channel)
		expectCustomerLogIDs(t, request(f.h, "GET", path, nil, f.rootCookie, ""), byChannel[channel]...)
	}
}

func customerLogResources(t *testing.T, f customerLogFixture) [2]struct{ order, invoice, service int64 } {
	t.Helper()
	var out [2]struct{ order, invoice, service int64 }
	pid := product(t, f.db, 10)
	for index, cookie := range []string{f.ownerCookie, f.otherCookie} {
		version, item := add(t, f.h, cookie, pid, 1)
		created := request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, cookie, fmt.Sprintf("customer-log-order-%d", index))
		expect(t, created, http.StatusCreated)
		out[index].order = int64(data(t, created)["order_id"].(float64))
		out[index].invoice = int64(data(t, created)["invoice_id"].(float64))
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1", out[index].order).Scan(&out[index].service); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func customerLogTicket(t *testing.T, f customerLogFixture, department int64) int64 {
	t.Helper()
	var id int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO tickets(ticket_no,user_id,subject,body,department_id)
 VALUES('LOG-'||gen_random_uuid()::text,$1,'customer-log-fixture','private-customer-log-body',$2) RETURNING id`, f.owner, department).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCustomerAuditLogsFollowBusinessOwnershipAndTicketScope(t *testing.T) {
	f := newCustomerLogFixture(t)
	resources := customerLogResources(t, f)
	write := func(actor any, action, entity string, id any, payload any) int64 {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		var audit int64
		if err = f.db.Pool.QueryRow(t.Context(), `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload) VALUES($1,$2,$3,$4,$5) RETURNING id`, actor, "customer-log-fixture."+action, entity, fmt.Sprint(id), raw).Scan(&audit); err != nil {
			t.Fatal(err)
		}
		return audit
	}
	base := []int64{
		write(f.staff, "customer_updated", "user", f.owner, map[string]any{"secret": "private-customer-log-body"}),
		write(f.staff, "order_updated", "order", resources[0].order, map[string]any{}),
		write(nil, "worker_service_updated", "service", resources[0].service, map[string]any{}),
		write(f.other, "invoice_updated", "invoice", resources[0].invoice, map[string]any{}),
		write(f.staff, "deleted_contact", "customer_contact", 900001, map[string]any{"customer_id": f.owner}),
		write(f.staff, "deleted_file", "customer_file", 900002, map[string]any{"customer_id": f.owner}),
	}
	write(f.owner, "foreign_order", "order", resources[1].order, map[string]any{})
	write(f.owner, "global_operation", "global_configuration", 1, map[string]any{"customer_id": f.owner})
	write(f.owner, "malformed_entity", "service", "historical-nonnumeric-id", map[string]any{})
	allowedTicket, deniedTicket := customerLogTicket(t, f, f.allowed), customerLogTicket(t, f, f.denied)
	visibleTicket := write(f.staff, "allowed_ticket", "ticket", allowedTicket, map[string]any{})
	write(f.staff, "denied_department_ticket", "ticket", deniedTicket, map[string]any{})
	var note int64
	if err := f.db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_notes(ticket_id,author_id,body) VALUES($1,$2,'private-customer-log-body') RETURNING id", allowedTicket, f.staff).Scan(&note); err != nil {
		t.Fatal(err)
	}
	visibleNote := write(f.staff, "ticket_note", "ticket_note", note, map[string]any{})
	// A real deletion audit retains the immutable ticket relation after its row is gone.
	var deletedAttachment int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload)
 VALUES($1,'ticket.attachment.deleted','ticket_attachment','deleted-attachment',jsonb_build_object('ticket_id',$2::bigint)) RETURNING id`, f.staff, allowedTicket).Scan(&deletedAttachment); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/logs?customer_id=%d&q=customer-log-fixture", f.owner)
	expectCustomerLogIDs(t, request(f.h, "GET", path, nil, f.staffCookie, ""), base...)
	contentRole(t, f.db, f.staff, "audit.view", "notifications.view", "tickets.view")
	expectCustomerLogIDs(t, request(f.h, "GET", path, nil, f.staffCookie, ""), append(append([]int64{}, base...), visibleTicket, visibleNote)...)
	deletedPath := fmt.Sprintf("/api/v1/admin/logs?customer_id=%d&q=ticket.attachment.deleted", f.owner)
	expectCustomerLogIDs(t, request(f.h, "GET", deletedPath, nil, f.staffCookie, ""), deletedAttachment)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE tickets SET department_id=$2 WHERE id=$1", allowedTicket, f.denied); err != nil {
		t.Fatal(err)
	}
	expectCustomerLogIDs(t, request(f.h, "GET", path, nil, f.staffCookie, ""), base...)
	expectCustomerLogIDs(t, request(f.h, "GET", deletedPath, nil, f.staffCookie, ""))
	paged := request(f.h, "GET", path+"&limit=2&offset=1", nil, f.staffCookie, "")
	expect(t, paged, http.StatusOK)
	page := data(t, paged)
	if page["total"] != float64(len(base)) || len(page["items"].([]any)) != 2 || page["limit"] != float64(2) || page["offset"] != float64(1) {
		t.Fatal("customer audit pagination lost its filtered total")
	}
	global := request(f.h, "GET", "/api/v1/admin/logs?q=customer-log-fixture", nil, f.rootCookie, "")
	expect(t, global, http.StatusOK)
	if data(t, global)["total"] != float64(12) {
		t.Fatal("unfiltered audit query lost its existing global contract")
	}
}

func TestCustomerNotificationLogsUseImmutableRelationsAndTicketScope(t *testing.T) {
	f := newCustomerLogFixture(t)
	resources := customerLogResources(t, f)
	newOutbox := func(channel string, ticket any) int64 {
		t.Helper()
		var id int64
		var err error
		if channel == "email" {
			err = f.db.Pool.QueryRow(t.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind,ticket_id)
 VALUES('customer-log-owner@example.test','customer-log-fixture','private-customer-log-body','customer-log-fixture',$1) RETURNING id`, ticket).Scan(&id)
		} else {
			err = f.db.Pool.QueryRow(t.Context(), `INSERT INTO sms_outbox(recipient,body_encrypted,kind,expires_at)
 VALUES('+8613800138000','private-customer-log-body'::bytea,'customer-log-fixture',now()+interval '1 day') RETURNING id`).Scan(&id)
		}
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	owned := map[string]int64{}
	for _, channel := range []string{"email", "sms"} {
		for index, customer := range []int64{f.owner, f.other} {
			id := newOutbox(channel, nil)
			if index == 0 {
				owned[channel] = id
			}
			if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO notification_delivery_events(service_id,user_id,channel,kind,stage,due_date,`+channel+`_outbox_id)
 VALUES($1,$2,$3,'service_provision_success',0,current_date,$4)`, resources[index].service, customer, channel, id); err != nil {
				t.Fatal(err)
			}
		}
		// Same-address unlinked and foreign messages must never be guessed as owned.
		newOutbox(channel, nil)
	}
	allowedTicket, deniedTicket := customerLogTicket(t, f, f.allowed), customerLogTicket(t, f, f.denied)
	allowedOutboxes := map[string]int64{}
	for index, ticket := range []int64{allowedTicket, deniedTicket} {
		var event int64
		if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO ticket_notification_events(event_key,ticket_id,actor_id,kind)
 VALUES('customer-log-'||gen_random_uuid()::text,$1,$2,'staff_replied') RETURNING id`, ticket, f.staff).Scan(&event); err != nil {
			t.Fatal(err)
		}
		for _, channel := range []string{"email", "sms"} {
			id := newOutbox(channel, nil)
			if index == 0 {
				allowedOutboxes[channel] = id
			}
			// A CC/contact address does not need to match the customer's current address.
			if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO ticket_notification_deliveries
 (event_id,recipient_kind,recipient_id,customer_id,channel,address,normalized_address,preference_enabled,address_verified,template_kind,outcome,`+channel+`_outbox_id)
 VALUES($1,'cc',1,$2,$3,'shared-contact@example.test','shared-contact@example.test',true,true,'ticket_staff_replied','queued',$4)`, event, f.owner, channel, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	legacyEmail := newOutbox("email", allowedTicket)
	var ownedInbox int64
	for index, customer := range []int64{f.owner, f.other} {
		var id int64
		if err := f.db.Pool.QueryRow(t.Context(), "INSERT INTO admin_inbox_messages(user_id,subject,body) VALUES($1,'customer-log-fixture','private-customer-log-body') RETURNING id", customer).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			ownedInbox = id
		}
	}
	path := func(channel string) string {
		return fmt.Sprintf("/api/v1/admin/logs/%s?customer_id=%d&q=customer-log-fixture", channel, f.owner)
	}
	for _, channel := range []string{"email", "sms"} {
		expectCustomerLogIDs(t, request(f.h, "GET", path(channel), nil, f.staffCookie, ""), owned[channel])
	}
	expectCustomerLogIDs(t, request(f.h, "GET", path("inbox"), nil, f.staffCookie, ""), ownedInbox)
	contentRole(t, f.db, f.staff, "audit.view", "notifications.view", "tickets.view")
	expectCustomerLogIDs(t, request(f.h, "GET", path("email"), nil, f.staffCookie, ""), owned["email"], allowedOutboxes["email"], legacyEmail)
	expectCustomerLogIDs(t, request(f.h, "GET", path("sms"), nil, f.staffCookie, ""), owned["sms"], allowedOutboxes["sms"])
	if _, err := f.db.Pool.Exec(t.Context(), "DELETE FROM admin_ticket_departments WHERE user_id=$1", f.staff); err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{"email", "sms"} {
		expectCustomerLogIDs(t, request(f.h, "GET", path(channel), nil, f.staffCookie, ""), owned[channel])
	}
	for channel, want := range map[string]float64{"email": 6, "sms": 5, "inbox": 2} {
		global := request(f.h, "GET", "/api/v1/admin/logs/"+channel+"?q=customer-log-fixture", nil, f.rootCookie, "")
		expect(t, global, http.StatusOK)
		if data(t, global)["total"] != want {
			t.Fatal("unfiltered notification logs lost their global contract", channel)
		}
	}
}
