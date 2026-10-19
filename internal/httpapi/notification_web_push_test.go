package httpapi

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/notify"
)

func webPushKeys(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(key.Bytes())
}

func webPushInstance(t *testing.T, h http.Handler, admin, id string) (string, string) {
	t.Helper()
	public, private := webPushKeys(t)
	w := request(h, "POST", "/api/v1/admin/channel-instances", map[string]any{
		"id": id, "category": "push", "implementation_id": "web_push", "display_name": "浏览器通知", "enabled": true,
		"config":        map[string]any{"public_key": public, "subject": "mailto:admin@example.test"},
		"secure_config": map[string]string{"private_key": private},
	}, admin, "")
	expect(t, w, 201)
	if strings.Contains(w.Body.String(), private) {
		t.Fatal("VAPID private key escaped instance response")
	}
	return public, private
}

func webPushInput(t *testing.T, instance, suffix string, version int64) map[string]any {
	t.Helper()
	public, _ := webPushKeys(t)
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"instance_id": instance, "expected_version": version,
		"subscription": map[string]any{"endpoint": "https://fcm.googleapis.com/fcm/send/" + suffix, "keys": map[string]string{"p256dh": public, "auth": base64.RawURLEncoding.EncodeToString(auth)}},
	}
}

func TestWebPushAPIConsentIsolationAndPrivacy(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "webpush-api-root", "super_admin")
	uid, customer := user(t, db, h, "webpush-api-owner", "customer")
	_, other := user(t, db, h, "webpush-api-other", "customer")
	public, private := webPushInstance(t, h, admin, "browser-push")
	statePath := "/api/v1/me/notification-web-push"
	path := statePath + "/subscriptions"
	expect(t, request(h, "GET", statePath, nil, "", ""), 401)
	state := request(h, "GET", statePath, nil, customer, "")
	expect(t, state, 200)
	instances := data(t, state)["instances"].([]any)
	if len(instances) != 1 || instances[0].(map[string]any)["public_key"] != public || len(data(t, state)["subscriptions"].([]any)) != 0 {
		t.Fatal("invalid initial push state", state.Body.String())
	}
	pref := map[string]any{"channel": "web_push", "enabled": true, "event_scope": "business", "expected_version": 0}
	expect(t, request(h, "PATCH", "/api/v1/me/notification-preferences", pref, customer, ""), 409)
	in := webPushInput(t, "browser-push", "private-browser-capability", 0)
	created := request(h, "POST", path, in, customer, "")
	expect(t, created, 200)
	sub := data(t, created)
	deletePath := fmt.Sprintf("%s/%.0f", path, sub["id"])
	state = request(h, "GET", statePath, nil, customer, "")
	expect(t, state, 200)
	if len(data(t, state)["subscriptions"].([]any)) != 1 {
		t.Fatal("missing subscription")
	}
	if data(t, state)["subscriptions"].([]any)[0].(map[string]any)["current_session"] != true {
		t.Fatal("originating browser session not identified")
	}
	otherState := request(h, "GET", statePath, nil, other, "")
	expect(t, otherState, 200)
	if len(data(t, otherState)["subscriptions"].([]any)) != 0 {
		t.Fatal("other account inferred browser subscription")
	}
	expect(t, request(h, "POST", path, in, other, ""), 409)
	expect(t, request(h, "POST", path, in, customer, ""), 409)
	var count int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM user_notification_preferences WHERE user_id=$1 AND enabled`, uid).Scan(&count); err != nil || count != 0 {
		t.Fatal("browser binding implicitly enabled consent", count, err)
	}
	expect(t, request(h, "PATCH", "/api/v1/me/notification-preferences", pref, customer, ""), 200)
	center := request(h, "GET", "/api/v1/admin/notification-center", nil, admin, "")
	expect(t, center, 200)
	if !strings.Contains(center.Body.String(), "web_push") {
		t.Fatal("push channel missing in administrator notification center")
	}
	var audit string
	if err := db.Pool.QueryRow(t.Context(), `SELECT coalesce(string_agg(payload::text,''),'') FROM audit_events`).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	for _, response := range []string{created.Body.String(), state.Body.String(), center.Body.String(), audit} {
		for _, secret := range []string{private, "private-browser-capability", in["subscription"].(map[string]any)["keys"].(map[string]string)["auth"]} {
			if strings.Contains(response, secret) {
				t.Fatal("private push capability escaped public state/audit")
			}
		}
	}
	expect(t, request(h, "DELETE", deletePath, map[string]any{"expected_version": sub["version"]}, other, ""), 409)
	expect(t, request(h, "DELETE", deletePath, map[string]any{"expected_version": 0}, customer, ""), 400)
	expect(t, request(h, "DELETE", deletePath, map[string]any{"expected_version": sub["version"]}, customer, ""), 200)
}

func TestWebPushAPIAuditRollbackAndRevokedSession(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "webpush-rollback-root", "super_admin")
	uid, customer := user(t, db, h, "webpush-rollback-owner", "customer")
	webPushInstance(t, h, admin, "rollback-push")
	path := "/api/v1/me/notification-web-push/subscriptions"
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_push_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('notification.web_push.subscribed','notification.web_push.unsubscribed') THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_push_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_push_audit()`); err != nil {
		t.Fatal(err)
	}
	in := webPushInput(t, "rollback-push", "rollback-capability", 0)
	expect(t, request(h, "POST", path, in, customer, ""), 500)
	var count int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_web_push_subscriptions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("audit failure committed browser subscription", count, err)
	}
	if _, err := db.Pool.Exec(t.Context(), `DROP TRIGGER reject_push_audit ON audit_events`); err != nil {
		t.Fatal(err)
	}
	created := request(h, "POST", path, in, customer, "")
	expect(t, created, 200)
	sub := data(t, created)
	if _, err := db.Pool.Exec(t.Context(), `CREATE TRIGGER reject_push_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_push_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "DELETE", fmt.Sprintf("%s/%.0f", path, sub["id"]), map[string]any{"expected_version": sub["version"]}, customer, ""), 500)
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_web_push_subscriptions WHERE active`).Scan(&count); err != nil || count != 1 {
		t.Fatal("audit failure committed unsubscription", count, err)
	}
	if _, err := db.Pool.Exec(t.Context(), `DELETE FROM sessions WHERE user_id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", path, in, customer, ""), 401)
	expect(t, request(h, "GET", "/api/v1/me/notification-web-push", nil, customer, ""), 401)
}

func TestWebPushAPIRejectsUnsafeSubscriptionAndMissingCAS(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "webpush-input-root", "super_admin")
	_, customer := user(t, db, h, "webpush-input-owner", "customer")
	webPushInstance(t, h, admin, "input-push")
	path := "/api/v1/me/notification-web-push/subscriptions"
	in := webPushInput(t, "input-push", "input-capability", 0)
	expect(t, requestFrom(h, "POST", path, in, "127.0.0.1:1234", customer, map[string]string{"Origin": "https://other.example"}), 403)
	// Individually valid VAPID keys from two identities cannot be enabled.
	otherPublic, _ := webPushKeys(t)
	mismatch := request(h, "PUT", "/api/v1/admin/channel-instances/input-push", map[string]any{"display_name": "不匹配的密钥", "enabled": true, "config": map[string]any{"public_key": otherPublic, "subject": "mailto:admin@example.test"}, "expected_version": 1}, admin, "")
	expect(t, mismatch, 409)
	var version int64
	if err := db.Pool.QueryRow(t.Context(), `SELECT version FROM channel_instances WHERE id='input-push'`).Scan(&version); err != nil || version != 1 {
		t.Fatal("mismatched VAPID pair was persisted", version, err)
	}
	delete(in, "expected_version")
	expect(t, request(h, "POST", path, in, customer, ""), 400)
	in["expected_version"] = 0
	in["subscription"].(map[string]any)["endpoint"] = "https://127.0.0.1/private"
	expect(t, request(h, "POST", path, in, customer, ""), 409)
	if _, err := db.Pool.Exec(t.Context(), `UPDATE channel_instances SET enabled=false WHERE id='input-push'`); err != nil {
		t.Fatal(err)
	}
	in = webPushInput(t, "input-push", "disabled-capability", 0)
	expect(t, request(h, "POST", path, in, customer, ""), 409)
}

func TestWebPushAPIHistoryWorksWithoutInboxAndRechecksOwnership(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "webpush-history-root", "super_admin")
	uid, customer := user(t, db, h, "webpush-history-customer", "customer")
	otherID, other := user(t, db, h, "webpush-history-other", "customer")
	webPushInstance(t, h, admin, "history-push")
	path := "/api/v1/me/notification-web-push/history?limit=1"
	expect(t, request(h, "GET", path, nil, "", ""), 401)
	expect(t, request(h, "POST", "/api/v1/me/notification-web-push/subscriptions", webPushInput(t, "history-push", "history-capability", 0), customer, ""), 200)
	expect(t, request(h, "PATCH", "/api/v1/me/notification-preferences", map[string]any{"channel": "web_push", "enabled": true, "expected_version": 0}, customer, ""), 200)
	expect(t, request(h, "PATCH", "/api/v1/admin/notification-center/rules/invoice-paid-web_push", map[string]any{"enabled": true, "template_id": "invoice-paid-web_push", "instance_id": "history-push", "expected_version": 1}, admin, ""), 200)
	var latestInvoice int64
	for i := 0; i < 2; i++ {
		latestInvoice = centerInvoice(t, db, uid, fmt.Sprintf("PUSH-HISTORY-%d", i))
		job, err := db.ClaimNotificationJob(t.Context())
		if err != nil || job == nil || job.Channel != "web_push" {
			t.Fatal("missing push job", err)
		}
		if err = db.CompleteNotificationJob(t.Context(), *job, notify.Result{Outcome: "accepted", Code: "push_accepted"}); err != nil {
			t.Fatal(err)
		}
	}
	state := request(h, "GET", path, nil, customer, "")
	expect(t, state, 200)
	items := data(t, state)["items"].([]any)
	if len(items) != 1 || data(t, state)["has_more"] != true || items[0].(map[string]any)["name"] != "付款成功" || !strings.Contains(state.Body.String(), "PUSH-HISTORY-1") {
		t.Fatal("push-only detail or pagination missing", state.Body.String())
	}
	second := request(h, "GET", path+"&offset=1", nil, customer, "")
	expect(t, second, 200)
	if data(t, second)["has_more"] != false || !strings.Contains(second.Body.String(), "PUSH-HISTORY-0") {
		t.Fatal("history second page mismatch", second.Body.String())
	}
	otherState := request(h, "GET", path, nil, other, "")
	expect(t, otherState, 200)
	if len(data(t, otherState)["items"].([]any)) != 0 {
		t.Fatal("other customer could read notification history")
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE invoices SET user_id=$2 WHERE id=$1`, latestInvoice, otherID); err != nil {
		t.Fatal(err)
	}
	state = request(h, "GET", path, nil, customer, "")
	expect(t, state, 200)
	if strings.Contains(state.Body.String(), "PUSH-HISTORY-1") || data(t, state)["has_more"] != false {
		t.Fatal("history retained access after resource reassignment")
	}
}
