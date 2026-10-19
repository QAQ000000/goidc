package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/secure"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type webPushProtocolStore struct {
	*store.Store
	implementation string
}

func (s *webPushProtocolStore) RuntimeChannelInstance(ctx context.Context, id string, category channel.Category) (module.Config, string, error) {
	config, implementation, err := s.Store.RuntimeChannelInstance(ctx, id, category)
	if err == nil && category == channel.Push && implementation == "web_push" {
		implementation = s.implementation
	}
	return config, implementation, err
}

// Only HTTP transport is substituted: real encrypted subscription storage,
// event fanout, claim, eligibility, worker dispatch and result fencing execute.
func TestWebPushProtocolResultsPersistThroughNotificationWorkerAndStore(t *testing.T) {
	if os.Getenv("ZJMFGO_TEST_DATABASE_URL") == "" && os.Getenv("REQUIRE_INTEGRATION") != "1" {
		t.Skip("requires isolated PostgreSQL 18 test database")
	}
	f := newNotificationProcessFixture(t)
	ctx := t.Context()
	config, _, _ := webPushFixture(t)
	values, err := json.Marshal(config.Values)
	if err != nil {
		t.Fatal(err)
	}
	// A module with one credential uses the channel's single-secret storage
	// contract; independently named secret_fields is reserved for multi-secret
	// implementations such as Telegram.
	const secretAAD = "zjmfgo:channel-instance:v1:push-fixture"
	secret, err := secure.Seal(f.key, []byte(secretAAD), []byte(config.Secrets["private_key"]))
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE notification_routes SET enabled=false`)
	f.exec(t, `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,config,secret_ciphertext,secret_aad) VALUES('push-fixture','push','web_push','Push protocol fixture',true,$1,$2,$3)`, values, secret, secretAAD)
	f.exec(t, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('push-protocol-session',$1,now()+interval '1 hour')`, f.userID)
	f.exec(t, `INSERT INTO user_notification_preferences(user_id,channel,enabled) VALUES($1,'web_push',true)`, f.userID)
	if err = f.db.WithTx(ctx, func(tx pgx.Tx) error {
		_, e := f.db.UpdateNotificationRoute(ctx, tx, "invoice-paid-web_push", store.NotificationRouteInput{Enabled: true, InstanceID: "push-fixture", ExpectedVersion: 1})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, status, attempt, code string
		httpStatus                  int
	}{
		{"accepted", "accepted", "accepted", "push_accepted", 201},
		{"gone", "failed", "failed", "push_subscription_gone", 410},
		{"rate_limit", "pending", "failed", "rate_limited", 429},
		{"server_unknown", "unknown", "unknown", "push_response_unknown", 503},
		{"lost_response", "unknown", "unknown", "push_transport_unknown", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.exec(t, `UPDATE notification_web_push_subscriptions SET active=false,version=version+1 WHERE active`)
			_, job, receiver := webPushFixture(t)
			job.PushSubscription.Endpoint += "-" + tc.name
			var browser store.WebPushSubscriptionInfo
			if err := f.db.WithTx(ctx, func(tx pgx.Tx) error {
				var e error
				browser, e = f.db.SaveWebPushSubscription(ctx, tx, f.userID, "push-protocol-session", "push-fixture", *job.PushSubscription, 0)
				return e
			}); err != nil {
				t.Fatal(err)
			}
			var invoiceID, deliveryID int64
			if err := f.db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at) VALUES($1,$2,'paid',12345,now()) RETURNING id`, "PUSH-"+tc.name, f.userID).Scan(&invoiceID); err != nil {
				t.Fatal(err)
			}
			if err := f.db.WithTx(ctx, func(tx pgx.Tx) error { return f.db.QueuePaymentSuccessNotifications(ctx, tx, invoiceID) }); err != nil {
				t.Fatal(err)
			}
			if err := f.db.Pool.QueryRow(ctx, `SELECT d.id FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE e.invoice_id=$1 AND d.channel='web_push'`, invoiceID).Scan(&deliveryID); err != nil {
				t.Fatal(err)
			}
			var otherBrowser store.WebPushSubscriptionInfo
			if tc.httpStatus == 410 {
				other := *job.PushSubscription
				other.Endpoint += "-unaffected-browser"
				if err := f.db.WithTx(ctx, func(tx pgx.Tx) error {
					var e error
					otherBrowser, e = f.db.SaveWebPushSubscription(ctx, tx, f.userID, "push-protocol-session", "push-fixture", other, 0)
					return e
				}); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				verifyWebPushVAPID(t, r.Header.Get("Authorization"), config.Values["public_key"].(string))
				raw, err := io.ReadAll(io.LimitReader(r.Body, 8193))
				if err != nil || string(decryptWebPushFixture(t, raw, receiver, job.PushSubscription.Auth)) != webPushPrivatePayload {
					t.Error("real worker did not send a decryptable privacy-preserving message")
				}
				if tc.httpStatus == 0 {
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = connection.Close()
					return
				}
				if tc.httpStatus == 429 {
					w.Header().Set("Retry-After", "3600")
				}
				w.WriteHeader(tc.httpStatus)
				_, _ = w.Write([]byte("secret_marker_response"))
			}))
			defer server.Close()
			client := server.Client()
			transport := client.Transport.(*http.Transport).Clone()
			transport.TLSClientConfig.ServerName = "127.0.0.1"
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			client.Transport = transport
			implementation := fmt.Sprintf("push_store_protocol_%d", centerFactorySequence.Add(1))
			if err := RegisterPushProvider(channel.Descriptor{ID: implementation, Name: "Local TLS protocol fixture", Category: channel.Push}, func(cfg module.Config) (PushProvider, error) { return newWebPushProvider(cfg, client) }); err != nil {
				t.Fatal(err)
			}
			workerStore := &webPushProtocolStore{Store: f.db, implementation: implementation}
			before := time.Now()
			count, err := DrainNotificationCenter(ctx, workerStore, 10)
			if err != nil || count != 1 || calls.Load() != 1 {
				t.Fatalf("real push dispatch failed: count=%d calls=%d err=%v", count, calls.Load(), err)
			}
			var status, attempt, code string
			var next time.Time
			if err := f.db.Pool.QueryRow(ctx, `SELECT d.status,a.status,d.code,d.next_attempt_at FROM notification_deliveries d JOIN notification_delivery_attempts a ON a.delivery_id=d.id WHERE d.id=$1`, deliveryID).Scan(&status, &attempt, &code, &next); err != nil {
				t.Fatal(err)
			}
			if status != tc.status || attempt != tc.attempt || code != tc.code {
				t.Fatalf("Web Push protocol/store drift: status=%s attempt=%s code=%s", status, attempt, code)
			}
			if tc.httpStatus == 429 && next.Before(before.Add(3599*time.Second)) {
				t.Fatal("rate limit was retried before its explicit deadline")
			}
			if tc.httpStatus == 410 && (f.count(t, `SELECT count(*) FROM notification_web_push_subscriptions WHERE id=$1 AND active`, browser.ID) != 0 || f.count(t, `SELECT count(*) FROM notification_web_push_subscriptions WHERE id=$1 AND active`, otherBrowser.ID) != 1) {
				t.Fatal("410 did not invalidate only the matching browser")
			}
			count, err = DrainNotificationCenter(ctx, workerStore, 10)
			if err != nil || count != 0 || calls.Load() != 1 {
				t.Fatal("completed, unknown, or rate-limited push was immediately replayed")
			}
		})
	}
}
