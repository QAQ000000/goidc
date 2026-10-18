package httpapi

import (
	"fmt"
	"sync"
	"testing"
)

func TestTicketRatingOwnershipOnceReplayAndHistoricalIdentity(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "rating-root", "super_admin")
	customerID, customer := user(t, db, h, "rating-owner", "customer")
	_, other := user(t, db, h, "rating-other", "customer")
	_, reporter := user(t, db, h, "rating-reporter", "admin")
	var dept int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO ticket_departments(name,slug) VALUES('Rating','rating') RETURNING id`).Scan(&dept); err != nil {
		t.Fatal(err)
	}
	created := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "Rate staff response", "body": "question", "priority": "normal", "department_id": dept}, customer, "")
	expect(t, created, 201)
	tid := int64(data(t, created)["id"].(float64))
	replied := request(h, "POST", fmt.Sprintf("/api/v1/tickets/%d/replies", tid), map[string]any{"body": "staff response", "version": 1}, root, "")
	expect(t, replied, 200)
	replyID := int64(data(t, replied)["reply_id"].(float64))
	adminCreated := request(h, "POST", "/api/v1/admin/tickets", map[string]any{"customer_id": customerID, "department_id": dept, "subject": "Initial staff content", "body": "staff initial", "priority": "normal", "send_email": false}, root, "rating-admin-initial")
	expect(t, adminCreated, 201)
	initialID := int64(data(t, adminCreated)["id"].(float64))
	// Changing the author's current role must not rewrite the writing-time identity.
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET role='customer' WHERE id=$1`, rootID); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/tickets/%d/ratings", tid)
	body := map[string]any{"target": "reply", "reply_id": replyID, "stars": 5}
	expect(t, request(h, "POST", path, body, other, "rating-wrong-owner"), 404)
	expect(t, request(h, "POST", path, map[string]any{"target": "reply", "reply_id": replyID, "stars": 0}, customer, "rating-zero-stars"), 400)
	first := request(h, "POST", path, body, customer, "rating-stable-key")
	expect(t, first, 201)
	replay := request(h, "POST", path, body, customer, "rating-stable-key")
	expect(t, replay, 200)
	if data(t, first)["id"] != data(t, replay)["id"] {
		t.Fatal("replay changed rating")
	}
	expect(t, request(h, "POST", path, body, customer, "rating-another-key"), 409)
	expect(t, request(h, "POST", path, map[string]any{"target": "reply", "reply_id": replyID, "stars": 3}, customer, "rating-stable-key"), 409)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/tickets/%d/ratings", initialID), map[string]any{"target": "initial", "stars": 4}, customer, "rating-initial-key"), 201)
	expect(t, request(h, "POST", path, map[string]any{"target": "initial", "stars": 4}, customer, "rating-customer-initial"), 404)
	detail := request(h, "GET", fmt.Sprintf("/api/v1/tickets/%d", tid), nil, customer, "")
	expect(t, detail, 200)
	reply := data(t, detail)["replies"].([]any)[0].(map[string]any)
	rating := reply["rating"].(map[string]any)
	if rating["stars"] != float64(5) || rating["can_rate"] != false || reply["author_role"] != "admin" {
		t.Fatalf("lost historical identity/rating: %#v", reply)
	}
	var count, audits int
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ticket_ratings),(SELECT count(*) FROM audit_events WHERE action='ticket.rated')`).Scan(&count, &audits); err != nil || count != 2 || audits != 2 {
		t.Fatalf("duplicate records count=%d audits=%d err=%v", count, audits, err)
	}
	// The current department still protects both reads and idempotent replays.
	if _, err := db.Pool.Exec(t.Context(), `UPDATE ticket_departments SET customer_visible=false WHERE id=$1`, dept); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", path, body, customer, "rating-stable-key"), 404)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/reports/tickets/handlers?year=%d", 2024), nil, reporter, ""), 200)
}

func TestTicketRatingConcurrentOnceAndAuditRollback(t *testing.T) {
	db, h := testServer(t)
	staff, _ := user(t, db, h, "rating-race-staff", "admin")
	owner, customer := user(t, db, h, "rating-race-owner", "customer")
	var tid, rid int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO tickets(ticket_no,user_id,subject,body) VALUES('rating-race',$1,'test','test') RETURNING id`, owner).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO ticket_replies(ticket_id,author_id,body,author_kind) VALUES($1,$2,'staff','staff') RETURNING id`, tid, staff).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/tickets/%d/ratings", tid)
	body := map[string]any{"target": "reply", "reply_id": rid, "stars": 2}
	codes := make(chan int, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, key := range []string{"rating-race-key-one", "rating-race-key-two"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			<-start
			codes <- request(h, "POST", path, body, customer, key).Code
		}(key)
	}
	close(start)
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[201] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent scores: %v", counts)
	}
	var second int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO ticket_replies(ticket_id,author_id,body,author_kind) VALUES($1,$2,'rollback','staff') RETURNING id`, tid, staff).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_rating_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='ticket.rated' THEN RAISE EXCEPTION 'rating audit unavailable'; END IF; RETURN NEW; END$$;CREATE TRIGGER reject_rating_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_rating_audit()`); err != nil {
		t.Fatal(err)
	}
	body = map[string]any{"target": "reply", "reply_id": second, "stars": 3}
	expect(t, request(h, "POST", path, body, customer, "rating-audit-retry"), 500)
	var saved int
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ticket_ratings WHERE reply_id=$1)+(SELECT count(*) FROM mutation_operations WHERE scope='ticket-rating' AND idempotency_key='rating-audit-retry')`, second).Scan(&saved); err != nil || saved != 0 {
		t.Fatalf("audit failure left state: %d %v", saved, err)
	}
	if _, err := db.Pool.Exec(t.Context(), `DROP TRIGGER reject_rating_audit ON audit_events`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", path, body, customer, "rating-audit-retry"), 201)
}

func TestTicketHandlerReportPeriodAssignmentStarsScopeAndPagination(t *testing.T) {
	db, h := testServer(t)
	viewerID, viewer := user(t, db, h, "handler-viewer", "admin")
	a, _ := user(t, db, h, "handler-a", "admin")
	b, _ := user(t, db, h, "handler-b", "admin")
	owner, _ := user(t, db, h, "handler-owner", "customer")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var dept, hidden int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO ticket_departments(name,slug) VALUES('Visible','handler-visible') RETURNING id`).Scan(&dept); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO ticket_departments(name,slug) VALUES('Hidden','handler-hidden') RETURNING id`).Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE users SET ticket_scope='departments' WHERE id=$1`, viewerID)
	exec(`INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)`, viewerID, dept)
	ticket := func(no string, department, assignee int64) int64 {
		t.Helper()
		var id int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO tickets(ticket_no,user_id,subject,body,department_id,assignee_id,created_at) VALUES($1,$2,'test','test',$3,$4,'2024-02-01 08:00+08') RETURNING id`, no, owner, department, assignee).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	target := ticket("handler-target", dept, a)
	source := ticket("handler-source", dept, b)
	secret := ticket("handler-secret", hidden, a)
	exec(`UPDATE tickets SET merged_ticket_id=$2,status='closed' WHERE id=$1`, source, target)
	exec(`UPDATE tickets SET created_by=$2,initial_author_kind='staff' WHERE id=$1`, target, b)
	reply := func(tid, author int64, date, kind string, stars int) {
		t.Helper()
		var rid int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO ticket_replies(ticket_id,author_id,body,author_kind,created_at) VALUES($1,$2,'test',$3,$4::timestamptz) RETURNING id`, tid, author, kind, date).Scan(&rid); err != nil {
			t.Fatal(err)
		}
		if stars > 0 {
			exec(`INSERT INTO ticket_ratings(reply_id,customer_id,staff_id,stars,rated_at) VALUES($1,$2,$3,$4,'2025-01-01')`, rid, owner, author, stars)
		}
	}
	reply(target, a, "2024-02-01 08:00+08", "staff", 5)
	reply(source, b, "2024-02-02 08:00+08", "staff", 2)
	reply(source, b, "2024-02-03 08:00+08", "staff", 1)
	reply(source, b, "2024-02-04 08:00+08", "staff", 4)
	reply(target, b, "2024-01-31 15:00+00", "staff", 1)
	reply(secret, a, "2024-02-01 08:00+08", "staff", 1)
	reply(target, a, "2024-02-01 08:00+08", "unknown", 0)
	exec(`INSERT INTO ticket_ratings(initial_ticket_id,customer_id,staff_id,stars,rated_at) VALUES($1,$2,$3,3,'2025-01-01')`, target, owner, b)
	path := "/api/v1/admin/reports/tickets/handlers?year=2024&month=2&sort=handler_id&direction=asc"
	w := request(h, "GET", path, nil, viewer, "")
	expect(t, w, 200)
	page := data(t, w)
	items := page["items"].([]any)
	if page["total"] != float64(2) || len(items) != 2 {
		t.Fatalf("handler page: %s", w.Body.String())
	}
	rowA, rowB := items[0].(map[string]any), items[1].(map[string]any)
	if rowA["handler_id"] != float64(a) || rowA["ticket_count"] != float64(1) || rowA["total_score"] != float64(5) || rowA["reply_count"] != float64(1) || rowB["ticket_count"] != float64(1) || rowB["open_count"] != float64(0) || rowB["closed_count"] != float64(1) || rowB["total_score"] != float64(10) || rowB["rated_reply_count"] != float64(4) || rowB["one_star_count"] != float64(1) || rowB["two_star_count"] != float64(1) || rowB["three_star_count"] != float64(1) || rowB["four_star_count"] != float64(1) || rowA["five_star_count"] != float64(1) {
		t.Fatalf("wrong attribution/stars: %#v %#v", rowA, rowB)
	}
	filtered := request(h, "GET", path+fmt.Sprintf("&assignee_id=%d&limit=1&offset=1", b), nil, viewer, "")
	expect(t, filtered, 200)
	if data(t, filtered)["total"] != float64(1) || len(data(t, filtered)["items"].([]any)) != 0 {
		t.Fatal("pagination lost total")
	}
	if len(data(t, filtered)["handlers"].([]any)) != 2 {
		t.Fatal("handler choices were narrowed to the current page/filter")
	}
	date := request(h, "GET", path+"&from=2024-02-02&to=2024-02-02", nil, viewer, "")
	expect(t, date, 200)
	if data(t, date)["items"].([]any)[0].(map[string]any)["total_score"] != float64(2) {
		t.Fatal("rating counted rated_at instead of reply creation")
	}
	removedExport := request(h, "GET", path+"&format=csv", nil, viewer, "")
	expect(t, removedExport, 400)
	if errorData(t, removedExport)["code"] != "invalid_report_format" || removedExport.Header().Get("Content-Disposition") != "" {
		t.Fatalf("removed export returned a download: %s", removedExport.Body.String())
	}
	exec(`UPDATE tickets SET assignee_id=$2 WHERE id=$1`, target, b)
	w = request(h, "GET", path, nil, viewer, "")
	expect(t, w, 200)
	if data(t, w)["items"].([]any)[0].(map[string]any)["ticket_count"] != float64(0) {
		t.Fatal("tickets not attributed to current assignee")
	}
	expect(t, request(h, "GET", "/api/v1/admin/reports/tickets/handlers?year=2024&month=2&sort=handler_id%3BDROP", nil, viewer, ""), 400)
	exec(`UPDATE admin_roles SET permissions=array_remove(permissions,'tickets.view') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)`, viewerID)
	expect(t, request(h, "GET", path, nil, viewer, ""), 403)
}
