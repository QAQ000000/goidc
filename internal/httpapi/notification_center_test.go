package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/notification"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func centerBot(t *testing.T, h http.Handler, admin, id string) {
	t.Helper()
	expect(t, request(h, "POST", "/api/v1/admin/channel-instances", map[string]any{"id": id, "category": "bot", "implementation_id": "telegram_bot", "display_name": "本地机器人-" + id, "enabled": true, "config": map[string]any{"bot_username": "goidc_notice_bot"}, "secure_config": map[string]string{"bot_token": "123456:fixture_private_token", "webhook_secret": "fixture_webhook_secret"}}, admin, ""), 201)
}

func centerChallenge(t *testing.T, h http.Handler, cookie, id string) string {
	t.Helper()
	w := request(h, "POST", "/api/v1/me/notification-bindings/challenges", map[string]any{"instance_id": id}, cookie, "")
	expect(t, w, 201)
	result := data(t, w)
	if result["token"] != nil {
		t.Fatal("raw challenge escaped response")
	}
	u, err := url.Parse(result["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("start")
}

func centerWebhook(h http.Handler, id, secret, token, kind string, chat, from int64) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"update_id": 1, "message": map[string]any{"message_id": 1, "date": 1, "chat": map[string]any{"id": chat, "type": kind}, "from": map[string]any{"id": from, "is_bot": false, "first_name": "Fixture"}, "text": "/start " + token}})
	r := httptest.NewRequest("POST", "/api/v1/notifications/telegram/"+id+"/webhook", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func centerBind(t *testing.T, h http.Handler, cookie, id string, chat int64) {
	t.Helper()
	token := centerChallenge(t, h, cookie, id)
	expect(t, centerWebhook(h, id, "fixture_webhook_secret", token, "private", chat, chat), 200)
}

func TestNotificationCenterBindingPrivacyAndConsent(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "center-admin", "super_admin")
	uid, customer := user(t, db, h, "center-customer", "customer")
	_, other := user(t, db, h, "center-other", "customer")
	centerBot(t, h, admin, "center-bot")
	centerBot(t, h, admin, "other-bot")
	initial := request(h, "GET", "/api/v1/me/notification-preferences", nil, customer, "")
	expect(t, initial, 200)
	for _, raw := range data(t, initial)["channels"].([]any) {
		p := raw.(map[string]any)
		if p["enabled"] != false || p["version"] != float64(0) {
			t.Fatalf("unexpected initial preference %#v", p)
		}
	}
	expect(t, request(h, "PATCH", "/api/v1/me/notification-preferences", map[string]any{"channel": "telegram", "enabled": true, "expected_version": 0}, customer, ""), 409)
	token := centerChallenge(t, h, customer, "center-bot")
	deleting := request(h, "DELETE", "/api/v1/admin/channel-instances/center-bot", map[string]any{"expected_version": 1}, admin, "")
	expect(t, deleting, 409)
	if !strings.Contains(deleting.Body.String(), "channel_in_use") {
		t.Fatal("bound channel reference not identified", deleting.Body.String())
	}
	expect(t, centerWebhook(h, "center-bot", "forged", token, "private", 98543210, 98543210), 401)
	expect(t, centerWebhook(h, "other-bot", "fixture_webhook_secret", token, "private", 98543210, 98543210), 200)
	expect(t, centerWebhook(h, "center-bot", "fixture_webhook_secret", token, "group", 98543210, 98543210), 200)
	expect(t, centerWebhook(h, "center-bot", "fixture_webhook_secret", token, "private", 98543210, 98543211), 200)
	var count int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_targets`).Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid callback bound target", count, err)
	}
	expect(t, centerWebhook(h, "center-bot", "fixture_webhook_secret", token, "private", 98543210, 98543210), 200)
	expect(t, centerWebhook(h, "center-bot", "fixture_webhook_secret", token, "private", 98543210, 98543210), 200)
	state := request(h, "GET", "/api/v1/me/notification-preferences", nil, customer, "")
	expect(t, state, 200)
	if strings.Contains(state.Body.String(), "98543210") || strings.Contains(state.Body.String(), "fixture_private_token") || strings.Contains(state.Body.String(), token) {
		t.Fatal("notification state leaked private identity or secret")
	}
	targets := data(t, state)["targets"].([]any)
	if len(targets) != 1 {
		t.Fatal("binding/replay count", targets)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM user_notification_preferences WHERE user_id=$1 AND enabled`, uid).Scan(&count); err != nil || count != 0 {
		t.Fatal("binding auto-subscribed", count, err)
	}
	expect(t, request(h, "PATCH", "/api/v1/me/notification-preferences", map[string]any{"channel": "telegram", "enabled": true, "expected_version": 0}, customer, ""), 200)
	expect(t, request(h, "PATCH", "/api/v1/me/notification-preferences", map[string]any{"channel": "telegram", "enabled": false, "expected_version": 0}, customer, ""), 409)
	target := targets[0].(map[string]any)
	path := fmt.Sprintf("/api/v1/me/notification-bindings/%.0f", target["id"])
	expect(t, request(h, "DELETE", path, map[string]any{"expected_version": target["version"]}, other, ""), 409)
	expect(t, request(h, "DELETE", path, map[string]any{"expected_version": target["version"]}, customer, ""), 200)
	expect(t, request(h, "GET", "/api/v1/admin/notification-center", nil, customer, ""), 401)
	var details string
	if err := db.Pool.QueryRow(t.Context(), `SELECT coalesce(string_agg(payload::text,''),'') FROM audit_events`).Scan(&details); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(details, token) || strings.Contains(details, "98543210") || strings.Contains(details, "fixture_private_token") {
		t.Fatal("private binding values entered audit")
	}
}

func TestNotificationCenterBindingRequiresLiveOriginatingSession(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "center-session-admin", "super_admin")
	uid, cookie := user(t, db, h, "center-session-customer", "customer")
	centerBot(t, h, admin, "session-bot")
	token := centerChallenge(t, h, cookie, "session-bot")
	if _, err := db.Pool.Exec(t.Context(), `DELETE FROM sessions WHERE user_id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	expect(t, centerWebhook(h, "session-bot", "fixture_webhook_secret", token, "private", 12345001, 12345001), 200)
	var count int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_targets`).Scan(&count); err != nil || count != 0 {
		t.Fatal("revoked session bound target", count, err)
	}
}

func TestNotificationCenterWebhookRotationDuringBindingRollsBack(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "center-rotate-admin", "super_admin")
	uid, cookie := user(t, db, h, "center-rotate-customer", "customer")
	centerBot(t, h, admin, "rotate-bot")
	token := centerChallenge(t, h, cookie, "rotate-bot")
	blocker, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(t.Context())
	var pid int
	if err = blocker.QueryRow(t.Context(), `SELECT pg_backend_pid() FROM users WHERE id=$1 FOR UPDATE`, uid).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		finished <- centerWebhook(h, "rotate-bot", "fixture_webhook_secret", token, "private", 19456780, 19456780)
	}()
	// Wait until the callback has passed the initial secret check and is
	// blocked on this user's row, rather than relying on a scheduling delay.
	timer := time.NewTicker(10 * time.Millisecond)
	defer timer.Stop()
	deadline := time.After(5 * time.Second)
	for {
		var waiting bool
		if err = db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-timer.C:
		case <-deadline:
			t.Fatal("callback did not reach transactional user lock")
		}
	}
	expect(t, request(h, "PUT", "/api/v1/admin/channel-instances/rotate-bot", map[string]any{"display_name": "轮换机器人", "enabled": true, "config": map[string]any{"bot_username": "goidc_notice_bot"}, "secure_config": map[string]string{"webhook_secret": "rotated_secret"}, "expected_version": 1}, admin, ""), 200)
	if err = blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-finished:
		expect(t, result, 200)
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not complete")
	}
	var targets, consumed int
	if err = db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM notification_targets),(SELECT count(*) FROM notification_binding_challenges WHERE consumed_at IS NOT NULL)`).Scan(&targets, &consumed); err != nil || targets != 0 || consumed != 0 {
		t.Fatal("old webhook secret committed after rotation", targets, consumed, err)
	}
	expect(t, centerWebhook(h, "rotate-bot", "rotated_secret", token, "private", 19456780, 19456780), 200)
	if err = db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_targets`).Scan(&targets); err != nil || targets != 1 {
		t.Fatal("current secret could not consume retained challenge", targets, err)
	}
}

func centerInvoice(t *testing.T, db *store.Store, uid int64, number string) int64 {
	t.Helper()
	var id int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at) VALUES($1,$2,'paid',1250,now()) RETURNING id`, number, uid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := db.WithTx(t.Context(), func(tx pgx.Tx) error { return db.QueuePaymentSuccessNotifications(t.Context(), tx, id) }); err != nil {
		t.Fatal(err)
	}
	return id
}

type centerTransport func(*http.Request) (*http.Response, error)

func (f centerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNotificationCenterActualAdapterWorkerAndInbox(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "center-worker-admin", "super_admin")
	uid, customer := user(t, db, h, "center-worker-customer", "customer")
	_, other := user(t, db, h, "center-worker-other", "customer")
	centerBot(t, h, admin, "worker-bot")
	centerBind(t, h, customer, "worker-bot", 123456789)
	for _, ch := range []string{"telegram", "inbox"} {
		expect(t, request(h, "PATCH", "/api/v1/me/notification-preferences", map[string]any{"channel": ch, "enabled": true, "expected_version": 0}, customer, ""), 200)
		rule := map[string]any{"enabled": true, "template_id": "invoice-paid-" + ch, "expected_version": 1}
		if ch == "telegram" {
			rule["instance_id"] = "worker-bot"
		}
		expect(t, request(h, "PATCH", "/api/v1/admin/notification-center/rules/invoice-paid-"+ch, rule, admin, ""), 200)
	}
	invoice := centerInvoice(t, db, uid, "CENTER-ACCEPT")
	if err := db.WithTx(t.Context(), func(tx pgx.Tx) error { return db.QueuePaymentSuccessNotifications(t.Context(), tx, invoice) }); err != nil {
		t.Fatal(err)
	}
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	calls := 0
	mode := "accepted"
	http.DefaultTransport = centerTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.telegram.org" || !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Fatal("unexpected external destination")
		}
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Contains(raw, []byte("12.50")) || bytes.Contains(raw, []byte("parse_mode")) {
			t.Fatal("incorrect template/formatting", string(raw))
		}
		status, body := 200, `{"ok":true,"result":{"message_id":100,"chat":{"id":123456789,"type":"private"}}}`
		if mode == "limited" {
			status = 429
			body = `{"ok":false,"error_code":429,"parameters":{"retry_after":2}}`
		}
		if mode == "unknown" {
			body = `{"ok":true,"result":{}}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	if _, err := notification.DrainNotificationCenter(t.Context(), db, 10); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("duplicate external send", calls)
	}
	inbox := request(h, "GET", "/api/v1/me/notification-inbox", nil, customer, "")
	expect(t, inbox, 200)
	messages := data(t, inbox)["items"].([]any)
	if len(messages) != 1 {
		t.Fatal("inbox not delivered exactly once", messages)
	}
	messageID := messages[0].(map[string]any)["id"]
	readPath := fmt.Sprintf("/api/v1/me/notification-inbox/%.0f/read", messageID)
	expect(t, request(h, "POST", readPath, map[string]any{}, other, ""), 409)
	expect(t, request(h, "POST", readPath, map[string]any{}, customer, ""), 200)
	expect(t, request(h, "POST", readPath, map[string]any{}, customer, ""), 200)
	mode = "limited"
	limitedID := centerInvoice(t, db, uid, "CENTER-LIMITED")
	if _, err := notification.DrainNotificationCenter(t.Context(), db, 10); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.Pool.QueryRow(t.Context(), `SELECT d.status FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE e.invoice_id=$1 AND d.channel='telegram'`, limitedID).Scan(&status); err != nil || status != "pending" {
		t.Fatal("rate limit did not schedule retry", status, err)
	}
	mode = "unknown"
	unknownID := centerInvoice(t, db, uid, "CENTER-UNKNOWN")
	if _, err := notification.DrainNotificationCenter(t.Context(), db, 10); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT d.status FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE e.invoice_id=$1 AND d.channel='telegram'`, unknownID).Scan(&status); err != nil || status != "unknown" {
		t.Fatal("malformed success was not unknown", status, err)
	}
	before := calls
	if _, err := db.Pool.Exec(t.Context(), `UPDATE notification_deliveries SET next_attempt_at=clock_timestamp()+interval '1 hour' WHERE status='pending'`); err != nil {
		t.Fatal(err)
	}
	if _, err := notification.DrainNotificationCenter(t.Context(), db, 10); err != nil {
		t.Fatal(err)
	}
	if calls != before {
		t.Fatal("unknown result automatically resent")
	}
}

func TestNotificationCenterAdminScopeAndAuditRollback(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "center-scope-admin", "super_admin")
	staffID, staff := user(t, db, h, "center-scope-staff", "admin")
	uid, customer := user(t, db, h, "center-scope-customer", "customer")
	otherID, _ := user(t, db, h, "center-scope-other", "customer")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1;`, staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET sales_owner_id=$2 WHERE id=$1`, uid, staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PATCH", "/api/v1/admin/notification-center/rules/invoice-paid-inbox", map[string]any{"enabled": true, "template_id": "invoice-paid-inbox", "expected_version": 1}, admin, ""), 200)
	centerInvoice(t, db, uid, "SCOPE-OWN")
	centerInvoice(t, db, otherID, "SCOPE-OTHER")
	result := request(h, "GET", "/api/v1/admin/notification-center", nil, staff, "")
	expect(t, result, 200)
	rows := data(t, result)["deliveries"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["user_id"] != float64(uid) {
		t.Fatal("customer notification scope leaked", rows)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO admin_inbox_messages(user_id,subject,body) VALUES($1,'owned invoice','owned body'),($2,'private invoice','private body')`, uid, otherID); err != nil {
		t.Fatal(err)
	}
	legacy := request(h, "GET", "/api/v1/admin/logs/inbox", nil, staff, "")
	expect(t, legacy, 200)
	if data(t, legacy)["total"] != float64(1) {
		t.Fatal("legacy inbox log bypassed customer scope", legacy.Body.String())
	}
	search := request(h, "GET", "/api/v1/admin/logs/inbox?q=private", nil, staff, "")
	expect(t, search, 200)
	if data(t, search)["total"] != float64(0) {
		t.Fatal("legacy inbox search inferred private content")
	}
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_center_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='notification.preference.updated' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_center_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_center_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PATCH", "/api/v1/me/notification-preferences", map[string]any{"channel": "inbox", "enabled": true, "expected_version": 0}, customer, ""), 500)
	var count int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM user_notification_preferences WHERE user_id=$1`, uid).Scan(&count); err != nil || count != 0 {
		t.Fatal("audit failure did not rollback preference", count, err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY['notifications.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)`, staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PATCH", "/api/v1/admin/notification-center/templates/invoice-paid-inbox", map[string]any{"subject": "新模板", "body": "{{amount}}", "active": true, "expected_version": 1}, staff, ""), 403)
	expect(t, request(h, "POST", "/api/v1/admin/notification-center/channels/any/webhook", map[string]any{}, staff, ""), 403)
}

func TestNotificationCenterInvalidSecretRejectedAtSave(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "center-secret-admin", "super_admin")
	w := request(h, "POST", "/api/v1/admin/channel-instances", map[string]any{"id": "invalid-bot", "category": "bot", "implementation_id": "telegram_bot", "display_name": "Invalid", "enabled": true, "config": map[string]any{"bot_username": "goidc_notice_bot"}, "secure_config": map[string]string{"bot_token": "123:valid", "webhook_secret": "bad\r\nsecret"}}, admin, "")
	expect(t, w, 409)
	if strings.Contains(w.Body.String(), "bad") {
		t.Fatal("secret value leaked")
	}
}
