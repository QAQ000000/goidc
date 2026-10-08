package httpapi

import (
	"strconv"
	"testing"
)

func TestExpiredOutboxLeasesAtAttemptLimitBecomeDeadLetters(t *testing.T) {
	db, _ := testServer(t)
	var emailID, smsID int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind,max_attempts)
VALUES('lease-email@example.test','subject','body','system',2) RETURNING id`).Scan(&emailID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO sms_outbox(recipient,body_encrypted,kind,expires_at,max_attempts)
VALUES('+15550000001','payload','system',now()+interval '1 day',2) RETURNING id`).Scan(&smsID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE email_outbox SET status='sending',attempts=2,locked_at=now()-interval '6 minutes' WHERE id=$1`, emailID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE sms_outbox SET status='sending',attempts=2,locked_at=now()-interval '6 minutes' WHERE id=$1`, smsID); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverStaleEmailJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverStaleSMSJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, tableAndID := range []struct {
		table string
		id    int64
	}{{"email_outbox", emailID}, {"sms_outbox", smsID}} {
		var status string
		var deadLetterAt any
		if err := db.Pool.QueryRow(t.Context(), "SELECT status,dead_letter_at FROM "+tableAndID.table+" WHERE id=$1", tableAndID.id).Scan(&status, &deadLetterAt); err != nil {
			t.Fatal(err)
		}
		if status != "dead_letter" || deadLetterAt == nil {
			t.Fatalf("%s lease recovery status=%q dead_letter_at=%v", tableAndID.table, status, deadLetterAt)
		}
	}
}

func TestAdminCanListAndRetrySMSDeadLetter(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "sms-outbox-admin", "super_admin")
	var id int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO sms_outbox(recipient,body_encrypted,kind,expires_at,max_attempts,status,attempts,dead_letter_at,last_error)
VALUES('+15550000002','payload','system',now()+interval '1 day',3,'dead_letter',3,now(),'fixture failure') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	list := request(h, "GET", "/api/v1/admin/sms-outbox?status=dead_letter", nil, admin, "")
	expect(t, list, 200)
	items := listData(t, list)
	if len(items) != 1 || int64(items[0]["id"].(float64)) != id || int(items[0]["max_attempts"].(float64)) != 3 {
		t.Fatalf("unexpected SMS outbox list: %#v", items)
	}
	retry := request(h, "POST", "/api/v1/admin/sms-outbox/"+strconv.FormatInt(id, 10)+"/retry", map[string]any{}, admin, "")
	expect(t, retry, 200)
	var status string
	var attempts int
	if err := db.Pool.QueryRow(t.Context(), "SELECT status,attempts FROM sms_outbox WHERE id=$1", id).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 0 {
		t.Fatalf("SMS retry status=%s attempts=%d", status, attempts)
	}
}
