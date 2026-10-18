package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/secure"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const notificationUpgrade160 = "160_product_category_legacy_mapping.sql"
const notificationUpgrade161 = "161_channel_configuration_contract.sql"
const notificationUpgrade162 = "162_unified_notifications.sql"

// Start with the existing genuine pre-upgrade migration helper and apply only
// the actual remaining SQL through 160. No 161 column or 162 table has existed
// when the old configuration, receipts and pending jobs are written.
func notificationSchema160(t *testing.T) *Store {
	t.Helper()
	db := isolatedOrderRefundSchema153(t)
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		if name > "153_resolved_topup_external_refunds.sql" && name <= notificationUpgrade160 {
			applyNotificationUpgradeMigration(t, db, name)
		}
	}
	var boundary string
	var newObjects bool
	err = db.Pool.QueryRow(t.Context(), `SELECT max(version),
 to_regclass('notification_events') IS NOT NULL OR EXISTS(
 SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema()
 AND table_name='channel_instances' AND column_name IN ('schema_version','secret_fields'))
 FROM schema_migrations`).Scan(&boundary, &newObjects)
	if err != nil || boundary != notificationUpgrade160 || newObjects {
		t.Fatal("fixture must originate at actual schema 160", boundary, newObjects, err)
	}
	return db
}

func applyNotificationUpgradeMigration(t *testing.T, db *Store, name string) {
	t.Helper()
	body, err := migrations.Files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.WithTx(t.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), string(body)); err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		_, err := tx.Exec(t.Context(), `INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)`, name, hex.EncodeToString(sum[:]))
		return err
	}); err != nil {
		t.Fatalf("apply actual upgrade checkpoint %s: %v", name, err)
	}
}

func notificationUpgradeSnapshot(t *testing.T, db *Store, current bool) string {
	t.Helper()
	// Remove only the two new columns so this comparison includes every old
	// column, timestamp and ciphertext byte, without printing sensitive rows.
	query := `SELECT jsonb_build_object(
 'instances',(SELECT jsonb_agg(to_jsonb(c)-'schema_version'-'secret_fields' ORDER BY id) FROM channel_instances c),
 'payment_settings',(SELECT to_jsonb(p) FROM payment_settings p),
 'smtp_settings',(SELECT to_jsonb(s) FROM smtp_settings s),
 'sms_settings',(SELECT to_jsonb(s) FROM sms_settings s),
 'users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),
 'invoices',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invoices i),
 'payments',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM payment_transactions p),
 'webhooks',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM payment_webhook_events e),
 'email_outbox',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM email_outbox e),
 'sms_outbox',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM sms_outbox s),
 'legacy_delivery_events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM notification_delivery_events e)
 )`
	if current {
		query += ` || jsonb_build_object(
 'migrations',(SELECT jsonb_agg(to_jsonb(m) ORDER BY version) FROM schema_migrations m),
 'routes',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM notification_routes r),
 'templates',(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM notification_message_templates m),
 'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM notification_events e),
 'deliveries',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM notification_deliveries d),
 'preferences',(SELECT jsonb_agg(to_jsonb(p) ORDER BY user_id,channel) FROM user_notification_preferences p))`
	}
	var snapshot string
	if err := db.Pool.QueryRow(t.Context(), `SELECT (`+strings.TrimPrefix(query, "SELECT ")+`)::text`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// This synthetic legacy fixture uses production encryption and the unchanged
// pre-162 email/SMS enqueue functions. The historical paid invoice/receipt is
// seeded explicitly: it is not a replay of an old binary or a production dump.
func seedNotificationUpgrade160(t *testing.T, db *Store) (customer, invoice, mailID, smsID int64, receipt []byte) {
	t.Helper()
	ctx := t.Context()
	db.ProvisionEncryptionKey = bytes.Repeat([]byte{0x8e}, 32)
	_, customer = topUpFixture(t, db)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE users SET email_verified=true,phone='+8613800000000',phone_verified=true WHERE id=$1`, customer)
	pay, err := db.paymentSecret([]byte("fixture-payment-key"), true)
	if err != nil {
		t.Fatal(err)
	}
	mail, err := smtpSeal(db.ProvisionEncryptionKey, "fixture-smtp-password")
	if err != nil {
		t.Fatal(err)
	}
	sms, err := smsCrypt(db.ProvisionEncryptionKey, []byte("fixture-sms-token"), "token", true)
	if err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE payment_settings SET configured=true,enabled=true,api_url='https://payment.example.test',pid='fixture-merchant',types=ARRAY['alipay'],key_ciphertext=$1`, []byte(pay))
	exec(`UPDATE smtp_settings SET configured=true,enabled=true,host='smtp.example.test',port=587,username='fixture-user',from_address='notification@example.test',tls_mode='starttls',password_encrypted=$1`, mail)
	exec(`UPDATE sms_settings SET configured=true,enabled=true,provider='generic_http_json',endpoint='https://sms.example.test',token_encrypted=$1`, sms)
	exec(`UPDATE channel_instances SET enabled=true,config='{"api_url":"https://payment.example.test","pid":"fixture-merchant","types":["alipay"]}',secret_ciphertext=$1,secret_aad='zjmfgo:payment-settings:epay-key:v1' WHERE id='payment-default'`, []byte(pay))
	exec(`UPDATE channel_instances SET enabled=true,config='{"host":"smtp.example.test","port":587,"username":"fixture-user","from":"notification@example.test","tls_mode":"starttls","from_name":"Fixture"}',secret_ciphertext=$1,secret_aad='zjmfgo:smtp-settings:password:v1' WHERE id='mail-smtp-default'`, mail)
	exec(`UPDATE channel_instances SET enabled=true,config='{"endpoint":"https://sms.example.test"}',secret_ciphertext=$1,secret_aad='sms:token' WHERE id='sms-default'`, sms)
	for _, item := range []struct{ source, id, value string }{
		{"payment-default", "upgrade-payment-extra", "fixture-extra-payment"},
		{"mail-smtp-default", "upgrade-mail-extra", "fixture-extra-mail"},
		{"sms-default", "upgrade-sms-extra", "fixture-extra-sms"},
	} {
		sealed, err := channelSeal(db.ProvisionEncryptionKey, item.id, item.value, true)
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,sort_order,config,secret_ciphertext,secret_aad) SELECT $1,category,implementation_id,$1,enabled,20,config,$2,$3 FROM channel_instances WHERE id=$4`, item.id, sealed, "zjmfgo:channel-instance:v1:"+item.id, item.source)
	}
	if err = db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents,paid_at) VALUES('UPGRADE160-PAID',$1,'manual','paid',12345,now()) RETURNING id`, customer).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	receipt = []byte(`{"fixture":"schema160-paid-receipt"}`)
	sum := sha256.Sum256(receipt)
	exec(`INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,idempotency_key,completed_at,raw_result,channel_instance_id) VALUES('epay_alipay','upgrade160-payment',$1,$2,12345,'succeeded','upgrade160-payment',now(),$3,'payment-default')`, invoice, customer, receipt)
	exec(`INSERT INTO payment_webhook_events(provider,event_id,payload_hash,status) VALUES('epay_alipay','upgrade160-receipt',$1,'processed')`, hex.EncodeToString(sum[:]))
	if err = db.WithTx(ctx, func(tx pgx.Tx) error {
		if err := db.queuePaymentSuccessEmail(ctx, tx, invoice, customer, "UPGRADE160-PAID", "¥123.45", "topup-customer@example.test"); err != nil {
			return err
		}
		return db.queuePaymentSuccessSMS(ctx, tx, invoice, customer, "UPGRADE160-PAID", "¥123.45", "+8613800000000")
	}); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `SELECT (SELECT id FROM email_outbox),(SELECT id FROM sms_outbox)`).Scan(&mailID, &smsID); err != nil {
		t.Fatal(err)
	}
	// Retain non-default attempt/backoff metadata too, rather than proving
	// preservation only for empty queues or freshly reset attempt counters.
	exec(`UPDATE email_outbox SET attempts=2,max_attempts=4,last_error='fixture previous failure'`)
	exec(`UPDATE sms_outbox SET attempts=1,max_attempts=3,last_error='fixture previous failure'`)
	return
}

func assertNotificationUpgradeChecksums(t *testing.T, db *Store) {
	t.Helper()
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	var actual []string
	rows, err := db.Pool.Query(t.Context(), `SELECT version,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, checksum string
		if err = rows.Scan(&name, &checksum); err != nil {
			t.Fatal(err)
		}
		body, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if checksum != hex.EncodeToString(sum[:]) {
			t.Fatalf("wrong applied migration checksum: %s", name)
		}
		actual = append(actual, name)
	}
	if err = rows.Err(); err != nil || !slices.Equal(actual, names) {
		t.Fatal("upgrade must apply the exact embedded migration inventory", err)
	}
}

func assertNotificationUpgradeCredentials(t *testing.T, db *Store) {
	t.Helper()
	for _, fixture := range []struct{ id, expected, key string }{
		{"payment-default", "fixture-payment-key", "key"},
		{"mail-smtp-default", "fixture-smtp-password", "password"},
		{"sms-default", "fixture-sms-token", "token"},
		{"upgrade-payment-extra", "fixture-extra-payment", "key"},
		{"upgrade-mail-extra", "fixture-extra-mail", "password"},
		{"upgrade-sms-extra", "fixture-extra-sms", "token"},
	} {
		rec, err := scanChannelRecord(db.Pool.QueryRow(t.Context(), "SELECT "+channelInstanceColumns+" FROM channel_instances WHERE id=$1", fixture.id))
		if err != nil || rec.item.SchemaVersion != 1 || len(rec.fields) != 0 {
			t.Fatal("legacy instance did not retain schema-1/single-ciphertext storage", fixture.id, err)
		}
		// Resolve the production single-credential compatibility reader without
		// registering fake SMTP/SMS providers in this store-only test binary.
		secrets, err := db.openChannelSecrets(rec, channel.Descriptor{Fields: []channel.Field{{Key: fixture.key, Secret: true}}})
		if err != nil || secrets[fixture.key] != fixture.expected {
			t.Fatal("legacy credential cannot be decoded after upgrade", fixture.id, err)
		}
		forged := rec
		forged.item.ID = "upgrade-wrong-owner"
		if _, err = db.openChannelSingle(forged); err == nil {
			t.Fatal("credential accepted under a different instance identity", fixture.id)
		}
	}
	legacy, err := db.PaymentConfigurationForInstance(t.Context(), "payment-default", PaymentConfiguration{})
	if err != nil || legacy.Key != "fixture-payment-key" || !legacy.Enabled {
		t.Fatal("legacy payment configuration no longer resolves", err)
	}
	// New multi-credential AAD binds both instance ID and field name. Neither
	// moving the ciphertext nor relabeling bot_token as webhook_secret works.
	desc := channel.Descriptor{Fields: []channel.Field{{Key: "bot_token", Secret: true}, {Key: "webhook_secret", Secret: true}}}
	sealed, err := secure.Seal(db.ProvisionEncryptionKey, credentialAAD("upgrade-bot", "bot_token"), []byte("fixture-only"))
	if err != nil {
		t.Fatal(err)
	}
	rec := channelRecord{item: ChannelInstance{ID: "upgrade-bot"}, fields: map[string][]byte{"bot_token": sealed}}
	if values, err := db.openChannelSecrets(rec, desc); err != nil || values["bot_token"] != "fixture-only" {
		t.Fatal("new credential AAD cannot decode its own value", err)
	}
	rec.item.ID = "upgrade-other-bot"
	if _, err = db.openChannelSecrets(rec, desc); err == nil {
		t.Fatal("named credential accepted for another instance")
	}
	rec.item.ID = "upgrade-bot"
	rec.fields = map[string][]byte{"webhook_secret": sealed}
	if _, err = db.openChannelSecrets(rec, desc); err == nil {
		t.Fatal("named credential accepted for another field")
	}
}

// CI can supply the same candidate executables used for manifest comparison.
// Absence means method-level CheckSchema evidence only; the explicit required
// flag fails closed so candidate acceptance cannot silently omit this check.
func assertNotificationUpgradeBinaryGate(t *testing.T, db *Store, schema string) {
	t.Helper()
	api := os.Getenv("ZJMFGO_UPGRADE_API_BINARY")
	worker := os.Getenv("ZJMFGO_UPGRADE_WORKER_BINARY")
	if api == "" && worker == "" && os.Getenv("ZJMFGO_UPGRADE_BINARY_ACCEPTANCE") != "1" {
		t.Log("candidate binary paths absent: schema gate evidence covers Store.CheckSchema only")
		return
	}
	poolConfig := db.Pool.Config()
	targetSchema := poolConfig.ConnConfig.RuntimeParams["search_path"]
	// ConnString retains the original parsed DSN, so a later RuntimeParams
	// mutation in the isolation helper is not serialized automatically.
	parsed, err := url.Parse(poolConfig.ConnString())
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" || !strings.HasPrefix(targetSchema, "order_refund_upgrade_") {
		t.Fatal("candidate gate requires its parent's isolated PostgreSQL URL/schema")
	}
	params := parsed.Query()
	params.Del("options")
	params.Set("search_path", targetSchema)
	params.Set("application_name", "notification-upgrade-candidate-"+schema)
	parsed.RawQuery = params.Encode()
	candidateDSN := parsed.String()
	candidateConfig, err := pgxpool.ParseConfig(candidateDSN)
	if err != nil || candidateConfig.ConnConfig.RuntimeParams["search_path"] != targetSchema || candidateConfig.ConnConfig.Database != poolConfig.ConnConfig.Database || candidateConfig.ConnConfig.Host != poolConfig.ConnConfig.Host || candidateConfig.ConnConfig.Port != poolConfig.ConnConfig.Port {
		t.Fatal("candidate DSN differs from parent isolation target")
	}
	// Prove this exact child environment DSN resolves the expected random
	// schema and migration checkpoint, rather than an empty public schema
	// which would coincidentally produce the same startup refusal message.
	target, err := Open(t.Context(), candidateDSN)
	if err != nil {
		t.Fatal("candidate isolated database target is unavailable")
	}
	var actualSchema, actualDatabase, actualMigration string
	err = target.Pool.QueryRow(t.Context(), `SELECT current_schema(),current_database(),max(version) FROM schema_migrations`).Scan(&actualSchema, &actualDatabase, &actualMigration)
	target.Close()
	if err != nil || actualSchema != targetSchema || actualDatabase != poolConfig.ConnConfig.Database || !strings.HasPrefix(actualMigration, schema+"_") {
		t.Fatal("candidate environment DSN does not reach the expected parent-owned schema checkpoint")
	}
	for _, candidate := range []struct{ name, path string }{{"api", api}, {"worker", worker}} {
		info, err := os.Stat(candidate.path)
		if !filepath.IsAbs(candidate.path) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			t.Fatal("candidate executable must be an existing absolute executable path", candidate.name)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		cmd := exec.CommandContext(ctx, candidate.path)
		cmd.Dir = t.TempDir()
		// Never inherit deployment endpoints or credentials. Startup happens
		// before fixture seeding, so even a broken guard has no work to send.
		cmd.Env = []string{"DATABASE_URL=" + candidateDSN, "HTTP_ADDR=127.0.0.1:0", "PUBLIC_ORIGIN=http://127.0.0.1:3088", "PAYMENT_MOCK_ENABLED=false"}
		out, runErr := cmd.CombinedOutput()
		cancel()
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) || exit.ExitCode() != 1 || ctx.Err() == context.DeadlineExceeded {
			t.Fatal("candidate did not refuse old schema with exit 1", candidate.name, schema)
		}
		refused := false
		for _, line := range bytes.Split(out, []byte{'\n'}) {
			var entry struct {
				Message string `json:"msg"`
				Error   string `json:"error"`
			}
			if json.Unmarshal(line, &entry) == nil && entry.Message == "schema invalid" && strings.Contains(entry.Error, "schema unavailable") {
				refused = true
			}
		}
		if !refused {
			// Keep output private: a future regression must not echo a DSN or
			// credentials just because startup emitted a different error.
			t.Fatal("candidate failed for a reason other than the old schema guard", candidate.name, schema)
		}
		t.Logf("PASS actual candidate %s startup refuses schema %s before worker recovery/listen", candidate.name, schema)
	}
}

func TestNotificationCenterUpgradeFrom160(t *testing.T) {
	t.Run("legacy_data_and_incremental_migrate", func(t *testing.T) {
		db := notificationSchema160(t)
		ctx := t.Context()
		assertNotificationUpgradeBinaryGate(t, db, "160")
		customer, invoice, mailID, smsID, receipt := seedNotificationUpgrade160(t, db)
		before := notificationUpgradeSnapshot(t, db, false)
		if err := db.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "schema unavailable") {
			t.Fatal("current API/worker schema gate accepted schema 160", err)
		}
		// A modified old migration must abort before adding either new schema.
		if _, err := db.Pool.Exec(ctx, `UPDATE schema_migrations SET checksum=repeat('0',64) WHERE version=$1`, notificationUpgrade160); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx); err == nil || !strings.Contains(err.Error(), notificationUpgrade160+" checksum mismatch") {
			t.Fatal("upgrade accepted a modified historical migration", err)
		}
		var boundary string
		if err := db.Pool.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&boundary); err != nil || boundary != notificationUpgrade160 {
			t.Fatal("refused upgrade partially applied new migrations", boundary, err)
		}
		body, err := migrations.Files.ReadFile(notificationUpgrade160)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if _, err = db.Pool.Exec(ctx, `UPDATE schema_migrations SET checksum=$1 WHERE version=$2`, hex.EncodeToString(sum[:]), notificationUpgrade160); err != nil {
			t.Fatal(err)
		}
		if err = db.Migrate(ctx); err != nil {
			t.Fatal("production 160 to current migration failed", err)
		}
		if err = db.CheckSchema(ctx); err != nil {
			t.Fatal("current schema gate rejected upgraded database", err)
		}
		assertNotificationUpgradeChecksums(t, db)
		if notificationUpgradeSnapshot(t, db, false) != before {
			t.Fatal("upgrade changed old settings, ciphertexts, payment facts or pending queues")
		}
		assertNotificationUpgradeCredentials(t, db)
		prefs, err := db.NotificationPreferences(ctx, customer)
		if err != nil || len(prefs) != 2 {
			t.Fatal("new preferences unavailable", err)
		}
		for _, p := range prefs {
			if p.Enabled || p.Version != 0 {
				t.Fatal("upgrade silently opted an existing customer in")
			}
		}
		var routes, enabled, events, deliveries, attempts, storedPreferences int
		if err = db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM notification_routes),(SELECT count(*) FROM notification_routes WHERE enabled),(SELECT count(*) FROM notification_events),(SELECT count(*) FROM notification_deliveries),(SELECT count(*) FROM notification_delivery_attempts),(SELECT count(*) FROM user_notification_preferences)`).Scan(&routes, &enabled, &events, &deliveries, &attempts, &storedPreferences); err != nil || routes != 2*len(NotificationEventCatalog()) || enabled+events+deliveries+attempts+storedPreferences != 0 {
			t.Fatal("upgrade enabled routes, invented consent or backfilled historical notifications", err)
		}
		upgraded := notificationUpgradeSnapshot(t, db, true)
		if err = db.Migrate(ctx); err != nil || notificationUpgradeSnapshot(t, db, true) != upgraded {
			t.Fatal("repeat production migration changed schema data or queue identities", err)
		}
		if err = db.WithTx(ctx, func(tx pgx.Tx) error {
			if _, err := db.UpdateNotificationPreference(ctx, tx, customer, "inbox", true, 0); err != nil {
				return err
			}
			_, err := db.UpdateNotificationRoute(ctx, tx, "invoice-paid-inbox", NotificationRouteInput{Enabled: true, ExpectedVersion: 1})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		// Enabling a new route must not make a previously settled invoice new.
		for _, eventID := range []string{"upgrade160-receipt", "upgrade160-new-delivery-of-old-payment"} {
			result, err := db.ProcessPaymentWebhook(ctx, "epay_alipay", eventID, "upgrade160-payment", invoice, 12345, "succeeded", receipt)
			if err != nil || !result.Replayed {
				t.Fatal("historical successful payment was not recognized on replay", err)
			}
		}
		if err = db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if job, err := db.ClaimNotificationJob(ctx); !errors.Is(err, pgx.ErrNoRows) || job != nil {
			t.Fatal("historical paid invoice entered new notification worker", err)
		}
		if err = db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM notification_events),(SELECT count(*) FROM notification_deliveries),(SELECT count(*) FROM admin_inbox_messages)`).Scan(&events, &deliveries, &attempts); err != nil || events+deliveries+attempts != 0 {
			t.Fatal("payment replay or route enablement backfilled old invoices", err)
		}
		// The actual old workers can still claim these exact pre-upgrade IDs
		// and decode the old encrypted body, preserving attempts and instance.
		mail, err := db.ClaimEmailJob(ctx)
		if err != nil || mail.ID != mailID || mail.Attempts != 3 || mail.Kind != "payment_success" || mail.ChannelInstanceID != "mail-smtp-default" || !strings.Contains(mail.Body, "UPGRADE160-PAID") {
			t.Fatal("pre-upgrade email job no longer claimable with its original identity", err)
		}
		sms, err := db.ClaimSMSJob(ctx)
		if err != nil || sms.ID != smsID || sms.Attempts != 2 || sms.Kind != "payment_success" || sms.ChannelInstanceID != "sms-default" || !strings.Contains(sms.Body, "UPGRADE160-PAID") {
			t.Fatal("pre-upgrade encrypted SMS job no longer claimable with its original identity", err)
		}
		t.Log("PASS actual 160 to current upgrade; unchanged legacy ciphertext/settings/receipts/pending IDs; checksum refusal and idempotent Migrate; disabled new routes/consent; old payment replay without backfill; original jobs claimable without external delivery")
	})
	t.Run("schema161_is_not_current", func(t *testing.T) {
		db := notificationSchema160(t)
		applyNotificationUpgradeMigration(t, db, notificationUpgrade161)
		assertNotificationUpgradeBinaryGate(t, db, "161")
		if err := db.CheckSchema(t.Context()); err == nil || !strings.Contains(err.Error(), "schema unavailable") {
			t.Fatal("current API/worker schema gate accepted schema 161", err)
		}
		if err := db.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := db.CheckSchema(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertNotificationUpgradeChecksums(t, db)
	})
}

func TestNotificationLifecycleUpgradeFrom162(t *testing.T) {
	db := notificationSchema160(t)
	ctx := t.Context()
	customer, invoice, _, _, _ := seedNotificationUpgrade160(t, db)
	applyNotificationUpgradeMigration(t, db, notificationUpgrade161)
	applyNotificationUpgradeMigration(t, db, notificationUpgrade162)
	assertNotificationUpgradeBinaryGate(t, db, "162")
	if _, err := db.Pool.Exec(ctx, `UPDATE notification_routes SET enabled=true,version=7 WHERE id='invoice-paid-inbox'; UPDATE notification_message_templates SET subject='旧版付款主题',body='旧版账单 {{invoice_no}}',version=5 WHERE id='invoice-paid-inbox'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO user_notification_preferences(user_id,channel,enabled,version) VALUES($1,'inbox',true,4)`, customer); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `WITH e AS(INSERT INTO notification_events(kind,dedupe_key,user_id,invoice_id,payload) VALUES('invoice.paid','invoice.paid:'||$1::bigint::text,$2::bigint,$1::bigint,'{"invoice_no":"UPGRADE160-PAID","amount":"¥123.45"}') RETURNING id) INSERT INTO notification_deliveries(event_id,route_key,route_id,route_version,template_version,preference_version,channel,target,subject,body) SELECT id,'invoice-paid-inbox','invoice-paid-inbox',7,5,4,'inbox',$2::bigint::text,'升级前待发主题','升级前待发内容' FROM e`, invoice, customer); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.Pool.QueryRow(ctx, `SELECT jsonb_build_object('route',(SELECT to_jsonb(r) FROM notification_routes r WHERE id='invoice-paid-inbox'),'template',(SELECT to_jsonb(m) FROM notification_message_templates m WHERE id='invoice-paid-inbox'),'deliveries',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM notification_deliveries d))::text`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var after, scope string
	var enabled bool
	var version, enabledRoutes, events int
	if err := db.Pool.QueryRow(ctx, `SELECT jsonb_build_object('route',(SELECT to_jsonb(r) FROM notification_routes r WHERE id='invoice-paid-inbox'),'template',(SELECT to_jsonb(m) FROM notification_message_templates m WHERE id='invoice-paid-inbox'),'deliveries',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM notification_deliveries d))::text`).Scan(&after); err != nil || before != after {
		t.Fatal("lifecycle migration altered existing route, template or pending delivery", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT event_scope,enabled,version FROM user_notification_preferences WHERE user_id=$1 AND channel='inbox'`, customer).Scan(&scope, &enabled, &version); err != nil || scope != "invoice.paid" || !enabled || version != 4 {
		t.Fatal("upgrade widened or reset existing payment-only consent", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM notification_routes WHERE enabled),(SELECT count(*) FROM notification_events)`).Scan(&enabledRoutes, &events); err != nil || enabledRoutes != 1 || events != 1 {
		t.Fatal("migration enabled new routes or invented historical events", err)
	}
	upgraded := notificationUpgradeSnapshot(t, db, true)
	if err := db.Migrate(ctx); err != nil || notificationUpgradeSnapshot(t, db, true) != upgraded {
		t.Fatal("repeated lifecycle migration changed existing data", err)
	}
	if job, err := db.ClaimNotificationJob(ctx); !errors.Is(err, pgx.ErrNoRows) || job != nil {
		t.Fatal("inbox-only upgraded queue returned an external job", err)
	}
	var messages int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM admin_inbox_messages WHERE user_id=$1 AND subject='升级前待发主题' AND body='升级前待发内容'`, customer).Scan(&messages); err != nil || messages != 1 {
		t.Fatal("pre-upgrade payment-only delivery no longer accepted", messages, err)
	}
	assertNotificationUpgradeChecksums(t, db)
}
