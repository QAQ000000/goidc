package notification

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/secure"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const notificationProcessOptIn = "ZJMFGO_WORKER_CRASH_ACCEPTANCE"
const notificationProcessPrefix = "goidc_notification_process_"
const notificationProcessBot = "bot-process-fixture"
const notificationProcessChat = int64(987654321)

// This acceptance suite SIGKILLs an owned test subprocess running the production
// RunNotificationCenter loop and Store. The only substituted production boundary
// is HTTP transport to a loopback Telegram protocol simulator. It does not claim
// that cmd/worker or a complete release binary was killed, or that Telegram
// accepted a real message. Normal unit/integration runs must explicitly opt in.
func TestNotificationCenterProcessRecovery(t *testing.T) {
	if os.Getenv(notificationProcessOptIn) != "1" {
		t.Skip("set ZJMFGO_WORKER_CRASH_ACCEPTANCE=1 for owned worker-loop subprocess recovery")
	}
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux SIGKILL process status")
	}
	if _, err := notificationProcessDatabase(os.Getenv("ZJMFGO_TEST_DATABASE_URL"), false); err != nil {
		t.Fatal(err)
	}
	t.Run("accepted_before_completion_sigkill_unknown_and_inbox_once", notificationProcessAcceptedCrash)
	t.Run("two_worker_competition", notificationProcessCompetition)
	t.Run("slow_telegram_isolated_from_legacy_mail_and_sms", notificationProcessLegacyIsolation)
}

type notificationProcessFixture struct {
	db      *store.Store
	dsn     string
	schema  string
	userID  int64
	dir     string
	nonce   string
	key     []byte
	version int
}

func notificationProcessDatabase(raw string, child bool) (*pgxpool.Config, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("isolated ZJMFGO_TEST_DATABASE_URL is required")
	}
	config, err := pgxpool.ParseConfig(raw)
	if err != nil {
		return nil, errors.New("invalid isolated test database configuration")
	}
	if config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "::1" || !strings.HasSuffix(config.ConnConfig.Database, "_test") {
		return nil, errors.New("refusing database outside loopback or without _test suffix")
	}
	for _, fallback := range config.ConnConfig.Fallbacks {
		if fallback.Host != "127.0.0.1" && fallback.Host != "::1" {
			return nil, errors.New("refusing non-loopback fallback database host")
		}
	}
	if child && !regexp.MustCompile(`^goidc_notification_process_[0-9a-f]{16}$`).MatchString(config.ConnConfig.RuntimeParams["search_path"]) {
		return nil, errors.New("helper requires its parent's isolated notification schema")
	}
	config.MaxConns = 6
	return config, nil
}

func notificationProcessDSN(raw, schema, application string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" || parsed.Host == "" {
		return "", errors.New("notification process fixture requires a PostgreSQL URL")
	}
	query := parsed.Query()
	query.Del("options")
	query.Set("search_path", schema)
	query.Set("application_name", application)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func newNotificationProcessFixture(t *testing.T) *notificationProcessFixture {
	t.Helper()
	config, err := notificationProcessDatabase(os.Getenv("ZJMFGO_TEST_DATABASE_URL"), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	base, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("isolated PostgreSQL unavailable")
	}
	var version int
	if err = base.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil || version < 180000 {
		base.Close()
		t.Fatal("isolated PostgreSQL 18 is required")
	}
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		base.Close()
		t.Fatal(err)
	}
	nonce := hex.EncodeToString(random)
	schema := notificationProcessPrefix + nonce[:16]
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		base.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.RuntimeParams["application_name"] = "notification-process-parent-" + nonce[:16]
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = base.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		base.Close()
		t.Fatal(err)
	}
	f := &notificationProcessFixture{db: &store.Store{Pool: pool, ProvisionEncryptionKey: make([]byte, 32)}, schema: schema, dir: t.TempDir(), nonce: nonce, key: make([]byte, 32), version: version}
	t.Cleanup(func() {
		f.db.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := base.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Error("isolated notification schema cleanup failed", err)
		}
		base.Close()
	})
	// Config.ConnString preserves the originally parsed input, not subsequent
	// RuntimeParams mutations. Serialize the child's schema explicitly.
	f.dsn, err = notificationProcessDSN(os.Getenv("ZJMFGO_TEST_DATABASE_URL"), schema, "notification-process-parent-"+nonce[:16])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.dir, "parent-nonce"), []byte(nonce), 0600); err != nil {
		t.Fatal(err)
	}
	if err = f.db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.db.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name) VALUES('notification-process','notification-process@example.test','unused','Process fixture') RETURNING id`).Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	fields := map[string][]byte{}
	for field, value := range map[string]string{"bot_token": "123456:fixture_only", "webhook_secret": "fixture_webhook_only"} {
		fields[field], err = secure.Seal(f.key, []byte("zjmfgo:channel-credential:v1:"+notificationProcessBot+":"+field), []byte(value))
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,config,secret_fields) VALUES($1,'bot','telegram_bot','Process fixture',true,'{"bot_username":"fixture_bot"}',$2)`, notificationProcessBot, raw)
	f.exec(t, `UPDATE notification_routes SET enabled=true,instance_id=CASE WHEN channel='telegram' THEN $1 ELSE NULL END`, notificationProcessBot)
	f.exec(t, `INSERT INTO user_notification_preferences(user_id,channel,enabled) VALUES($1,'telegram',true),($1,'inbox',true)`, f.userID)
	f.exec(t, `INSERT INTO notification_targets(user_id,channel,instance_id,target) VALUES($1,'telegram',$2,$3)`, f.userID, notificationProcessBot, strconv.FormatInt(notificationProcessChat, 10))
	return f
}

func (f *notificationProcessFixture) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	if _, err := f.db.Pool.Exec(t.Context(), statement, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *notificationProcessFixture) count(t *testing.T, statement string, args ...any) int {
	t.Helper()
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), statement, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *notificationProcessFixture) queue(t *testing.T, invoiceNo string) int64 {
	t.Helper()
	var invoiceID, deliveryID int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents,paid_at) VALUES($1,$2,'manual','paid',12345,now()) RETURNING id`, invoiceNo, f.userID).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.WithTx(t.Context(), func(tx pgx.Tx) error { return f.db.QueuePaymentSuccessNotifications(t.Context(), tx, invoiceID) }); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT d.id FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE e.invoice_id=$1 AND d.channel='telegram'`, invoiceID).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	return deliveryID
}

type notificationProcessChild struct {
	cmd     *exec.Cmd
	done    chan error
	stopped bool
	label   string
	logPath string
}

func (f *notificationProcessFixture) start(t *testing.T, endpoint, label string, legacy bool) *notificationProcessChild {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childDSN, err := notificationProcessDSN(f.dsn, f.schema, "notification-process-"+label+"-"+f.nonce[:16])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = notificationProcessDatabase(childDSN, true); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(f.dir, label+".log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	c := &notificationProcessChild{cmd: exec.Command(binary, "-test.run=^TestNotificationCenterProcessHelper$", "-test.count=1", "-test.v"), done: make(chan error, 1), label: label, logPath: logPath}
	c.cmd.Dir = f.dir
	// Deliberately do not inherit application credentials, deployment config,
	// proxies, or DATABASE_URL. Only this random _test schema is accessible.
	c.cmd.Env = []string{
		notificationProcessOptIn + "=1", "ZJMFGO_NOTIFICATION_PROCESS_HELPER=1",
		"ZJMFGO_TEST_DATABASE_URL=" + childDSN, "REQUIRE_INTEGRATION=1",
		"ZJMFGO_NOTIFICATION_PROCESS_DIR=" + f.dir, "ZJMFGO_NOTIFICATION_PROCESS_NONCE=" + f.nonce,
		"ZJMFGO_NOTIFICATION_PROCESS_ENDPOINT=" + endpoint, "ZJMFGO_NOTIFICATION_PROCESS_LABEL=" + label,
		"ZJMFGO_NOTIFICATION_PROCESS_LEGACY=" + strconv.FormatBool(legacy),
	}
	c.cmd.Stdout, c.cmd.Stderr = log, log
	if err = c.cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	go func() { c.done <- c.cmd.Wait() }()
	t.Cleanup(func() {
		if !c.stopped {
			_ = c.cmd.Process.Kill()
			<-c.done
			c.stopped = true
		}
		if t.Failed() {
			data, _ := os.ReadFile(c.logPath)
			t.Logf("owned notification helper %s diagnostics: %s", c.label, data)
		}
		_ = log.Close()
	})
	notificationProcessWait(t, "helper ready", 15*time.Second, func() bool {
		select {
		case err := <-c.done:
			c.stopped = true
			data, _ := os.ReadFile(c.logPath)
			t.Fatalf("owned helper %s exited before readiness: %v %s", c.label, err, data)
		default:
		}
		_, err := os.Stat(filepath.Join(f.dir, label+".ready"))
		return err == nil
	})
	return c
}

func (c *notificationProcessChild) stop(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if c.stopped {
		return
	}
	if err := c.cmd.Process.Signal(sig); err != nil {
		t.Fatal("owned helper signal failed", err)
	}
	select {
	case err := <-c.done:
		c.stopped = true
		status, ok := c.cmd.ProcessState.Sys().(syscall.WaitStatus)
		if sig == syscall.SIGKILL {
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatal("owned notification helper was not SIGKILLed", err)
			}
		} else if err != nil {
			data, _ := os.ReadFile(c.logPath)
			t.Fatalf("owned helper stop failed: %v %s", err, data)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("owned helper stop timeout")
	}
}

func notificationProcessWait(t *testing.T, label string, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("notification process acceptance timeout: " + label)
}

func notificationProcessResponse(w http.ResponseWriter, id int64) {
	body := fmt.Sprintf(`{"ok":true,"result":{"message_id":%d,"chat":{"id":%d,"type":"private"}}}`, id, notificationProcessChat)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = io.WriteString(w, body)
	if flush, ok := w.(http.Flusher); ok {
		flush.Flush()
	}
}

func notificationProcessAcceptedCrash(t *testing.T) {
	f := newNotificationProcessFixture(t)
	deliveryID := f.queue(t, "PROCESS-ACCEPTED-CRASH")
	var requests atomic.Int32
	accepted := make(chan struct{}, 1)
	fixtureErrors := make(chan error, 1)
	release := make(chan struct{})
	released := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) != 1 {
			notificationProcessResponse(w, 7999)
			return
		}
		blocker, err := f.db.Pool.Begin(t.Context())
		if err != nil {
			fixtureErrors <- err
			http.Error(w, "fixture unavailable", 503)
			return
		}
		if _, err = blocker.Exec(t.Context(), `SELECT id FROM notification_deliveries WHERE id=$1 FOR UPDATE`, deliveryID); err != nil {
			_ = blocker.Rollback(context.Background())
			fixtureErrors <- err
			http.Error(w, "fixture unavailable", 503)
			return
		}
		defer func() { _ = blocker.Rollback(context.Background()); close(released) }()
		notificationProcessResponse(w, 7001)
		accepted <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			// Preserve the row lock until the parent verifies the killed process.
			<-release
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(unblock)
	first := f.start(t, server.URL, "crash", false)
	select {
	case <-accepted:
	case err := <-fixtureErrors:
		t.Fatal(err)
	case <-time.After(15 * time.Second):
		t.Fatal("Telegram loopback never accepted the owned worker request")
	}
	// A complete successful protocol response has been flushed. The real
	// CompleteNotificationJob call must now be blocked on our delivery row.
	notificationProcessWait(t, "accepted response waiting to persist", 10*time.Second, func() bool {
		return f.count(t, `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE '%SELECT attempts,expires_at FROM notification_deliveries%'`, "notification-process-crash-"+f.nonce[:16]) == 1
	})
	if f.count(t, `SELECT count(*) FROM notification_deliveries WHERE id=$1 AND status='sending' AND attempts=1 AND lease_token IS NOT NULL`, deliveryID) != 1 || f.count(t, `SELECT count(*) FROM admin_inbox_messages WHERE user_id=$1`, f.userID) != 1 {
		t.Fatal("external lease or preceding atomic inbox write was not durable")
	}
	first.stop(t, syscall.SIGKILL)
	t.Logf("faultpoint reached: owned pid=%d SIGKILL confirmed after loopback accepted response while real completion waited on PG row lock", first.cmd.Process.Pid)
	unblock()
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture completion lock did not release")
	}
	if f.count(t, `SELECT count(*) FROM notification_deliveries WHERE id=$1 AND status='sending' AND attempts=1`, deliveryID) != 1 {
		t.Fatal("SIGKILL unexpectedly completed the accepted delivery")
	}
	// Only after proving the owned process is dead, accelerate its two-minute
	// lease. The replacement production loop performs recovery itself.
	f.exec(t, `UPDATE notification_deliveries SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, deliveryID)
	restarted := f.start(t, server.URL, "restart", false)
	competitor := f.start(t, server.URL, "recovery-competitor", false)
	notificationProcessWait(t, "production recovery to unknown", 10*time.Second, func() bool {
		return f.count(t, `SELECT count(*) FROM notification_deliveries d JOIN notification_delivery_attempts a ON a.delivery_id=d.id WHERE d.id=$1 AND d.status='unknown' AND d.code='lease_expired' AND d.attempts=1 AND d.lease_token IS NULL AND a.status='unknown' AND a.finished_at IS NOT NULL`, deliveryID) == 1
	})
	time.Sleep(4500 * time.Millisecond) // Observe more than two real loop ticks.
	restarted.stop(t, syscall.SIGTERM)
	competitor.stop(t, syscall.SIGTERM)
	if requests.Load() != 1 || f.count(t, `SELECT count(*) FROM notification_delivery_attempts WHERE delivery_id=$1`, deliveryID) != 1 || f.count(t, `SELECT count(*) FROM admin_inbox_messages WHERE user_id=$1`, f.userID) != 1 {
		t.Fatal("restart repeated an unknown external result or atomic inbox write")
	}
	t.Logf("PG=%d owned test subprocess pid=%d SIGKILL after loopback acceptance/before completion; production loop recovered unknown; two replacement workers produced no resend; inbox=1 attempts=1 requests=1", f.version, first.cmd.Process.Pid)
}

func notificationProcessCompetition(t *testing.T) {
	f := newNotificationProcessFixture(t)
	var mu sync.Mutex
	workers, messages := map[string]bool{}, map[string]int{}
	barrier := make(chan struct{})
	var openBarrier sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.ChatID != notificationProcessChat {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		mu.Lock()
		workers[r.Header.Get("X-Notification-Fixture-Worker")] = true
		messages[payload.Text]++
		if len(workers) >= 2 {
			openBarrier.Do(func() { close(barrier) })
		}
		mu.Unlock()
		select {
		case <-barrier:
		case <-r.Context().Done():
			return
		}
		notificationProcessResponse(w, 7002)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { openBarrier.Do(func() { close(barrier) }) })
	first := f.start(t, server.URL, "competitor-a", false)
	second := f.start(t, server.URL, "competitor-b", false)
	const jobs = 8
	for i := 0; i < jobs; i++ {
		f.queue(t, fmt.Sprintf("PROCESS-COMPETE-%d", i))
	}
	notificationProcessWait(t, "two workers finish disjoint claims", 15*time.Second, func() bool {
		return f.count(t, `SELECT count(*) FROM notification_deliveries WHERE channel='telegram' AND status='accepted' AND attempts=1`) == jobs
	})
	first.stop(t, syscall.SIGTERM)
	second.stop(t, syscall.SIGTERM)
	mu.Lock()
	workerCount, messageCount, total := len(workers), len(messages), 0
	for _, count := range messages {
		total += count
		if count != 1 {
			mu.Unlock()
			t.Fatal("two production loops sent a delivery more than once")
		}
	}
	mu.Unlock()
	if workerCount != 2 || messageCount != jobs || total != jobs || f.count(t, `SELECT count(*) FROM admin_inbox_messages WHERE user_id=$1`, f.userID) != jobs || f.count(t, `SELECT count(*) FROM notification_delivery_attempts WHERE status='accepted'`) != jobs*2 {
		t.Fatalf("competition invariant workers=%d unique_messages=%d requests=%d", workerCount, messageCount, total)
	}
	t.Logf("PG=%d production loops in two owned test subprocesses both dispatched; distinct Telegram messages=%d requests=%d; inbox=%d; all attempts=1", f.version, messageCount, total, jobs)
}

func notificationProcessLegacyIsolation(t *testing.T) {
	f := newNotificationProcessFixture(t)
	var telegramRequests, smsRequests atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sms" {
			var payload map[string]string
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer fixture_only" || json.NewDecoder(r.Body).Decode(&payload) != nil || payload["message"] != "Process isolation fixture" {
				http.Error(w, "invalid fixture SMS request", 400)
				return
			}
			smsRequests.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		telegramRequests.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":503}`)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(unblock)
	smtpAddress, mailAccepted := notificationProcessSMTP(t)
	host, portText, err := net.SplitHostPort(smtpAddress)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	mailConfig, _ := json.Marshal(map[string]any{"host": host, "port": port, "from": "fixture@example.test", "tls_mode": "none"})
	f.exec(t, `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,config) VALUES('mail-process-fixture','mail','smtp','Process mail fixture',true,$1)`, mailConfig)
	smsSecret, err := secure.Seal(f.key, []byte("zjmfgo:channel-instance:v1:sms-process-fixture"), []byte("fixture_only"))
	if err != nil {
		t.Fatal(err)
	}
	smsConfig, _ := json.Marshal(map[string]any{"endpoint": server.URL + "/sms"})
	f.exec(t, `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,config,secret_ciphertext,secret_aad) VALUES('sms-process-fixture','sms','generic_http_json','Process SMS fixture',true,$1,$2,'zjmfgo:channel-instance:v1:sms-process-fixture')`, smsConfig, smsSecret)
	deliveryID := f.queue(t, "PROCESS-SLOW-ISOLATION")
	child := f.start(t, server.URL, "isolation", true)
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("Telegram request did not enter the slow fixture")
	}
	// Enqueue legacy work only after the production notification loop is
	// blocked inside external Telegram I/O. Its companion test goroutine
	// executes real legacy claim/resolve/send/evidence/complete methods.
	f.exec(t, `INSERT INTO email_outbox(recipient,subject,body,kind,channel_instance_id) VALUES('recipient@example.test','Process mail fixture','Process isolation fixture','system','mail-process-fixture')`)
	if err = f.db.WithTx(t.Context(), func(tx pgx.Tx) error {
		_, err := f.db.QueueSMSWithID(t.Context(), tx, "+8613800000000", "Process isolation fixture", "test", "", time.Now().Add(time.Hour), "sms-process-fixture")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	notificationProcessWait(t, "legacy mail and SMS continue during slow Telegram", 10*time.Second, func() bool {
		return f.count(t, `SELECT (SELECT count(*) FROM email_outbox WHERE status='sent')+(SELECT count(*) FROM sms_outbox WHERE status='sent')`) == 2
	})
	if f.count(t, `SELECT count(*) FROM notification_deliveries WHERE id=$1 AND status='sending' AND attempts=1`, deliveryID) != 1 || smsRequests.Load() != 1 || mailAccepted.Load() != 1 {
		t.Fatal("slow Telegram blocked or duplicated independent legacy work")
	}
	unblock()
	notificationProcessWait(t, "Telegram 503 is unknown without resend", 10*time.Second, func() bool {
		return f.count(t, `SELECT count(*) FROM notification_deliveries WHERE id=$1 AND status='unknown' AND attempts=1`, deliveryID) == 1
	})
	time.Sleep(2200 * time.Millisecond)
	child.stop(t, syscall.SIGTERM)
	if telegramRequests.Load() != 1 || smsRequests.Load() != 1 || mailAccepted.Load() != 1 {
		t.Fatal("terminal Telegram failure or legacy completion was resent")
	}
	t.Logf("PG=%d slow production Telegram loop stayed sending while independent legacy Store/SMTP/HTTP-SMS pipeline completed in the same owned test subprocess; Telegram503=unknown and requests=1; not a full cmd/worker composition test", f.version)
}

// TestNotificationCenterProcessHelper is never enabled by REQUIRE_INTEGRATION
// alone. Its parent-only nonce, temp control directory, schema, and loopback
// endpoint must all agree before the child opens a database or starts a loop.
func TestNotificationCenterProcessHelper(t *testing.T) {
	if os.Getenv("ZJMFGO_NOTIFICATION_PROCESS_HELPER") != "1" || os.Getenv(notificationProcessOptIn) != "1" {
		t.Skip("parent-owned notification process helper")
	}
	dir, nonce := os.Getenv("ZJMFGO_NOTIFICATION_PROCESS_DIR"), os.Getenv("ZJMFGO_NOTIFICATION_PROCESS_NONCE")
	label := os.Getenv("ZJMFGO_NOTIFICATION_PROCESS_LABEL")
	if !filepath.IsAbs(dir) || !regexp.MustCompile(`^[a-z0-9-]{1,40}$`).MatchString(label) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(nonce) {
		t.Fatal("helper requires parent control coordinates")
	}
	parentNonce, err := os.ReadFile(filepath.Join(dir, "parent-nonce"))
	if err != nil || string(parentNonce) != nonce {
		t.Fatal("helper parent nonce mismatch")
	}
	config, err := notificationProcessDatabase(os.Getenv("ZJMFGO_TEST_DATABASE_URL"), true)
	if err != nil || config.ConnConfig.RuntimeParams["search_path"] != notificationProcessPrefix+nonce[:16] {
		t.Fatal("helper refused database outside parent-owned schema")
	}
	endpoint, err := url.Parse(os.Getenv("ZJMFGO_NOTIFICATION_PROCESS_ENDPOINT"))
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.User != nil {
		t.Fatal("helper requires a loopback protocol simulator")
	}
	local := &http.Transport{Proxy: nil}
	defer local.CloseIdleConnections()
	previous := http.DefaultTransport
	http.DefaultTransport = notificationProcessTransport{local: local, endpoint: endpoint, label: label}
	defer func() { http.DefaultTransport = previous }()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("helper isolated PostgreSQL unavailable")
	}
	db := &store.Store{Pool: pool, ProvisionEncryptionKey: make([]byte, 32)}
	defer db.Close()
	if err = db.CheckSchema(ctx); err != nil {
		t.Fatal("helper isolated schema unavailable", err)
	}
	var legacy sync.WaitGroup
	if os.Getenv("ZJMFGO_NOTIFICATION_PROCESS_LEGACY") == "true" {
		legacy.Add(1)
		go func() { defer legacy.Done(); notificationProcessLegacyPump(ctx, db) }()
	}
	if err = os.WriteFile(filepath.Join(dir, label+".ready"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		cancel()
		legacy.Wait()
		t.Fatal(err)
	}
	RunNotificationCenter(ctx, db, slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cancel()
	legacy.Wait()
}

type notificationProcessTransport struct {
	local    *http.Transport
	endpoint *url.URL
	label    string
}

func (p notificationProcessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "api.telegram.org" || req.URL.Path != "/bot123456:fixture_only/sendMessage" {
		return nil, errors.New("notification helper rejected non-fixture HTTP destination")
	}
	local := req.Clone(req.Context())
	local.URL.Scheme, local.URL.Host = p.endpoint.Scheme, p.endpoint.Host
	local.Host = p.endpoint.Host
	local.Header.Set("X-Notification-Fixture-Worker", p.label)
	return p.local.RoundTrip(local)
}

// The legacy pump is a test companion to the unmodified production center
// loop. It uses real legacy methods and loopback protocols, but intentionally
// does not claim coverage of cmd/worker's full scheduling/composition code.
func notificationProcessLegacyPump(ctx context.Context, db *store.Store) {
	timer := time.NewTicker(50 * time.Millisecond)
	defer timer.Stop()
	for {
		if job, err := db.ClaimSMSJob(ctx); err == nil {
			active, checkErr := db.SMSJobActive(ctx, *job)
			if checkErr == nil && active {
				config, resolveErr := db.ResolveSMSConfigForInstance(ctx, job.ChannelInstanceID)
				if resolveErr == nil {
					evidence, sendErr := SendSMSWithEvidence(ctx, config, *job)
					if db.SaveSMSDeliveryEvidence(ctx, *job, evidence) == nil {
						_ = db.CompleteSMSJob(ctx, *job, sendErr)
					}
				}
			}
		}
		if job, err := db.ClaimEmailJob(ctx); err == nil {
			active, checkErr := db.EmailJobActive(ctx, *job)
			if checkErr == nil && active {
				implementation, config, resolveErr := db.ResolveMailConfigForInstance(ctx, job.ChannelInstanceID, store.SMTPRuntimeConfig{})
				if resolveErr == nil {
					_ = db.CompleteEmailJob(ctx, *job, SendMail(implementation, config, *job))
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func notificationProcessSMTP(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := &atomic.Int32{}
	var sessions sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			sessions.Add(1)
			go func() {
				defer sessions.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
				reader := bufio.NewReader(conn)
				_, _ = io.WriteString(conn, "220 fixture ESMTP\r\n")
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO "), strings.HasPrefix(line, "HELO "), strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
						_, _ = io.WriteString(conn, "250 fixture\r\n")
					case line == "DATA\r\n":
						_, _ = io.WriteString(conn, "354 data\r\n")
						for {
							body, err := reader.ReadString('\n')
							if err != nil {
								return
							}
							if body == ".\r\n" {
								break
							}
						}
						accepted.Add(1)
						_, _ = io.WriteString(conn, "250 accepted\r\n")
					case line == "QUIT\r\n":
						_, _ = io.WriteString(conn, "221 goodbye\r\n")
						return
					default:
						_, _ = io.WriteString(conn, "500 unsupported\r\n")
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done; sessions.Wait() })
	return listener.Addr().String(), accepted
}
