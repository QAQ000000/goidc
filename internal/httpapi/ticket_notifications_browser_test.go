package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

// TestTicketNotificationsBrowserFixture is opt-in and creates only isolated test
// data. Its delivery sink claims/completes jobs locally; it never connects to
// SMTP or SMS providers and cannot prove external receipt.
func TestTicketNotificationsBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" {
		t.Skip("opt-in browser fixture")
	}
	capture := os.Getenv("ZJMFGO_BROWSER_CAPTURE_FILE")
	if capture == "" {
		t.Fatal("capture file required")
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	defer func() { close(stop); wg.Wait() }()
	adminBrowserFixture(t, "ticket-notify-super", func(db *store.Store, h http.Handler) {
		customer, cookie := user(t, db, h, "ticket-notify-customer", "customer")
		staff, _ := user(t, db, h, "ticket-notify-staff", "admin")
		user(t, db, h, "ticket-notify-other", "customer")
		var department int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO ticket_departments(name,slug) VALUES('通知验收支持','ticket-notify') RETURNING id`).Scan(&department); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET email_verified=true,phone='+8613800000001' WHERE id=$1`, customer); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `UPDATE administrators SET email_verified=true,phone='+8613800000002' WHERE id=$1`, staff); err != nil {
			t.Fatal(err)
		}

		if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET phone_verified=true WHERE phone<>''; UPDATE administrators SET phone_verified=true WHERE phone<>''; INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled) VALUES('ticket-browser-sms','sms','generic_http_json','Local SMS',true); UPDATE channel_instances SET enabled=true WHERE category IN ('mail','sms'); UPDATE notification_templates SET active=true,provider_template_id=CASE WHEN channel='sms' THEN 'local-ticket-template' ELSE provider_template_id END WHERE kind IN ('ticket_customer_created','ticket_customer_replied','ticket_staff_replied')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO customer_contacts(customer_id,username,email,status,support_emails) VALUES($1,'订阅联系人','ticket-contact@example.test','active',true),($1,'退订联系人','ticket-unsubscribed@example.test','active',false)`, customer); err != nil {
			t.Fatal(err)
		}
		result := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "通知链路验收工单", "body": "这是客户的公开问题，不包含内部备注。", "priority": "normal", "department_id": department}, cookie, "ticket-browser-seed-key")
		expect(t, result, 201)
		ticket := int64(data(t, result)["id"].(float64))
		if _, err := db.Pool.Exec(t.Context(), `UPDATE tickets SET assignee_id=$2 WHERE id=$1`, ticket, staff); err != nil {
			t.Fatal(err)
		}
		manifest, _ := json.Marshal(map[string]any{"customer_id": customer, "staff_id": staff, "department_id": department, "ticket_id": ticket, "customer_email": "ticket-notify-customer@example.test", "staff_username": "ticket-notify-staff"})
		if err := os.WriteFile(os.Getenv("ZJMFGO_BROWSER_READY_FILE")+".fixture.json", manifest, 0600); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := os.OpenFile(capture, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				t.Error(err)
				return
			}
			defer f.Close()
			enc := json.NewEncoder(f)
			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
					if job, e := db.ClaimEmailJob(t.Context()); e == nil {
						if e = enc.Encode(map[string]any{"channel": "email", "outbox_id": job.ID, "recipient": job.Recipient, "subject": job.Subject, "body": job.Body, "kind": job.Kind, "instance_id": job.ChannelInstanceID}); e != nil {
							t.Error(e)
							return
						}
						if e = db.CompleteEmailJob(t.Context(), *job, nil); e != nil {
							t.Error(e)
							return
						}
					}
					if job, e := db.ClaimSMSJob(t.Context()); e == nil {
						if e = enc.Encode(map[string]any{"channel": "sms", "outbox_id": job.ID, "recipient": job.Recipient, "body": job.Body, "kind": job.Kind, "instance_id": job.ChannelInstanceID, "template_id": job.TemplateID}); e != nil {
							t.Error(e)
							return
						}
						if e = db.CompleteSMSJob(t.Context(), *job, nil); e != nil {
							t.Error(e)
							return
						}
					}
				}
			}
		}()
	})
}
