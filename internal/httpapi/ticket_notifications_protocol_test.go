package httpapi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/notification"
	"github.com/codex/zjmfgo/internal/store"
)

// This exercises the public reply -> queue -> compiled transport -> completion
// contract against loopback recipients. It proves no real external delivery.
func TestTicketNotificationLoopbackTransport(t *testing.T) {
	db, h := testServer(t)
	owner, customer := user(t, db, h, "ticket-protocol-customer", "customer")
	_, staff := user(t, db, h, "ticket-protocol-staff", "super_admin")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET email_verified=true,phone_verified=true,phone='+8613800000011' WHERE id=$1;`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET phone_verified=true WHERE id=$1`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO user_ticket_notification_preferences(user_id,sms_enabled) VALUES($1,true)`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled) VALUES('ticket-protocol-sms','sms','generic_http_json','Local SMS',true); UPDATE channel_instances SET enabled=true WHERE category IN ('mail','sms'); UPDATE notification_templates SET active=true,provider_template_id='local-approved' WHERE kind='ticket_staff_replied'`); err != nil {
		t.Fatal(err)
	}
	created := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "工单协议验收", "body": "客户问题", "priority": "normal"}, customer, "ticket-protocol-create")
	expect(t, created, 201)
	id := int64(data(t, created)["id"].(float64))
	replied := request(h, "POST", fmt.Sprintf("/api/v1/tickets/%d/replies", id), map[string]any{"body": "仅此公开答复", "version": 1}, staff, "ticket-protocol-reply")
	expect(t, replied, 200)
	email, err := db.ClaimEmailJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sms, err := db.ClaimSMSJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{email.Body, sms.Body} {
		if !strings.Contains(body, "仅此公开答复") || !strings.Contains(body, fmt.Sprintf("/console/tickets/%d", id)) || strings.Contains(body, "{{") {
			t.Fatal("public reply payload or URL missing")
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	messages := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			messages <- ""
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		_, _ = fmt.Fprint(conn, "220 local test\r\n")
		reader := bufio.NewReader(conn)
		var body strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				messages <- ""
				return
			}
			if strings.HasPrefix(line, "DATA") {
				_, _ = fmt.Fprint(conn, "354 send data\r\n")
				for {
					line, err = reader.ReadString('\n')
					if err != nil {
						messages <- ""
						return
					}
					if line == ".\r\n" {
						break
					}
					body.WriteString(line)
				}
				_, _ = fmt.Fprint(conn, "250 accepted\r\n")
			} else if strings.HasPrefix(line, "QUIT") {
				_, _ = fmt.Fprint(conn, "221 bye\r\n")
				messages <- body.String()
				return
			} else {
				_, _ = fmt.Fprint(conn, "250 ok\r\n")
			}
		}
	}()
	smtp := notification.SMTPConfig{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, From: "ticket-local@example.test", TLSMode: "none"}
	if err = smtp.Send(*email); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-messages:
		if !strings.Contains(raw, "To: "+email.Recipient) || !strings.Contains(raw, "仅此公开答复") {
			t.Fatal("SMTP payload mismatch")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("local SMTP timeout")
	}
	if err = db.CompleteEmailJob(t.Context(), *email, nil); err != nil {
		t.Fatal(err)
	}
	received := make(chan map[string]string, 1)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		if r.Header.Get("Authorization") != "Bearer local-ticket-token" || json.NewDecoder(r.Body).Decode(&payload) != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		received <- payload
		w.WriteHeader(http.StatusAccepted)
	}))
	defer gateway.Close()
	cfg := store.SMSRuntimeConfig{Provider: "generic_http_json", Endpoint: gateway.URL, Token: "local-ticket-token", Enabled: true}
	if err = notification.SendSMS(t.Context(), cfg, *sms); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-received:
		if payload["recipient"] != sms.Recipient || payload["message"] != sms.Body || payload["kind"] != "ticket_staff_replied" {
			t.Fatal("SMS payload mismatch")
		}
	case <-time.After(time.Second):
		t.Fatal("local SMS timeout")
	}
	if err = db.CompleteSMSJob(t.Context(), *sms, nil); err != nil {
		t.Fatal(err)
	}
	var delivered int
	if err = db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id LEFT JOIN email_outbox m ON m.id=d.email_outbox_id LEFT JOIN sms_outbox s ON s.id=d.sms_outbox_id WHERE e.ticket_id=$1 AND e.kind='staff_replied' AND coalesce(m.status,s.status)='sent'`, id).Scan(&delivered); err != nil || delivered != 2 {
		t.Fatalf("loopback delivered=%d err=%v", delivered, err)
	}
}
