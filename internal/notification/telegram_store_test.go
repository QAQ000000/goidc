package notification

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This test deliberately joins the real Telegram protocol classifier to the
// real lease completion method: a unit-only fake would miss outcome drift.
func TestTelegramProtocolResultsPersistThroughNotificationStore(t *testing.T) {
	dsn := os.Getenv("ZJMFGO_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("REQUIRE_INTEGRATION") == "1" {
			t.Fatal("ZJMFGO_TEST_DATABASE_URL required")
		}
		t.Skip("requires isolated PostgreSQL 18 test database")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasSuffix(config.ConnConfig.Database, "_test") {
		t.Fatal("refusing database without _test suffix")
	}
	ctx := context.Background()
	base, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("isolated PostgreSQL unavailable")
	}
	defer base.Close()
	schema := fmt.Sprintf("telegram_protocol_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = base.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE") }()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	db := &store.Store{Pool: pool}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var userID, invoiceID, targetID int64
	if err = pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name) VALUES('telegram-protocol','telegram-protocol@example.test','unused','Protocol fixture') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at) VALUES('TELEGRAM-PROTOCOL',$1,'paid',100,now()) RETURNING id`, userID).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,config) VALUES('bot-fixture','bot','telegram_bot','Protocol fixture',true,'{"bot_username":"example_bot"}'); UPDATE notification_routes SET enabled=true,instance_id='bot-fixture' WHERE id='invoice-paid-telegram'`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO user_notification_preferences(user_id,channel,enabled) VALUES($1,'telegram',true)`, userID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO notification_targets(user_id,channel,instance_id,target) VALUES($1,'telegram','bot-fixture','987654321') RETURNING id`, userID).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, response, status, attempt string
		httpStatus                      int
	}{
		{"accepted", `{"ok":true,"result":{"message_id":77,"chat":{"id":987654321,"type":"private"}}}`, "accepted", "accepted", 200},
		{"forbidden", `{"ok":false,"error_code":403}`, "failed", "failed", 403},
		{"rate_limit", `{"ok":false,"error_code":429,"parameters":{"retry_after":42}}`, "pending", "failed", 429},
		{"server_unknown", `{"ok":false,"error_code":502}`, "unknown", "unknown", 502},
		{"malformed_success", `{"ok":true,"result":{}}`, "unknown", "unknown", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var eventID, deliveryID int64
			if err = pool.QueryRow(ctx, `INSERT INTO notification_events(kind,dedupe_key,user_id,invoice_id,payload) VALUES('invoice.paid',$1,$2,$3,'{}') RETURNING id`, tc.name, userID, invoiceID).Scan(&eventID); err != nil {
				t.Fatal(err)
			}
			if err = pool.QueryRow(ctx, `INSERT INTO notification_deliveries(event_id,route_key,route_id,route_version,template_version,preference_version,target_id,target_version,channel,instance_id,target,subject,body) VALUES($1,'invoice-paid-telegram','invoice-paid-telegram',1,1,1,$2,1,'telegram','bot-fixture','987654321','付款成功','付款成功 fixture') RETURNING id`, eventID, targetID).Scan(&deliveryID); err != nil {
				t.Fatal(err)
			}
			job, claimErr := db.ClaimNotificationJob(ctx)
			if claimErr != nil || job.ID != deliveryID {
				t.Fatalf("real claim failed: %v", claimErr)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.httpStatus)
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			provider, factoryErr := newTelegramProvider(telegramFixtureConfig(), server.Client(), server.URL)
			if factoryErr != nil {
				t.Fatal(factoryErr)
			}
			result := provider.Send(ctx, *job)
			before := time.Now()
			if completeErr := db.CompleteNotificationJob(ctx, *job, result); completeErr != nil {
				t.Fatal(completeErr)
			}
			var status, attempt, messageID, code string
			var next time.Time
			if err = pool.QueryRow(ctx, `SELECT d.status,a.status,d.provider_message_id,d.code,d.next_attempt_at FROM notification_deliveries d JOIN notification_delivery_attempts a ON a.delivery_id=d.id WHERE d.id=$1`, deliveryID).Scan(&status, &attempt, &messageID, &code, &next); err != nil {
				t.Fatal(err)
			}
			if status != tc.status || attempt != tc.attempt || (messageID == "77") != (tc.status == "accepted") {
				t.Fatalf("protocol/store drift: result=%+v status=%s attempt=%s", result, status, attempt)
			}
			if tc.status == "pending" && (result.Outcome != "rate_limited" || code != "rate_limited" || next.Before(before.Add(41*time.Second))) {
				t.Fatal("explicit Telegram rate limit did not retain its retry delay")
			}
			if _, claimErr = db.ClaimNotificationJob(ctx); claimErr != pgx.ErrNoRows {
				t.Fatal("completed/unknown external result was immediately reclaimed")
			}
		})
	}
}
