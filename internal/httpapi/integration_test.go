package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

func testServer(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	dsn := os.Getenv("ZJMFGO_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("REQUIRE_INTEGRATION") == "1" {
			t.Fatal("ZJMFGO_TEST_DATABASE_URL required")
		}
		t.Skip("requires isolated PostgreSQL 18 test database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatal("refusing database without _test suffix")
	}
	ctx := context.Background()
	base, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 8)
	rand.Read(random)
	schema := "zjmfgo_test_" + hex.EncodeToString(random)
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		base.Close()
		t.Fatal(err)
	}
	scoped := cfg.Copy()
	scoped.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, scoped)
	if err != nil {
		t.Fatal(err)
	}
	db := &store.Store{Pool: pool}
	t.Cleanup(func() { db.Close(); base.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); base.Close() })
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	if err = db.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE site_settings SET email_verification_required=false"); err != nil {
		t.Fatal(err)
	}
	db.ProvisionEncryptionKey = []byte("01234567890123456789012345678901")
	return db, New(db, config.Config{PublicOrigin: "http://localhost:3088", SMTPHost: "localhost", SMTPPort: 587, SMTPFrom: "test@example.test", ProvisionEncryptionKey: db.ProvisionEncryptionKey, PaymentMockEnabled: true, PaymentGatewaySecret: "integration-payment-secret", MFAEncryptionKey: []byte("01234567890123456789012345678901")}, slog.New(slog.NewTextHandler(os.Stderr, nil))).Handler()
}
func request(h http.Handler, method, path string, body any, cookie string, key string) *httptest.ResponseRecorder {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(payload))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Requested-With", "zjmfgo")
	r.Header.Set("Origin", "http://localhost:3088")
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func requestBearer(h http.Handler, method, path string, body any, secret string) *httptest.ResponseRecorder {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(payload))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func multipartRequest(h http.Handler, path, cookie, filename string, content []byte) *httptest.ResponseRecorder {
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		panic(err)
	}
	if _, err = part.Write(content); err != nil {
		panic(err)
	}
	if err = writer.Close(); err != nil {
		panic(err)
	}
	r := httptest.NewRequest("POST", path, &payload)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("X-Requested-With", "zjmfgo")
	r.Header.Set("Origin", "http://localhost:3088")
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func signedWebhook(h http.Handler, body []byte, signature string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/v1/payments/mock/webhook", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Payment-Signature", signature)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func paymentSignature(body []byte) string {
	m := hmac.New(sha256.New, []byte("integration-payment-secret"))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}
func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d; %s", w.Code, status, w.Body.String())
	}
}
func data(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Data
}
func listData(t *testing.T, w *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Data
}
func user(t *testing.T, db *store.Store, h http.Handler, name, role string) (int64, string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("integration-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	var roleID *int64
	if role == "admin" {
		permissions := []string{}
		for _, p := range store.AdminPermissions {
			permissions = append(permissions, p.Code)
		}
		var assigned int64
		if err = db.Pool.QueryRow(context.Background(), "INSERT INTO admin_roles(name,permissions) VALUES($1,$2) RETURNING id", name+"-role", permissions).Scan(&assigned); err != nil {
			t.Fatal(err)
		}
		roleID = &assigned
	}
	if err = db.Pool.QueryRow(context.Background(), "INSERT INTO users(username,email,password_hash,display_name,role,admin_role_id) VALUES($1,$2,$3,$1,$4,$5) RETURNING id", name, name+"@example.test", string(hash), role, roleID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(context.Background(), "INSERT INTO carts(user_id) VALUES($1)", id); err != nil {
		t.Fatal(err)
	}
	if role == "admin" {
		if _, err = db.Pool.Exec(context.Background(), "UPDATE users SET ticket_scope='all',customer_scope='all' WHERE id=$1", id); err != nil {
			t.Fatal(err)
		}
	}
	w := request(h, "POST", "/api/v1/auth/login", map[string]string{"username": name, "password": "integration-password"}, "", "")
	expect(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("session cookie flags incorrect")
	}
	return id, cookies[0].Name + "=" + cookies[0].Value
}
func product(t *testing.T, db *store.Store, stock int) int64 {
	t.Helper()
	var id int64
	if err := db.Pool.QueryRow(context.Background(), "INSERT INTO products(name,slug,price_cents,setup_fee_cents,stock,provision_mode) VALUES('商品','test-'||gen_random_uuid()::text,1234,99,$1,'simulation') RETURNING id", stock).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func cart(t *testing.T, h http.Handler, cookie string) (int64, int64) {
	t.Helper()
	w := request(h, "GET", "/api/v1/cart", nil, cookie, "")
	expect(t, w, 200)
	d := data(t, w)
	items := d["items"].([]any)
	var id int64
	if len(items) > 0 {
		id = int64(items[0].(map[string]any)["id"].(float64))
	}
	return int64(d["version"].(float64)), id
}
func add(t *testing.T, h http.Handler, cookie string, pid int64, qty int) (int64, int64) {
	t.Helper()
	v, _ := cart(t, h, cookie)
	w := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": v, "product_id": pid, "quantity": qty}, cookie, "")
	expect(t, w, 200)
	return cart(t, h, cookie)
}

func TestAuthenticationOwnershipAndCSRF(t *testing.T) {
	db, h := testServer(t)
	_, first := user(t, db, h, "first", "customer")
	secondID, second := user(t, db, h, "second", "customer")
	adminID, admin := user(t, db, h, "admin", "super_admin")
	expect(t, request(h, "GET", "/api/v1/admin/dashboard", nil, first, ""), 403)
	expect(t, request(h, "GET", "/api/v1/admin/dashboard", nil, admin, ""), 200)
	w := request(h, "POST", "/api/v1/tickets", map[string]string{"subject": "ownership", "body": "hello", "priority": "normal"}, first, "")
	expect(t, w, 201)
	id := int64(data(t, w)["id"].(float64))
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/tickets/%d", id), nil, second, ""), 404)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/tickets/%d/replies", id), map[string]any{"version": 1, "body": "intrusion"}, second, ""), 404)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/tickets/%d", id), nil, admin, ""), 200)
	r := httptest.NewRequest("POST", "/api/v1/auth/logout", strings.NewReader("{}"))
	r.Header.Set("Cookie", first)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	expect(t, w, 403)
	r = httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader("{}"))
	r.Header.Set("X-Requested-With", "zjmfgo")
	r.Header.Set("Origin", "http://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	expect(t, w, 403)
	expect(t, request(h, "POST", "/api/v1/auth/logout", map[string]any{}, first, ""), 200)
	expect(t, request(h, "GET", "/api/v1/me", nil, first, ""), 401)
	if _, err := db.Pool.Exec(context.Background(), "UPDATE users SET password_reset_required=true WHERE id=$1", secondID); err != nil {
		t.Fatal(err)
	}
	resetLogin := request(h, "POST", "/api/v1/auth/login", map[string]string{"username": "second", "password": "integration-password"}, "", "")
	expect(t, resetLogin, 403)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/password", secondID), map[string]string{"password": "new-integration-password"}, admin, ""), 200)
	expect(t, request(h, "GET", "/api/v1/me", nil, second, ""), 401)
	newLogin := request(h, "POST", "/api/v1/auth/login", map[string]string{"username": "second", "password": "new-integration-password"}, "", "")
	expect(t, newLogin, 200)
	newCookies := newLogin.Result().Cookies()
	if len(newCookies) != 1 {
		t.Fatal("new login did not return a session")
	}
	newCookie := newCookies[0].Name + "=" + newCookies[0].Value
	secondLogin := request(h, "POST", "/api/v1/auth/login", map[string]string{"username": "second", "password": "new-integration-password"}, "", "")
	expect(t, secondLogin, 200)
	secondCookies := secondLogin.Result().Cookies()
	if len(secondCookies) != 1 {
		t.Fatal("second login did not return a session")
	}
	otherCookie := secondCookies[0].Name + "=" + secondCookies[0].Value
	expect(t, request(h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "new-integration-password", "new_password": "changed-integration-password"}, newCookie, ""), 200)
	expect(t, request(h, "GET", "/api/v1/me", nil, otherCookie, ""), 401)
	changedLogin := request(h, "POST", "/api/v1/auth/login", map[string]string{"username": "second", "password": "changed-integration-password"}, "", "")
	expect(t, changedLogin, 200)
	var passwordAudits int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action IN ('customer.password_reset','customer.password_changed') AND entity_id=$1", fmt.Sprint(secondID)).Scan(&passwordAudits); err != nil || passwordAudits != 2 {
		t.Fatalf("password reset audit count=%d err=%v", passwordAudits, err)
	}
	if _, err := db.Pool.Exec(context.Background(), "UPDATE users SET status='disabled' WHERE id=$1", adminID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/dashboard", nil, admin, ""), 401)
}

func TestPasswordResetUsesOneTimeEmailCode(t *testing.T) {
	db, h := testServer(t)
	customerID, cookie := user(t, db, h, "password-reset-customer", "customer")
	expect(t, request(h, "POST", "/api/v1/auth/password-reset/request", map[string]string{"identifier": "unknown@example.test"}, "", ""), 202)
	issued := request(h, "POST", "/api/v1/auth/password-reset/request", map[string]string{"identifier": "password-reset-customer"}, "", "")
	expect(t, issued, 202)
	challenge := data(t, issued)["challenge_id"].(string)
	code := verificationEmailCode(t, db, challenge)
	expect(t, request(h, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"challenge_id": challenge, "code": code, "password": "new-reset-password"}, "", ""), 200)
	expect(t, request(h, "GET", "/api/v1/me", nil, cookie, ""), 401)
	login := request(h, "POST", "/api/v1/auth/login", map[string]string{"username": "password-reset-customer", "password": "new-reset-password"}, "", "")
	expect(t, login, 200)
	expect(t, request(h, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"challenge_id": challenge, "code": code, "password": "another-reset-password"}, "", ""), 409)
	var auditCount int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action LIKE 'auth.password_reset_%'", customerID).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("reset audit count=%d err=%v", auditCount, err)
	}
}

func TestNotificationTemplateAndOutboxAdministration(t *testing.T) {
	db, h := testServer(t)
	_, _ = user(t, db, h, "notification-customer", "customer")
	_, admin := user(t, db, h, "notification-admin", "super_admin")
	templates := request(h, "GET", "/api/v1/admin/notification-templates", nil, admin, "")
	expect(t, templates, 200)
	items := listData(t, templates)
	if len(items) < 4 {
		t.Fatalf("template count=%d", len(items))
	}
	var templateID int64
	for _, item := range items {
		if item["channel"] == "email" && item["kind"] == "password_reset" {
			templateID = int64(item["id"].(float64))
			break
		}
	}
	if templateID == 0 {
		t.Fatal("password reset template missing")
	}
	updated := request(h, "PATCH", fmt.Sprintf("/api/v1/admin/notification-templates/%d", templateID), map[string]any{"subject": "自定义重置主题", "body": "请使用：{{code}}", "active": true}, admin, "")
	expect(t, updated, 200)
	expect(t, request(h, "POST", "/api/v1/auth/password-reset/request", map[string]string{"identifier": "notification-customer"}, "", ""), 202)
	var body, subject string
	var sealed []byte
	if err := db.Pool.QueryRow(context.Background(), "SELECT body,subject,body_encrypted FROM email_outbox WHERE kind='password_reset' ORDER BY id DESC LIMIT 1").Scan(&body, &subject, &sealed); err != nil {
		t.Fatal(err)
	}
	if body != "" || subject != "自定义重置主题" || len(sealed) == 0 {
		t.Fatalf("template was not rendered: subject=%q body=%q", subject, body)
	}
	job, err := db.ClaimEmailJob(context.Background())
	if err != nil || job == nil {
		t.Fatalf("claim email job: %v", err)
	}
	if err = db.CompleteEmailJob(context.Background(), *job, errors.New("smtp unavailable")); err != nil {
		t.Fatal(err)
	}
	outbox := request(h, "GET", "/api/v1/admin/email-outbox?status=pending", nil, admin, "")
	expect(t, outbox, 200)
	outboxRows := listData(t, outbox)
	if len(outboxRows) != 1 || outboxRows[0]["body"] != nil || outboxRows[0]["last_error"] != "smtp unavailable" {
		t.Fatalf("outbox exposure/state invalid: %s", outbox.Body.String())
	}
	retry := request(h, "POST", fmt.Sprintf("/api/v1/admin/email-outbox/%d/retry", job.ID), map[string]any{}, admin, "")
	expect(t, retry, 200)
	var retriedStatus, lastError string
	if err = db.Pool.QueryRow(context.Background(), "SELECT status,last_error FROM email_outbox WHERE id=$1", job.ID).Scan(&retriedStatus, &lastError); err != nil || retriedStatus != "pending" || lastError != "" {
		t.Fatalf("retry state status=%s error=%q err=%v", retriedStatus, lastError, err)
	}
}

func TestEmailOutboxDeadLetterCanBeRetried(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "dead-letter-admin", "super_admin")
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind,max_attempts)
VALUES('dead-letter@example.test','dead letter','payload','system',5)`); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM email_outbox WHERE recipient='dead-letter@example.test'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := db.Pool.Exec(t.Context(), "UPDATE email_outbox SET available_at=now() WHERE id=$1", id); err != nil {
			t.Fatal(err)
		}
		job, err := db.ClaimEmailJob(t.Context())
		if err != nil || job == nil {
			t.Fatalf("claim attempt %d: %v", i+1, err)
		}
		if err = db.CompleteEmailJob(t.Context(), *job, errors.New("fixture delivery failure")); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	var attempts int
	if err := db.Pool.QueryRow(t.Context(), "SELECT status,attempts FROM email_outbox WHERE id=$1", id).Scan(&status, &attempts); err != nil || status != "dead_letter" || attempts != 5 {
		t.Fatalf("dead letter status=%s attempts=%d err=%v", status, attempts, err)
	}
	retry := request(h, "POST", fmt.Sprintf("/api/v1/admin/email-outbox/%d/retry", id), map[string]any{}, admin, "")
	expect(t, retry, 200)
	if err := db.Pool.QueryRow(t.Context(), "SELECT status,attempts FROM email_outbox WHERE id=$1", id).Scan(&status, &attempts); err != nil || status != "pending" || attempts != 0 {
		t.Fatalf("retry status=%s attempts=%d err=%v", status, attempts, err)
	}
}

func TestContactsAndCustomerGroupsOwnership(t *testing.T) {
	db, h := testServer(t)
	firstID, first := user(t, db, h, "contacts-first", "customer")
	secondID, second := user(t, db, h, "contacts-second", "customer")
	_, admin := user(t, db, h, "contacts-admin", "super_admin")
	created := request(h, "POST", "/api/v1/contacts", map[string]any{
		"username": "财务联系人", "email": "contact-owner@example.test", "status": "active",
		"permissions": []string{"invoices.read"}, "invoice_emails": true,
	}, first, "")
	expect(t, created, 201)
	contactID := int64(data(t, created)["id"].(float64))
	list := request(h, "GET", "/api/v1/contacts", nil, first, "")
	expect(t, list, 200)
	if len(listData(t, list)) != 1 {
		t.Fatalf("owner cannot see contact: %s", list.Body.String())
	}
	otherList := request(h, "GET", "/api/v1/contacts", nil, second, "")
	expect(t, otherList, 200)
	if len(listData(t, otherList)) != 0 {
		t.Fatalf("contact leaked to another customer: %s", otherList.Body.String())
	}
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/contacts/%d", contactID), map[string]any{"username": "越权"}, second, ""), 404)
	conflict := request(h, "POST", "/api/v1/contacts", map[string]any{"email": "contacts-first@example.test"}, first, "")
	expect(t, conflict, 409)
	adminList := request(h, "GET", fmt.Sprintf("/api/v1/admin/customers/%d/contacts", firstID), nil, admin, "")
	expect(t, adminList, 200)
	if len(listData(t, adminList)) != 1 {
		t.Fatalf("admin cannot see customer contacts: %s", adminList.Body.String())
	}
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/customers/%d/contacts/%d", firstID, contactID), map[string]any{"status": "disabled"}, admin, ""), 200)
	group := request(h, "POST", "/api/v1/admin/customer-groups", map[string]any{"name": "重点客户", "color": "#0f766e", "discount_percent": 10, "separate_invoices": true}, admin, "")
	expect(t, group, 201)
	groupID := int64(data(t, group)["id"].(float64))
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/customers/%d/group", firstID), map[string]any{"group_id": groupID}, admin, ""), 200)
	customers := request(h, "GET", "/api/v1/admin/customers?q=contacts-first", nil, admin, "")
	expect(t, customers, 200)
	rows := listData(t, customers)
	if len(rows) != 1 || rows[0]["group_name"] != "重点客户" {
		t.Fatalf("customer group assignment missing: %s", customers.Body.String())
	}
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/customer-groups/%d", groupID), nil, admin, ""), 409)
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/customers/%d/group", firstID), map[string]any{"group_id": nil}, admin, ""), 200)
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/customer-groups/%d", groupID), nil, admin, ""), 200)
	_ = secondID
}

func TestAdminCanEditAndDisableCustomer(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "editable-customer", "customer")
	_, admin := user(t, db, h, "editable-admin", "super_admin")
	updated := request(h, "PATCH", fmt.Sprintf("/api/v1/admin/customers/%d", customerID), map[string]string{"display_name": "已更新客户", "email": "updated-customer@example.test", "status": "active"}, admin, "")
	expect(t, updated, 200)
	var name, email, status string
	if err := db.Pool.QueryRow(context.Background(), "SELECT display_name,email,status FROM users WHERE id=$1", customerID).Scan(&name, &email, &status); err != nil {
		t.Fatal(err)
	}
	if name != "已更新客户" || email != "updated-customer@example.test" || status != "active" {
		t.Fatalf("customer update mismatch name=%s email=%s status=%s", name, email, status)
	}
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/customers/%d", customerID), map[string]string{"display_name": "越权修改"}, customer, ""), 403)
	disabled := request(h, "PATCH", fmt.Sprintf("/api/v1/admin/customers/%d", customerID), map[string]string{"status": "disabled"}, admin, "")
	expect(t, disabled, 200)
	expect(t, request(h, "GET", "/api/v1/me", nil, customer, ""), 401)
	var sessions int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM sessions WHERE user_id=$1", customerID).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("disabled customer sessions=%d err=%v", sessions, err)
	}
	reenabled := request(h, "PATCH", fmt.Sprintf("/api/v1/admin/customers/%d", customerID), map[string]string{"status": "active"}, admin, "")
	expect(t, reenabled, 200)
	var auditCount int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action='customer.updated' AND entity_id=$1", fmt.Sprint(customerID)).Scan(&auditCount); err != nil || auditCount != 3 {
		t.Fatalf("customer update audit count=%d err=%v", auditCount, err)
	}
}

func TestAdminCanCreateCustomer(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "creator-admin", "super_admin")
	_, staff := user(t, db, h, "regular-customer", "customer")

	forbidden := request(h, "POST", "/api/v1/admin/customers", map[string]any{
		"username":     "newcust1",
		"email":        "newcust1@example.test",
		"password":     "password123",
		"display_name": "新客户",
	}, staff, "")
	expect(t, forbidden, 403)

	created := request(h, "POST", "/api/v1/admin/customers", map[string]any{
		"username":     "newcust1",
		"email":        "newcust1@example.test",
		"password":     "password123",
		"display_name": "新客户",
		"status":       "active",
		"company_name": "测试网络公司",
		"phone":        "13800138000",
	}, admin, "")
	expect(t, created, 201)

	listed := request(h, "GET", "/api/v1/admin/customers?q=newcust1", nil, admin, "")
	expect(t, listed, 200)
	items := listData(t, listed)
	if len(items) != 1 || items[0]["company_name"] != "测试网络公司" {
		t.Fatalf("created customer list mismatch: %s", listed.Body.String())
	}

	conflict := request(h, "POST", "/api/v1/admin/customers", map[string]any{
		"username":     "newcust1",
		"email":        "newcust1@example.test",
		"password":     "password123",
		"display_name": "重复客户",
	}, admin, "")
	expect(t, conflict, 409)
}

func TestAuthenticationEventsAreAudited(t *testing.T) {
	db, h := testServer(t)
	customerID, cookie := user(t, db, h, "audit-login", "customer")
	failed := request(h, "POST", "/api/v1/auth/login", map[string]string{"username": "audit-login", "password": "wrong-password"}, "", "")
	expect(t, failed, 401)
	expect(t, request(h, "POST", "/api/v1/auth/logout", map[string]any{}, cookie, ""), 200)
	var succeeded, failedCount, loggedOut int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FILTER (WHERE action='auth.login_succeeded'),count(*) FILTER (WHERE action='auth.login_failed'),count(*) FILTER (WHERE action='auth.logout') FROM audit_events WHERE entity_id=$1", fmt.Sprint(customerID)).Scan(&succeeded, &failedCount, &loggedOut); err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 || failedCount != 1 || loggedOut != 1 {
		t.Fatalf("auth audit counts succeeded=%d failed=%d logout=%d", succeeded, failedCount, loggedOut)
	}
}

func TestAPIKeysAuthenticateAndRevoke(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "api-key-owner", "customer")
	_, other := user(t, db, h, "api-key-other", "customer")
	created := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "自动化客户端"}, customer, "")
	expect(t, created, 201)
	createdData := data(t, created)
	secret := createdData["secret"].(string)
	if len(secret) != 71 || createdData["prefix"] == secret {
		t.Fatalf("invalid one-time API key response: %s", created.Body.String())
	}
	var hash string
	if err := db.Pool.QueryRow(context.Background(), "SELECT secret_hash FROM api_keys WHERE id=$1", int64(createdData["id"].(float64))).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash == secret || len(hash) != 64 {
		t.Fatalf("API key secret was stored in plaintext")
	}
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, secret), 200)
	expect(t, requestBearer(h, "GET", "/api/v1/api-keys", nil, secret), 403)
	ticket := requestBearer(h, "POST", "/api/v1/tickets", map[string]string{"subject": "API key ticket", "body": "created through API key", "priority": "normal"}, secret)
	expect(t, ticket, 201)
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/api-keys/%d", int64(createdData["id"].(float64))), map[string]any{}, other, ""), 404)
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/api-keys/%d", int64(createdData["id"].(float64))), map[string]any{}, customer, ""), 200)
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, secret), 401)
	var lastUsed time.Time
	if err := db.Pool.QueryRow(context.Background(), "SELECT last_used_at FROM api_keys WHERE id=$1", int64(createdData["id"].(float64))).Scan(&lastUsed); err != nil {
		t.Fatal(err)
	}
	if lastUsed.IsZero() {
		t.Fatal("API key last_used_at was not recorded")
	}
	var auditCount int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action IN ('api_key.created','api_key.revoked')", customerID).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("API key audit count=%d err=%v", auditCount, err)
	}
}

func TestCheckoutAtomicAndIdempotent(t *testing.T) {
	db, h := testServer(t)
	_, cookie := user(t, db, h, "buyer", "customer")
	pid := product(t, db, 5)
	version, itemID := add(t, h, cookie, pid, 2)
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version}, cookie, "empty-key-1"), 409)
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version - 1, ItemIDs: []int64{itemID}}, cookie, "stale-key-1"), 409)
	input := store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}
	w := request(h, "POST", "/api/v1/orders", input, cookie, "same-key-123")
	expect(t, w, 201)
	if got := data(t, w)["total_cents"]; got != float64(2666) {
		t.Fatalf("price including setup fee: %v", got)
	}
	replay := request(h, "POST", "/api/v1/orders", input, cookie, "same-key-123")
	expect(t, replay, 200)
	if data(t, replay)["replayed"] != true || data(t, replay)["order_id"] != data(t, w)["order_id"] {
		t.Fatal("replay created different order")
	}
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID + 1}}, cookie, "same-key-123"), 409)
	var orders, invoices, items, audits, stock int
	err := db.Pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM orders),(SELECT count(*) FROM invoices),(SELECT count(*) FROM cart_items),(SELECT count(*) FROM audit_events WHERE action='order.created'),(SELECT stock FROM products WHERE id=$1)`, pid).Scan(&orders, &invoices, &items, &audits, &stock)
	if err != nil {
		t.Fatal(err)
	}
	if orders != 1 || invoices != 1 || items != 0 || audits != 1 || stock != 3 {
		t.Fatalf("atomic result: orders=%d invoices=%d cart=%d audits=%d stock=%d", orders, invoices, items, audits, stock)
	}
}

func TestProductPricingMatrixFlowsThroughCartAndOrder(t *testing.T) {
	db, h := testServer(t)
	_, cookie := user(t, db, h, "pricing-buyer", "customer")
	pid := product(t, db, 5)
	if _, err := db.Pool.Exec(context.Background(), "INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents) VALUES($1,'quarterly',3000,50)", pid); err != nil {
		t.Fatal(err)
	}
	cart := request(h, "GET", "/api/v1/cart", nil, cookie, "")
	expect(t, cart, 200)
	version := int64(data(t, cart)["version"].(float64))
	added := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": pid, "billing_cycle": "quarterly", "quantity": 1}, cookie, "")
	expect(t, added, 200)
	cart = request(h, "GET", "/api/v1/cart", nil, cookie, "")
	expect(t, cart, 200)
	items := data(t, cart)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["billing_cycle"] != "quarterly" || items[0].(map[string]any)["total_cents"] != float64(3050) {
		t.Fatalf("quarterly cart pricing mismatch: %s", cart.Body.String())
	}
	checkout := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(data(t, cart)["version"].(float64)), ItemIDs: []int64{int64(items[0].(map[string]any)["id"].(float64))}}, cookie, "pricing-order")
	expect(t, checkout, 201)
	orderID := int64(data(t, checkout)["order_id"].(float64))
	var cycle string
	var price, setup int64
	if err := db.Pool.QueryRow(context.Background(), "SELECT billing_cycle,price_cents,setup_fee_cents FROM order_items WHERE order_id=$1", orderID).Scan(&cycle, &price, &setup); err != nil {
		t.Fatal(err)
	}
	if cycle != "quarterly" || price != 3000 || setup != 50 {
		t.Fatalf("order pricing cycle=%s price=%d setup=%d", cycle, price, setup)
	}
}

func TestAdminCanUpdateProductPricingAndInvalidateCartRevision(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "pricing-admin-customer", "customer")
	_, admin := user(t, db, h, "pricing-admin", "super_admin")
	pid := product(t, db, 5)
	version, itemID := add(t, h, customer, pid, 1)
	update := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/pricing", pid), map[string]any{"billing_cycle": "monthly", "price_cents": 2222, "setup_fee_cents": 11, "default": true}, admin, "pricing-update")
	expect(t, update, 200)
	checkout := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "pricing-stale-order")
	expect(t, checkout, 409)
	if data(t, checkout) != nil {
		t.Fatalf("stale cart unexpectedly accepted: %s", checkout.Body.String())
	}
	detail := request(h, "GET", fmt.Sprintf("/api/v1/catalog/%d", pid), nil, "", "")
	expect(t, detail, 200)
	pricing := data(t, detail)["pricing"].([]any)
	found := false
	for _, raw := range pricing {
		entry := raw.(map[string]any)
		if entry["billing_cycle"] == "monthly" && entry["price_cents"] == float64(2222) {
			found = true
		}
	}
	if !found {
		t.Fatalf("updated pricing not visible: %s", detail.Body.String())
	}
}

func TestProductOptionGroupsValidateAndSnapshotOrder(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "options-customer", "customer")
	_, admin := user(t, db, h, "options-admin", "super_admin")
	pid := product(t, db, 5)
	group := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), map[string]any{
		"name": "系统版本", "slug": "os", "required": true,
		"options": []map[string]any{{"label": "Linux", "value": "linux", "price_delta_cents": 500, "setup_delta_cents": 20}, {"label": "Windows", "value": "windows", "price_delta_cents": 900}},
	}, admin, "options-group")
	expect(t, group, 201)
	var optionID int64
	if err := db.Pool.QueryRow(context.Background(), "SELECT id FROM product_options WHERE group_id=$1 ORDER BY id LIMIT 1", int64(data(t, group)["id"].(float64))).Scan(&optionID); err != nil {
		t.Fatal(err)
	}
	optionalGroup := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), map[string]any{
		"name": "备份", "slug": "backup", "required": false,
		"options": []map[string]any{{"label": "启用", "value": "on", "price_delta_cents": 100}},
	}, admin, "options-optional-group")
	expect(t, optionalGroup, 201)
	var optionalID int64
	if err := db.Pool.QueryRow(context.Background(), "SELECT id FROM product_options WHERE group_id=$1 ORDER BY id LIMIT 1", int64(data(t, optionalGroup)["id"].(float64))).Scan(&optionalID); err != nil {
		t.Fatal(err)
	}
	cart := request(h, "GET", "/api/v1/cart", nil, customer, "")
	expect(t, cart, 200)
	version := int64(data(t, cart)["version"].(float64))
	missing := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": pid, "quantity": 1, "option_ids": []int64{optionalID}}, customer, "")
	expect(t, missing, 409)
	added := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": pid, "quantity": 1, "option_ids": []int64{optionID}}, customer, "")
	expect(t, added, 200)
	cart = request(h, "GET", "/api/v1/cart", nil, customer, "")
	expect(t, cart, 200)
	items := data(t, cart)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["total_cents"] != float64(1853) {
		t.Fatalf("option cart total mismatch: %s", cart.Body.String())
	}
	itemID := int64(items[0].(map[string]any)["id"].(float64))
	version = int64(data(t, cart)["version"].(float64))
	edited := request(h, "PATCH", fmt.Sprintf("/api/v1/cart/items/%d", itemID), map[string]any{"version": version, "quantity": 2, "option_ids": []int64{optionID, optionalID}}, customer, "")
	expect(t, edited, 200)
	cart = request(h, "GET", "/api/v1/cart", nil, customer, "")
	expect(t, cart, 200)
	items = data(t, cart)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["quantity"] != float64(2) || items[0].(map[string]any)["total_cents"] != float64(3906) {
		t.Fatalf("edited option cart mismatch: %s", cart.Body.String())
	}
	checkout := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(data(t, cart)["version"].(float64)), ItemIDs: []int64{itemID}}, customer, "options-order")
	expect(t, checkout, 201)
	orderID := int64(data(t, checkout)["order_id"].(float64))
	var snapshot []byte
	var total int64
	if err := db.Pool.QueryRow(context.Background(), "SELECT config_options,total_cents FROM order_items WHERE order_id=$1", orderID).Scan(&snapshot, &total); err != nil {
		t.Fatal(err)
	}
	if string(snapshot) != fmt.Sprintf("[%d, %d]", optionID, optionalID) || total != 3906 {
		t.Fatalf("option snapshot=%s total=%d", snapshot, total)
	}
	_ = customerID
}

func TestCartKeepsDifferentProductOptionVariantsSeparate(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "variant-customer", "customer")
	_, admin := user(t, db, h, "variant-admin", "super_admin")
	pid := product(t, db, 5)
	group := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), map[string]any{
		"name": "容量", "slug": "size", "required": true,
		"options": []map[string]any{{"label": "小", "value": "small"}, {"label": "大", "value": "large", "price_delta_cents": 200}},
	}, admin, "variant-group")
	expect(t, group, 201)
	var smallID, largeID int64
	groupID := int64(data(t, group)["id"].(float64))
	if err := db.Pool.QueryRow(context.Background(), "SELECT id FROM product_options WHERE group_id=$1 AND value='small'", groupID).Scan(&smallID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(context.Background(), "SELECT id FROM product_options WHERE group_id=$1 AND value='large'", groupID).Scan(&largeID); err != nil {
		t.Fatal(err)
	}
	cart := request(h, "GET", "/api/v1/cart", nil, customer, "")
	expect(t, cart, 200)
	version := int64(data(t, cart)["version"].(float64))
	expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": pid, "quantity": 1, "option_ids": []int64{smallID}}, customer, ""), 200)
	cart = request(h, "GET", "/api/v1/cart", nil, customer, "")
	expect(t, cart, 200)
	version = int64(data(t, cart)["version"].(float64))
	expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": pid, "quantity": 1, "option_ids": []int64{largeID}}, customer, ""), 200)
	cart = request(h, "GET", "/api/v1/cart", nil, customer, "")
	expect(t, cart, 200)
	if len(data(t, cart)["items"].([]any)) != 2 {
		t.Fatalf("option variants collapsed: %s", cart.Body.String())
	}
}

func TestPromoCodeLifecycleAndCheckoutDiscount(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "promo-customer", "customer")
	_, secondCustomer := user(t, db, h, "promo-second", "customer")
	_, admin := user(t, db, h, "promo-admin", "super_admin")
	pid := product(t, db, 5)
	created := request(h, "POST", "/api/v1/admin/promo-codes", map[string]any{
		"code": "welcome10", "discount_type": "percent", "value": 10,
		"max_uses": 1, "once_per_customer": true,
	}, admin, "")
	expect(t, created, 201)
	if data(t, created)["code"] != "WELCOME10" {
		t.Fatalf("promo code was not normalized: %s", created.Body.String())
	}
	version, itemID := add(t, h, customer, pid, 1)
	apply := request(h, "POST", "/api/v1/cart/promo", map[string]any{"version": version, "code": "welcome10"}, customer, "")
	expect(t, apply, 200)
	version = int64(data(t, apply)["version"].(float64))
	cartResponse := request(h, "GET", "/api/v1/cart", nil, customer, "")
	expect(t, cartResponse, 200)
	promo := data(t, cartResponse)["promo"].(map[string]any)
	if promo["code"] != "WELCOME10" || int64(promo["discount_cents"].(float64)) != 133 {
		t.Fatalf("unexpected cart promo quote: %s", cartResponse.Body.String())
	}
	checkout := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "promo-order-1")
	expect(t, checkout, 201)
	result := data(t, checkout)
	if int64(result["discount_cents"].(float64)) != 133 || int64(result["total_cents"].(float64)) != 1200 {
		t.Fatalf("unexpected discounted checkout: %s", checkout.Body.String())
	}
	orderID := int64(result["order_id"].(float64))
	var total, discount int64
	var code string
	if err := db.Pool.QueryRow(context.Background(), "SELECT total_cents,discount_cents,promo_code FROM orders WHERE id=$1", orderID).Scan(&total, &discount, &code); err != nil {
		t.Fatal(err)
	}
	if total != 1200 || discount != 133 || code != "WELCOME10" {
		t.Fatalf("order promo snapshot total=%d discount=%d code=%s", total, discount, code)
	}
	replay := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "promo-order-1")
	expect(t, replay, 200)
	if !data(t, replay)["replayed"].(bool) {
		t.Fatal("promo checkout replay was not marked")
	}
	secondVersion, _ := cart(t, h, secondCustomer)
	applySecond := request(h, "POST", "/api/v1/cart/promo", map[string]any{"version": secondVersion, "code": "WELCOME10"}, secondCustomer, "")
	expect(t, applySecond, 409)
	var used int64
	if err := db.Pool.QueryRow(context.Background(), "SELECT used_count FROM promo_codes WHERE code='WELCOME10'").Scan(&used); err != nil || used != 1 {
		t.Fatalf("promo usage count=%d err=%v", used, err)
	}
}

func TestPublishedContentIsPublicAndDraftIsHidden(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "content-admin", "super_admin")
	draft := request(h, "POST", "/api/v1/admin/content", map[string]any{"content_type": "news", "title": "草稿", "slug": "draft-news", "body": "draft", "status": "draft"}, admin, "")
	expect(t, draft, 201)
	published := request(h, "POST", "/api/v1/admin/content", map[string]any{"content_type": "knowledge", "title": "使用指南", "slug": "getting-started", "excerpt": "摘要", "body": "正文", "status": "published"}, admin, "")
	expect(t, published, 201)
	list := request(h, "GET", "/api/v1/knowledgebase?q=指南", nil, "", "")
	expect(t, list, 200)
	items := data(t, list)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["slug"] != "getting-started" {
		t.Fatalf("published content list mismatch: %s", list.Body.String())
	}
	detail := request(h, "GET", fmt.Sprintf("/api/v1/knowledgebase/%d", int64(data(t, published)["id"].(float64))), nil, "", "")
	expect(t, detail, 200)
	if data(t, detail)["body"] != "正文" {
		t.Fatalf("content detail mismatch: %s", detail.Body.String())
	}
	hidden := request(h, "GET", "/api/v1/news/1", nil, "", "")
	expect(t, hidden, 404)
}

func TestPendingOrderCancellationRestoresStock(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "order-cancel-owner", "customer")
	_, other := user(t, db, h, "order-cancel-other", "customer")
	pid := product(t, db, 2)
	version, itemID := add(t, h, customer, pid, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "pending-cancel-order")
	expect(t, order, 201)
	orderID := int64(data(t, order)["order_id"].(float64))
	cancel := request(h, "POST", fmt.Sprintf("/api/v1/orders/%d/cancel", orderID), map[string]any{}, customer, "pending-cancel-key")
	expect(t, cancel, 201)
	if data(t, cancel)["status"] != "cancelled" || data(t, cancel)["stock_units_released"] != float64(1) {
		t.Fatalf("unexpected cancellation: %s", cancel.Body.String())
	}
	replay := request(h, "POST", fmt.Sprintf("/api/v1/orders/%d/cancel", orderID), map[string]any{}, customer, "pending-cancel-key")
	expect(t, replay, 200)
	if !data(t, replay)["replayed"].(bool) {
		t.Fatal("cancel replay flag missing")
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/orders/%d/cancel", orderID), map[string]any{}, other, "other-cancel-key"), 404)
	var orderStatus, invoiceStatus, serviceStatus string
	var stock, jobs int
	if err := db.Pool.QueryRow(context.Background(), `SELECT o.status,i.status,s.status,p.stock,
(SELECT count(*) FROM provision_jobs j WHERE j.service_id=s.id)
FROM orders o JOIN invoices i ON i.order_id=o.id JOIN services s ON s.order_id=o.id JOIN products p ON p.id=s.product_id WHERE o.id=$1`, orderID).Scan(&orderStatus, &invoiceStatus, &serviceStatus, &stock, &jobs); err != nil {
		t.Fatal(err)
	}
	if orderStatus != "cancelled" || invoiceStatus != "cancelled" || serviceStatus != "cancelled" || stock != 2 || jobs != 0 {
		t.Fatalf("cancelled state order=%s invoice=%s service=%s stock=%d jobs=%d", orderStatus, invoiceStatus, serviceStatus, stock, jobs)
	}
	var auditCount int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action='order.cancelled' AND entity_id=$1", fmt.Sprint(orderID)).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("cancel audit count=%d err=%v", auditCount, err)
	}
	_ = customerID
}

func TestAdminOrderFilterAndBulkCancellation(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "bulk-order-owner", "customer")
	_, admin := user(t, db, h, "bulk-order-admin", "super_admin")
	pid := product(t, db, 4)
	version, itemID := add(t, h, customer, pid, 1)
	first := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "bulk-order-1")
	expect(t, first, 201)
	firstData := data(t, first)
	firstID := int64(firstData["order_id"].(float64))
	version, itemID = add(t, h, customer, pid, 1)
	second := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "bulk-order-2")
	expect(t, second, 201)
	secondID := int64(data(t, second)["order_id"].(float64))
	filtered := request(h, "GET", "/api/v1/admin/orders?status=pending&q=bulk-order-owner", nil, admin, "")
	expect(t, filtered, 200)
	if rows := listData(t, filtered); len(rows) != 2 {
		t.Fatalf("filtered orders=%d, want 2", len(rows))
	}
	bulk := request(h, "POST", "/api/v1/admin/orders/bulk-cancel", map[string]any{"order_ids": []int64{firstID, secondID, 999999999}}, admin, "bulk-cancel-1")
	expect(t, bulk, 200)
	bulkData := data(t, bulk)
	if bulkData["succeeded"] != float64(2) || bulkData["failed"] != float64(1) {
		t.Fatalf("bulk result: %s", bulk.Body.String())
	}
	var cancelled int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM orders WHERE id=ANY($1) AND status='cancelled'", []int64{firstID, secondID}).Scan(&cancelled); err != nil || cancelled != 2 {
		t.Fatalf("cancelled orders=%d err=%v", cancelled, err)
	}
	replay := request(h, "POST", "/api/v1/admin/orders/bulk-cancel", map[string]any{"order_ids": []int64{firstID, secondID, 999999999}}, admin, "bulk-cancel-1")
	expect(t, replay, 200)
	if data(t, replay)["succeeded"] != float64(2) {
		t.Fatalf("bulk replay: %s", replay.Body.String())
	}
}

func TestAdminReportsAggregatePaidCommerceData(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "report-customer", "customer")
	_, admin := user(t, db, h, "report-admin", "super_admin")
	pid := product(t, db, 2)
	version, itemID := add(t, h, customer, pid, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "report-order")
	expect(t, order, 201)
	invoiceID := int64(data(t, order)["invoice_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]any{"amount_cents": 5000}, admin, "report-topup"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "report-payment"), 201)
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60))
	income := request(h, "GET", fmt.Sprintf("/api/v1/admin/reports/income?year=%d", now.Year()), nil, admin, "")
	expect(t, income, 200)
	if total := data(t, income)["total"].(map[string]any)["income_cents"].(float64); total <= 0 {
		t.Fatalf("income total=%v", total)
	}
	daily := request(h, "GET", fmt.Sprintf("/api/v1/admin/reports/new-clients?year=%d&month=%d", now.Year(), int(now.Month())), nil, admin, "")
	expect(t, daily, 200)
	if days := data(t, daily)["days"].([]any); len(days) < 28 {
		t.Fatalf("daily report days=%d", len(days))
	}
	products := request(h, "GET", fmt.Sprintf("/api/v1/admin/reports/product-income?year=%d&month=%d", now.Year(), int(now.Month())), nil, admin, "")
	expect(t, products, 200)
	productRows := data(t, products)["products"].([]any)
	if len(productRows) == 0 {
		t.Fatal("product report is empty")
	}
	ranking := request(h, "GET", "/api/v1/admin/reports/customer-ranking", nil, admin, "")
	expect(t, ranking, 200)
	found := false
	for _, row := range listData(t, ranking) {
		if int64(row["user_id"].(float64)) == customerID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("customer missing from ranking")
	}
}

func TestOrderApprovalRequiredWaitsForAdminReview(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "review-required-customer", "customer")
	_, admin := user(t, db, h, "review-required-admin", "super_admin")
	pid := product(t, db, 2)
	if _, err := db.Pool.Exec(context.Background(), "UPDATE products SET approval_required=true WHERE id=$1", pid); err != nil {
		t.Fatal(err)
	}
	version, itemID := add(t, h, customer, pid, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "review-required-order")
	expect(t, order, 201)
	orderID := int64(data(t, order)["order_id"].(float64))
	invoiceID := int64(data(t, order)["invoice_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]any{"amount_cents": 2000}, admin, "review-required-topup"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "review-required-pay"), 201)
	var review, orderStatus, serviceStatus string
	var jobs int
	if err := db.Pool.QueryRow(context.Background(), `SELECT o.review_status,o.status,s.status,(SELECT count(*) FROM provision_jobs j WHERE j.service_id=s.id)
FROM orders o JOIN services s ON s.order_id=o.id WHERE o.id=$1`, orderID).Scan(&review, &orderStatus, &serviceStatus, &jobs); err != nil {
		t.Fatal(err)
	}
	if review != "pending" || orderStatus != "paid" || serviceStatus != "pending_payment" || jobs != 0 {
		t.Fatalf("before review status=%s order=%s service=%s jobs=%d", review, orderStatus, serviceStatus, jobs)
	}
	detail := request(h, "GET", fmt.Sprintf("/api/v1/admin/orders/%d", orderID), nil, admin, "")
	expect(t, detail, 200)
	if len(data(t, detail)["items"].([]any)) != 1 {
		t.Fatalf("order detail items missing: %s", detail.Body.String())
	}
	approved := request(h, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/review", orderID), map[string]string{"decision": "approved", "note": "资料核验通过"}, admin, "review-required-approve")
	expect(t, approved, 201)
	if data(t, approved)["queued_services"] != float64(1) {
		t.Fatalf("approval did not queue service: %s", approved.Body.String())
	}
	if err := db.Pool.QueryRow(context.Background(), `SELECT o.review_status,o.status,s.status,(SELECT count(*) FROM provision_jobs j WHERE j.service_id=s.id AND j.status='pending')
FROM orders o JOIN services s ON s.order_id=o.id WHERE o.id=$1`, orderID).Scan(&review, &orderStatus, &serviceStatus, &jobs); err != nil {
		t.Fatal(err)
	}
	if review != "approved" || orderStatus != "active" || serviceStatus != "queued" || jobs != 1 {
		t.Fatalf("after review status=%s order=%s service=%s jobs=%d", review, orderStatus, serviceStatus, jobs)
	}
	replay := request(h, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/review", orderID), map[string]string{"decision": "approved", "note": "资料核验通过"}, admin, "review-required-approve")
	expect(t, replay, 200)
	if !data(t, replay)["replayed"].(bool) {
		t.Fatal("review replay flag missing")
	}
	notes := request(h, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/notes", orderID), map[string]string{"notes": "需要保留人工审核凭据"}, admin, "")
	expect(t, notes, 200)
	if data(t, notes)["notes"] != "需要保留人工审核凭据" {
		t.Fatalf("notes not saved: %s", notes.Body.String())
	}
}

func TestConcurrentCheckoutCannotOversell(t *testing.T) {
	db, h := testServer(t)
	_, c1 := user(t, db, h, "one", "customer")
	_, c2 := user(t, db, h, "two", "customer")
	pid := product(t, db, 1)
	v1, i1 := add(t, h, c1, pid, 1)
	v2, i2 := add(t, h, c2, pid, 1)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for idx, entry := range []struct {
		Cookie      string
		Version, ID int64
	}{{c1, v1, i1}, {c2, v2, i2}} {
		wg.Add(1)
		go func(key int, e struct {
			Cookie      string
			Version, ID int64
		}) {
			defer wg.Done()
			results <- request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: e.Version, ItemIDs: []int64{e.ID}}, e.Cookie, fmt.Sprintf("parallel-%d", key)).Code
		}(idx, entry)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for status := range results {
		if status == 201 {
			success++
		} else if status == 409 {
			conflicts++
		} else {
			t.Fatalf("unexpected status %d", status)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("results success=%d conflicts=%d", success, conflicts)
	}
	var stock int
	db.Pool.QueryRow(context.Background(), "SELECT stock FROM products WHERE id=$1", pid).Scan(&stock)
	if stock != 0 {
		t.Fatalf("stock=%d", stock)
	}
}

func TestCheckoutRollsBackOnInvoiceFailure(t *testing.T) {
	db, h := testServer(t)
	_, cookie := user(t, db, h, "rollback", "customer")
	pid := product(t, db, 2)
	version, id := add(t, h, cookie, pid, 1)
	_, err := db.Pool.Exec(context.Background(), `CREATE FUNCTION fail_invoice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected invoice failure'; END; $$; CREATE TRIGGER test_fail BEFORE INSERT ON invoices FOR EACH ROW EXECUTE FUNCTION fail_invoice();`)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{id}}, cookie, "rollback-key"), 500)
	var orders, stock, items int
	db.Pool.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM orders),(SELECT stock FROM products WHERE id=$1),(SELECT count(*) FROM cart_items)", pid).Scan(&orders, &stock, &items)
	if orders != 0 || stock != 2 || items != 1 {
		t.Fatalf("rollback failed orders=%d stock=%d items=%d", orders, stock, items)
	}
}

func TestTicketVersionAndClosure(t *testing.T) {
	db, h := testServer(t)
	_, cookie := user(t, db, h, "ticket", "customer")
	w := request(h, "POST", "/api/v1/tickets", map[string]string{"subject": "工单", "body": "正文", "priority": "normal"}, cookie, "")
	expect(t, w, 201)
	id := int64(data(t, w)["id"].(float64))
	path := fmt.Sprintf("/api/v1/tickets/%d", id)
	expect(t, request(h, "POST", path+"/replies", map[string]any{"version": 1, "body": "reply"}, cookie, ""), 200)
	expect(t, request(h, "POST", path+"/replies", map[string]any{"version": 1, "body": "duplicate"}, cookie, ""), 409)
	expect(t, request(h, "PATCH", path, map[string]any{"version": 2}, cookie, ""), 200)
	expect(t, request(h, "POST", path+"/replies", map[string]any{"version": 3, "body": "closed reply"}, cookie, ""), 409)
	w = request(h, "GET", path, nil, cookie, "")
	expect(t, w, 200)
	d := data(t, w)
	if len(d["replies"].([]any)) != 1 || d["status"] != "closed" {
		t.Fatal("invalid ticket state")
	}
}

func TestTicketAttachmentsAreOwnedAndBounded(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "attachment-owner", "customer")
	_, other := user(t, db, h, "attachment-other", "customer")
	_, admin := user(t, db, h, "attachment-admin", "super_admin")
	created := request(h, "POST", "/api/v1/tickets", map[string]string{"subject": "附件工单", "body": "请查看附件", "priority": "normal"}, customer, "")
	expect(t, created, 201)
	ticketID := int64(data(t, created)["id"].(float64))
	path := fmt.Sprintf("/api/v1/tickets/%d", ticketID)
	content := []byte("attachment body\n")
	uploaded := multipartRequest(h, path+"/attachments", customer, "说明.txt", content)
	expect(t, uploaded, 201)
	attachmentID := int64(data(t, uploaded)["id"].(float64))
	if data(t, uploaded)["sha256"] != fmt.Sprintf("%x", sha256.Sum256(content)) {
		t.Fatal("attachment digest was not stored")
	}
	detail := request(h, "GET", path, nil, customer, "")
	expect(t, detail, 200)
	attachments, ok := data(t, detail)["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("unexpected attachment projection: %s", detail.Body.String())
	}
	expect(t, request(h, "GET", fmt.Sprintf("%s/attachments/%d", path, attachmentID), nil, other, ""), 404)
	expect(t, multipartRequest(h, path+"/attachments", other, "intrusion.txt", []byte("no")), 404)
	downloaded := request(h, "GET", fmt.Sprintf("%s/attachments/%d", path, attachmentID), nil, customer, "")
	expect(t, downloaded, 200)
	if downloaded.Body.String() != string(content) || !strings.Contains(downloaded.Header().Get("Content-Disposition"), "filename*=UTF-8''") {
		t.Fatal("attachment download mismatch")
	}
	expect(t, request(h, "GET", fmt.Sprintf("%s/attachments/%d", path, attachmentID), nil, admin, ""), 200)
	expect(t, multipartRequest(h, path+"/attachments", customer, "script.php", []byte("<?php echo 1;")), 415)
	expect(t, request(h, "DELETE", fmt.Sprintf("%s/attachments/%d", path, attachmentID), nil, other, ""), 404)
	expect(t, request(h, "DELETE", fmt.Sprintf("%s/attachments/%d", path, attachmentID), nil, admin, ""), 200)
	expect(t, request(h, "GET", fmt.Sprintf("%s/attachments/%d", path, attachmentID), nil, customer, ""), 404)
	var audits int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE entity_type='ticket_attachment'").Scan(&audits); err != nil || audits != 4 {
		t.Fatalf("attachment audit count=%d err=%v", audits, err)
	}
	tooLarge := make([]byte, maxTicketAttachmentBytes+1)
	expect(t, multipartRequest(h, path+"/attachments", customer, "large.txt", tooLarge), 413)
}

func TestTicketWorkflowNotesReceiveTransferAndMerge(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "ticket-workflow-customer", "customer")
	_, admin := user(t, db, h, "ticket-workflow-admin", "super_admin")
	admin2ID, admin2 := user(t, db, h, "ticket-workflow-admin2", "admin")
	firstDepartment := request(h, "POST", "/api/v1/admin/ticket-departments", map[string]string{"name": "一线支持", "slug": "frontline"}, admin, "")
	expect(t, firstDepartment, 201)
	firstDepartmentID := int64(data(t, firstDepartment)["id"].(float64))
	secondDepartment := request(h, "POST", "/api/v1/admin/ticket-departments", map[string]string{"name": "专家支持", "slug": "experts"}, admin, "")
	expect(t, secondDepartment, 201)
	secondDepartmentID := int64(data(t, secondDepartment)["id"].(float64))
	makeTicket := func(subject string) int64 {
		created := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": subject, "body": "需要处理", "priority": "normal", "department_id": firstDepartmentID}, customer, "")
		expect(t, created, 201)
		return int64(data(t, created)["id"].(float64))
	}
	firstID, secondID := makeTicket("主工单"), makeTicket("待合并工单")
	firstPath := fmt.Sprintf("/api/v1/tickets/%d", firstID)
	secondPath := fmt.Sprintf("/api/v1/tickets/%d", secondID)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/tickets/%d/receive", firstID), map[string]any{}, admin, ""), 200)
	note := request(h, "POST", fmt.Sprintf("/api/v1/admin/tickets/%d/notes", firstID), map[string]string{"body": "内部核验完成"}, admin, "")
	expect(t, note, 201)
	noteID := int64(data(t, note)["id"].(float64))
	notes := request(h, "GET", fmt.Sprintf("/api/v1/admin/tickets/%d/notes", firstID), nil, admin, "")
	expect(t, notes, 200)
	if len(listData(t, notes)) != 1 {
		t.Fatal("ticket note was not listed")
	}
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/tickets/%d/notes/%d", firstID, noteID), nil, admin, ""), 200)
	transferred := request(h, "POST", fmt.Sprintf("/api/v1/admin/tickets/%d/transfer", firstID), map[string]any{"department_id": secondDepartmentID, "assignee_id": admin2ID, "reason": "升级到专家组"}, admin, "")
	expect(t, transferred, 200)
	detail := request(h, "GET", firstPath, nil, customer, "")
	expect(t, detail, 200)
	if data(t, detail)["department_name"] != "专家支持" || data(t, detail)["assignee_name"] != "ticket-workflow-admin2" {
		t.Fatalf("ticket transfer missing: %s", detail.Body.String())
	}
	expect(t, request(h, "POST", secondPath+"/replies", map[string]any{"version": 1, "body": "补充信息"}, customer, ""), 200)
	merged := request(h, "POST", "/api/v1/admin/tickets/merge", map[string]any{"ticket_ids": []int64{firstID, secondID}}, admin, "")
	expect(t, merged, 200)
	if int64(data(t, merged)["canonical_ticket_id"].(float64)) != firstID {
		t.Fatal("unexpected canonical ticket")
	}
	expect(t, request(h, "GET", secondPath, nil, customer, ""), 409)
	detail = request(h, "GET", firstPath, nil, customer, "")
	expect(t, detail, 200)
	if len(data(t, detail)["replies"].([]any)) != 1 {
		t.Fatal("merged reply was not moved")
	}
	_, otherCustomer := user(t, db, h, "ticket-workflow-other", "customer")
	other := request(h, "POST", "/api/v1/tickets", map[string]string{"subject": "其他客户", "body": "不能合并", "priority": "normal"}, otherCustomer, "")
	expect(t, other, 201)
	otherID := int64(data(t, other)["id"].(float64))
	expect(t, request(h, "POST", "/api/v1/admin/tickets/merge", map[string]any{"ticket_ids": []int64{firstID, otherID}}, admin, ""), 409)
	_ = admin2
}

func TestTicketReplyPresetsAreAdminOwned(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "ticket-preset-admin", "super_admin")
	_, customer := user(t, db, h, "ticket-preset-customer", "customer")
	category := request(h, "POST", "/api/v1/admin/ticket-reply-preset-categories", map[string]any{"name": "账单处理", "sort_order": 2}, admin, "")
	expect(t, category, 201)
	categoryID := int64(data(t, category)["id"].(float64))
	categories := request(h, "GET", "/api/v1/admin/ticket-reply-preset-categories", nil, admin, "")
	expect(t, categories, 200)
	if len(listData(t, categories)) != 1 {
		t.Fatal("preset category was not listed")
	}
	preset := request(h, "POST", "/api/v1/admin/ticket-reply-presets", map[string]any{"category_id": categoryID, "title": "付款核验", "body": "请提供付款凭证。"}, admin, "")
	expect(t, preset, 201)
	presetID := int64(data(t, preset)["id"].(float64))
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/ticket-reply-preset-categories/%d", categoryID), nil, admin, ""), 409)
	presets := request(h, "GET", "/api/v1/admin/ticket-reply-presets", nil, admin, "")
	expect(t, presets, 200)
	if len(listData(t, presets)) != 1 {
		t.Fatal("preset was not listed")
	}
	expect(t, request(h, "GET", "/api/v1/admin/ticket-reply-presets", nil, customer, ""), 403)
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/ticket-reply-presets/%d", presetID), map[string]any{"active": false}, admin, ""), 200)
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/ticket-reply-presets/%d", presetID), nil, admin, ""), 200)
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/ticket-reply-preset-categories/%d", categoryID), nil, admin, ""), 200)
	var count int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action LIKE 'ticket.preset%' OR action LIKE 'ticket.preset_category%'").Scan(&count); err != nil || count != 5 {
		t.Fatalf("preset audit count=%d err=%v", count, err)
	}
}

func TestTicketDepartmentsAndAssignment(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "ticket-department-customer", "customer")
	adminID, admin := user(t, db, h, "ticket-department-admin", "super_admin")
	_, otherCustomer := user(t, db, h, "ticket-department-other", "customer")

	created := request(h, "POST", "/api/v1/admin/ticket-departments", map[string]string{"name": "技术支持", "slug": "technical"}, admin, "")
	expect(t, created, 201)
	departmentID := int64(data(t, created)["id"].(float64))
	assigneeList := request(h, "GET", "/api/v1/admin/ticket-assignees", nil, admin, "")
	expect(t, assigneeList, 200)
	assignees := listData(t, assigneeList)
	if len(assignees) != 1 || int64(assignees[0]["id"].(float64)) != adminID {
		t.Fatalf("unexpected assignees: %s", assigneeList.Body.String())
	}
	listed := request(h, "GET", "/api/v1/ticket-departments", nil, customer, "")
	expect(t, listed, 200)
	departments := listData(t, listed)
	if len(departments) != 1 || int64(departments[0]["id"].(float64)) != departmentID {
		t.Fatalf("unexpected departments: %s", listed.Body.String())
	}

	newTicket := request(h, "POST", "/api/v1/tickets", map[string]any{
		"subject":       "需要分派",
		"body":          "请安排处理",
		"priority":      "normal",
		"department_id": departmentID,
	}, customer, "")
	expect(t, newTicket, 201)
	ticketID := int64(data(t, newTicket)["id"].(float64))
	invalidDepartment := request(h, "POST", "/api/v1/tickets", map[string]any{
		"subject":       "无效部门",
		"body":          "不应创建",
		"priority":      "normal",
		"department_id": int64(999999999),
	}, customer, "")
	expect(t, invalidDepartment, 409)

	assigned := request(h, "PATCH", fmt.Sprintf("/api/v1/admin/tickets/%d/assignment", ticketID), map[string]any{
		"department_id": departmentID,
		"assignee_id":   adminID,
	}, admin, "")
	expect(t, assigned, 200)
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/tickets/%d/assignment", ticketID), map[string]any{
		"department_id": departmentID,
		"assignee_id":   adminID,
	}, otherCustomer, ""), 403)

	detail := request(h, "GET", fmt.Sprintf("/api/v1/tickets/%d", ticketID), nil, customer, "")
	expect(t, detail, 200)
	detailData := data(t, detail)
	if int64(detailData["department_id"].(float64)) != departmentID || detailData["department_name"] != "技术支持" || int64(detailData["assignee_id"].(float64)) != adminID || detailData["assignee_name"] != "ticket-department-admin" {
		t.Fatalf("assignment missing from detail: %s", detail.Body.String())
	}
	adminList := request(h, "GET", "/api/v1/admin/tickets", nil, admin, "")
	expect(t, adminList, 200)
	adminTickets := listData(t, adminList)
	if len(adminTickets) != 1 || adminTickets[0]["department_name"] != "技术支持" || adminTickets[0]["assignee_name"] != "ticket-department-admin" {
		t.Fatalf("assignment missing from list: %s", adminList.Body.String())
	}
	var auditCount int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action='ticket.assigned' AND entity_id=$1 AND actor_id=$2", fmt.Sprint(ticketID), adminID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("assignment audit count=%d err=%v", auditCount, err)
	}

	var disabledID int64
	if err := db.Pool.QueryRow(context.Background(), "INSERT INTO ticket_departments(name,slug,active) VALUES('停用部门','disabled',false) RETURNING id").Scan(&disabledID); err != nil {
		t.Fatal(err)
	}
	disabledTicket := request(h, "POST", "/api/v1/tickets", map[string]any{
		"subject":       "停用部门",
		"body":          "不应创建",
		"priority":      "normal",
		"department_id": disabledID,
	}, customer, "")
	expect(t, disabledTicket, 409)
	_ = customerID
}

func TestBalancePaymentAndRefundAreAtomicAndIdempotent(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "finance", "customer")
	_, admin := user(t, db, h, "finance-admin", "super_admin")
	pid := product(t, db, 2)
	version, itemID := add(t, h, customer, pid, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "finance-order")
	expect(t, order, 201)
	invoiceID := int64(data(t, order)["invoice_id"].(float64))

	topup := request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]any{"amount_cents": 2000, "note": "integration"}, admin, "finance-topup")
	expect(t, topup, 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]any{"amount_cents": 2000}, admin, "finance-topup"), 200)

	paid := request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "finance-pay")
	expect(t, paid, 201)
	if data(t, paid)["status"] != "paid" || data(t, paid)["balance_cents"] != float64(667) {
		t.Fatalf("unexpected payment result: %s", paid.Body.String())
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "finance-pay"), 200)
	expect(t, request(h, "GET", "/api/v1/account", nil, customer, ""), 200)

	refund := request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoiceID), map[string]any{}, admin, "finance-refund")
	expect(t, refund, 201)
	if data(t, refund)["status"] != "refunded" || data(t, refund)["balance_cents"] != float64(2000) {
		t.Fatalf("unexpected refund result: %s", refund.Body.String())
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoiceID), map[string]any{}, admin, "finance-refund"), 200)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "finance-pay-again"), 409)
	var stock int
	if err := db.Pool.QueryRow(context.Background(), "SELECT stock FROM products WHERE id=$1", pid).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if stock != 2 {
		t.Fatalf("refund did not restore stock: %d", stock)
	}
}

func TestExternalPaymentIntentAndWebhookAreSignedAndIdempotent(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "gateway-owner", "customer")
	pid := product(t, db, 2)
	version, itemID := add(t, h, customer, pid, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "gateway-order")
	expect(t, order, 201)
	invoiceID := int64(data(t, order)["invoice_id"].(float64))

	intent := request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoiceID), map[string]string{"provider": "mock"}, customer, "gateway-intent")
	expect(t, intent, 201)
	intentData := data(t, intent)
	providerPaymentID := intentData["provider_payment_id"].(string)
	if intentData["status"] != "pending" || intentData["amount_cents"] != float64(1333) {
		t.Fatalf("unexpected payment intent: %s", intent.Body.String())
	}
	replay := request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoiceID), map[string]string{"provider": "mock"}, customer, "gateway-intent")
	expect(t, replay, 200)
	if !data(t, replay)["replayed"].(bool) {
		t.Fatal("payment intent replay flag missing")
	}

	body, _ := json.Marshal(map[string]any{"event_id": "gateway-event-1", "payment_id": providerPaymentID, "invoice_id": invoiceID, "amount_cents": 1333, "status": "succeeded"})
	invalid := signedWebhook(h, body, "bad-signature")
	expect(t, invalid, 401)
	callback := signedWebhook(h, body, paymentSignature(body))
	expect(t, callback, 201)
	if data(t, callback)["status"] != "succeeded" {
		t.Fatalf("unexpected callback: %s", callback.Body.String())
	}
	replayed := signedWebhook(h, body, paymentSignature(body))
	expect(t, replayed, 200)
	if !data(t, replayed)["replayed"].(bool) {
		t.Fatal("webhook replay flag missing")
	}
	mismatchBody, _ := json.Marshal(map[string]any{"event_id": "gateway-event-1", "payment_id": providerPaymentID, "invoice_id": invoiceID, "amount_cents": 999, "status": "succeeded"})
	mismatch := signedWebhook(h, mismatchBody, paymentSignature(mismatchBody))
	expect(t, mismatch, 409)
	var invoiceStatus, orderStatus string
	if err := db.Pool.QueryRow(context.Background(), `SELECT i.status,o.status FROM invoices i JOIN orders o ON o.id=i.order_id WHERE i.id=$1`, invoiceID).Scan(&invoiceStatus, &orderStatus); err != nil {
		t.Fatal(err)
	}
	if invoiceStatus != "paid" || orderStatus != "paid" {
		t.Fatalf("external payment did not settle invoice=%s order=%s", invoiceStatus, orderStatus)
	}
	var jobs int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM provision_jobs j JOIN services s ON s.id=j.service_id WHERE s.order_id=(SELECT order_id FROM invoices WHERE id=$1) AND j.status='pending'", invoiceID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("external payment jobs=%d err=%v", jobs, err)
	}
	_, admin := user(t, db, h, "gateway-admin", "super_admin")
	refund := request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoiceID), map[string]any{}, admin, "gateway-refund")
	expect(t, refund, 409)
	if data(t, refund) != nil {
		t.Fatalf("external refund unexpectedly succeeded: %s", refund.Body.String())
	}
	_ = customerID
}

func TestServiceProvisionQueueOwnershipAndLifecycle(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "service-owner", "customer")
	_, other := user(t, db, h, "service-other", "customer")
	_, admin := user(t, db, h, "service-admin", "super_admin")
	pid := product(t, db, 2)
	version, itemID := add(t, h, customer, pid, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "service-order")
	expect(t, order, 201)
	var serviceID, invoiceID int64
	if err := db.Pool.QueryRow(context.Background(), "SELECT id FROM services WHERE user_id=$1 AND product_id=$2", customerID, pid).Scan(&serviceID); err != nil {
		t.Fatal(err)
	}
	invoiceID = int64(data(t, order)["invoice_id"].(float64))
	var serviceStatus string
	var jobs int
	if err := db.Pool.QueryRow(context.Background(), "SELECT status FROM services WHERE id=$1", serviceID).Scan(&serviceStatus); err != nil {
		t.Fatal(err)
	}
	if serviceStatus != "pending_payment" {
		t.Fatalf("service status before payment: %s", serviceStatus)
	}
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1", serviceID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("unexpected jobs before payment: %d", jobs)
	}

	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]any{"amount_cents": 2000}, admin, "service-topup"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "service-pay"), 201)
	detail := request(h, "GET", fmt.Sprintf("/api/v1/services/%d", serviceID), nil, customer, "")
	expect(t, detail, 200)
	if data(t, detail)["status"] != "queued" {
		t.Fatalf("service not queued after payment: %s", detail.Body.String())
	}
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND status='pending'", serviceID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("pending provision jobs: %d", jobs)
	}
	if _, err := db.Pool.Exec(context.Background(), "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	job, err := db.ClaimProvisionJob(context.Background())
	if err != nil || job == nil {
		t.Fatalf("claim provision job: %v", err)
	}
	if err = db.ProcessProvisionJob(context.Background(), *job); err != nil {
		t.Fatal(err)
	}
	detail = request(h, "GET", fmt.Sprintf("/api/v1/services/%d", serviceID), nil, customer, "")
	expect(t, detail, 200)
	if data(t, detail)["status"] != "active" || data(t, detail)["upstream_id"] != "local-"+fmt.Sprint(serviceID) {
		t.Fatalf("service not active after worker: %s", detail.Body.String())
	}
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/services/%d", serviceID), nil, other, ""), 404)

	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/services/%d/actions", serviceID), map[string]string{"action": "suspend"}, customer, "service-suspend"), 202)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/services/%d/actions", serviceID), map[string]string{"action": "suspend"}, customer, "service-suspend"), 200)
	job, err = db.ClaimProvisionJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ProcessProvisionJob(context.Background(), *job); err != nil {
		t.Fatal(err)
	}
	detail = request(h, "GET", fmt.Sprintf("/api/v1/services/%d", serviceID), nil, customer, "")
	expect(t, detail, 200)
	if data(t, detail)["status"] != "suspended" {
		t.Fatalf("service not suspended: %s", detail.Body.String())
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/services/%d/actions", serviceID), map[string]string{"action": "terminate"}, customer, "service-terminate"), 202)
	job, err = db.ClaimProvisionJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ProcessProvisionJob(context.Background(), *job); err != nil {
		t.Fatal(err)
	}
	detail = request(h, "GET", fmt.Sprintf("/api/v1/services/%d", serviceID), nil, customer, "")
	expect(t, detail, 200)
	if data(t, detail)["status"] != "terminated" {
		t.Fatalf("service not terminated: %s", detail.Body.String())
	}
}

func TestServiceCancellationApprovalAndWithdrawal(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "cancel-owner", "customer")
	_, other := user(t, db, h, "cancel-other", "customer")
	_, admin := user(t, db, h, "cancel-admin", "super_admin")
	pid := product(t, db, 3)
	version, itemID := add(t, h, customer, pid, 2)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "cancel-order")
	expect(t, order, 201)
	invoiceID := int64(data(t, order)["invoice_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]any{"amount_cents": 5000}, admin, "cancel-topup"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "cancel-pay"), 201)
	var serviceIDs []int64
	rows, err := db.Pool.Query(context.Background(), "SELECT id FROM services WHERE user_id=$1 AND product_id=$2 ORDER BY id", customerID, pid)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		serviceIDs = append(serviceIDs, id)
	}
	rows.Close()
	if len(serviceIDs) != 2 {
		t.Fatalf("service count=%d", len(serviceIDs))
	}
	for i := 0; i < 2; i++ {
		job, claimErr := db.ClaimProvisionJob(context.Background())
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		if err = db.ProcessProvisionJob(context.Background(), *job); err != nil {
			t.Fatal(err)
		}
	}
	endRequest := request(h, "POST", fmt.Sprintf("/api/v1/services/%d/cancellation", serviceIDs[0]), map[string]string{"type": "end_of_billing", "reason": "账期结束后不再使用"}, customer, "cancel-end-key")
	expect(t, endRequest, 201)
	replay := request(h, "POST", fmt.Sprintf("/api/v1/services/%d/cancellation", serviceIDs[0]), map[string]string{"type": "end_of_billing", "reason": "账期结束后不再使用"}, customer, "cancel-end-key")
	expect(t, replay, 200)
	if !data(t, replay)["replayed"].(bool) {
		t.Fatal("cancellation replay flag missing")
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/services/%d/cancellation", serviceIDs[0]), map[string]string{"type": "immediate", "reason": "其它"}, other, "cancel-other-key"), 404)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/services/%d/cancellation", serviceIDs[0]), map[string]string{"type": "immediate", "reason": "重复申请"}, customer, "cancel-end-key-2"), 409)
	immediateRequest := request(h, "POST", fmt.Sprintf("/api/v1/services/%d/cancellation", serviceIDs[1]), map[string]string{"type": "immediate", "reason": "立即停止测试资源"}, customer, "cancel-now-key")
	expect(t, immediateRequest, 201)
	var requestID int64
	if err = db.Pool.QueryRow(context.Background(), "SELECT id FROM service_cancellation_requests WHERE service_id=$1", serviceIDs[1]).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	decision := request(h, "POST", fmt.Sprintf("/api/v1/admin/cancellations/%d/decision", requestID), map[string]string{"decision": "approved", "note": "立即取消已审核"}, admin, "")
	expect(t, decision, 200)
	if data(t, decision)["job_id"] == nil {
		t.Fatalf("missing immediate cancellation job: %s", decision.Body.String())
	}
	job, err := db.ClaimProvisionJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ProcessProvisionJob(context.Background(), *job); err != nil {
		t.Fatal(err)
	}
	detail := request(h, "GET", fmt.Sprintf("/api/v1/services/%d", serviceIDs[1]), nil, customer, "")
	expect(t, detail, 200)
	if data(t, detail)["status"] != "terminated" {
		t.Fatalf("immediate cancellation did not terminate: %s", detail.Body.String())
	}
	var endRequestID int64
	if err = db.Pool.QueryRow(context.Background(), "SELECT id FROM service_cancellation_requests WHERE service_id=$1", serviceIDs[0]).Scan(&endRequestID); err != nil {
		t.Fatal(err)
	}
	endDecision := request(h, "POST", fmt.Sprintf("/api/v1/admin/cancellations/%d/decision", endRequestID), map[string]string{"decision": "approved", "note": "账期结束执行"}, admin, "")
	expect(t, endDecision, 200)
	var jobAvailable time.Time
	if err = db.Pool.QueryRow(context.Background(), "SELECT available_at FROM provision_jobs WHERE id=$1", int64(data(t, endDecision)["job_id"].(float64))).Scan(&jobAvailable); err != nil {
		t.Fatal(err)
	}
	if !jobAvailable.After(time.Now()) {
		t.Fatalf("end-of-billing job was immediately available: %s", jobAvailable)
	}
	if err = db.Pool.QueryRow(context.Background(), "UPDATE provision_jobs SET available_at=now() WHERE id=$1 RETURNING id", int64(data(t, endDecision)["job_id"].(float64))).Scan(&job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = db.ClaimProvisionJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ProcessProvisionJob(context.Background(), *job); err != nil {
		t.Fatal(err)
	}
	detail = request(h, "GET", fmt.Sprintf("/api/v1/services/%d", serviceIDs[0]), nil, customer, "")
	expect(t, detail, 200)
	if data(t, detail)["status"] != "terminated" {
		t.Fatalf("end-of-billing cancellation did not terminate: %s", detail.Body.String())
	}
}

func TestServiceRenewalInvoiceAndPayment(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "renew-owner", "customer")
	_, admin := user(t, db, h, "renew-admin", "super_admin")
	pid := product(t, db, 2)
	version, itemID := add(t, h, customer, pid, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "renew-order")
	expect(t, order, 201)
	initialInvoiceID := int64(data(t, order)["invoice_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]any{"amount_cents": 5000}, admin, "renew-topup"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", initialInvoiceID), map[string]any{}, customer, "renew-initial-pay"), 201)
	job, err := db.ClaimProvisionJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ProcessProvisionJob(context.Background(), *job); err != nil {
		t.Fatal(err)
	}
	var serviceID int64
	var dueBefore time.Time
	if err = db.Pool.QueryRow(context.Background(), "SELECT id,next_due_at FROM services WHERE user_id=$1 AND product_id=$2", customerID, pid).Scan(&serviceID, &dueBefore); err != nil {
		t.Fatal(err)
	}
	renew := request(h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", serviceID), map[string]any{}, customer, "renew-service-key")
	expect(t, renew, 201)
	renewInvoiceID := int64(data(t, renew)["invoice_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", serviceID), map[string]any{}, customer, "renew-service-key-2"), 409)
	replay := request(h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", serviceID), map[string]any{}, customer, "renew-service-key")
	expect(t, replay, 200)
	if !data(t, replay)["replayed"].(bool) || int64(data(t, replay)["invoice_id"].(float64)) != renewInvoiceID {
		t.Fatalf("renewal replay mismatch: %s", replay.Body.String())
	}
	var orderID *int64
	var itemServiceID int64
	if err = db.Pool.QueryRow(context.Background(), "SELECT order_id FROM invoices WHERE id=$1", renewInvoiceID).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if orderID != nil {
		t.Fatalf("renewal invoice unexpectedly belongs to order %v", *orderID)
	}
	if err = db.Pool.QueryRow(context.Background(), "SELECT service_id FROM invoice_items WHERE invoice_id=$1 AND item_type='renew'", renewInvoiceID).Scan(&itemServiceID); err != nil || itemServiceID != serviceID {
		t.Fatalf("renewal item service=%d err=%v", itemServiceID, err)
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", renewInvoiceID), map[string]any{}, customer, "renew-pay"), 201)
	var dueAfter time.Time
	var serviceStatus string
	if err = db.Pool.QueryRow(context.Background(), "SELECT next_due_at,status FROM services WHERE id=$1", serviceID).Scan(&dueAfter, &serviceStatus); err != nil {
		t.Fatal(err)
	}
	if !dueAfter.After(dueBefore) || serviceStatus != "active" {
		t.Fatalf("renewal did not extend service due=%s before=%s status=%s", dueAfter, dueBefore, serviceStatus)
	}
	var invoiceStatus string
	if err = db.Pool.QueryRow(context.Background(), "SELECT status FROM invoices WHERE id=$1", renewInvoiceID).Scan(&invoiceStatus); err != nil || invoiceStatus != "paid" {
		t.Fatalf("renewal invoice status=%s err=%v", invoiceStatus, err)
	}
	refund := request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", renewInvoiceID), map[string]any{}, admin, "renew-refund")
	expect(t, refund, 201)
	if data(t, refund)["status"] != "refunded" {
		t.Fatalf("renewal refund result: %s", refund.Body.String())
	}
	var dueRefunded time.Time
	if err = db.Pool.QueryRow(context.Background(), "SELECT next_due_at FROM services WHERE id=$1", serviceID).Scan(&dueRefunded); err != nil {
		t.Fatal(err)
	}
	if dueRefunded.After(dueAfter) || dueRefunded.Before(dueBefore.Add(-time.Minute)) {
		t.Fatalf("renewal refund due time unexpected: %s before=%s after=%s", dueRefunded, dueBefore, dueAfter)
	}
}

func TestAutomaticRenewalUsesBalanceAndIsIdempotent(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "auto-renew-owner", "customer")
	_, admin := user(t, db, h, "auto-renew-admin", "super_admin")
	pid := product(t, db, 2)
	version, itemID := add(t, h, customer, pid, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "auto-renew-order")
	expect(t, order, 201)
	invoiceID := int64(data(t, order)["invoice_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]any{"amount_cents": 5000}, admin, "auto-renew-topup"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "auto-renew-initial-pay"), 201)
	job, err := db.ClaimProvisionJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ProcessProvisionJob(context.Background(), *job); err != nil {
		t.Fatal(err)
	}
	var serviceID int64
	if err = db.Pool.QueryRow(context.Background(), "SELECT id FROM services WHERE user_id=$1 AND product_id=$2", customerID, pid).Scan(&serviceID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/services/%d/auto-renew", serviceID), map[string]bool{"enabled": true}, customer, ""), 200)
	if _, err = db.Pool.Exec(context.Background(), "UPDATE services SET next_due_at=now()+interval '1 hour' WHERE id=$1", serviceID); err != nil {
		t.Fatal(err)
	}
	processed, err := db.ProcessAutoRenewals(context.Background(), 20)
	if err != nil || processed != 1 {
		t.Fatalf("auto renewal processed=%d err=%v", processed, err)
	}
	var paidRenewals, pendingRenewals int
	if err = db.Pool.QueryRow(context.Background(), "SELECT count(*) FILTER (WHERE status='paid'),count(*) FILTER (WHERE status='unpaid') FROM invoices i JOIN invoice_items ii ON ii.invoice_id=i.id WHERE ii.service_id=$1 AND ii.item_type='renew'", serviceID).Scan(&paidRenewals, &pendingRenewals); err != nil {
		t.Fatal(err)
	}
	if paidRenewals != 1 || pendingRenewals != 0 {
		t.Fatalf("auto renewal invoices paid=%d pending=%d", paidRenewals, pendingRenewals)
	}
	processed, err = db.ProcessAutoRenewals(context.Background(), 20)
	if err != nil || processed != 0 {
		t.Fatalf("auto renewal repeated processed=%d err=%v", processed, err)
	}
}

func TestManualDeliveryCreatesOneServicePerUnit(t *testing.T) {
	db, h := testServer(t)
	customerID, customer := user(t, db, h, "manual-owner", "customer")
	_, admin := user(t, db, h, "manual-admin", "super_admin")
	var pid int64
	if err := db.Pool.QueryRow(context.Background(), "INSERT INTO products(name,slug,price_cents,stock) VALUES('人工交付','manual-'||gen_random_uuid()::text,100,5) RETURNING id").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	version, itemID := add(t, h, customer, pid, 2)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "manual-order")
	expect(t, order, 201)
	invoiceID := int64(data(t, order)["invoice_id"].(float64))
	var serviceID int64
	if err := db.Pool.QueryRow(context.Background(), "SELECT id FROM services WHERE user_id=$1 ORDER BY id LIMIT 1", customerID).Scan(&serviceID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM services WHERE order_id=(SELECT order_id FROM invoices WHERE id=$1)", invoiceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("service units=%d, want 2", count)
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]any{"amount_cents": 1000}, admin, "manual-topup"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "manual-pay"), 201)
	job, err := db.ClaimProvisionJob(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ProcessProvisionJob(context.Background(), *job); err != nil {
		t.Fatal(err)
	}
	var status, serviceStatus string
	if err = db.Pool.QueryRow(context.Background(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(context.Background(), "SELECT status FROM services WHERE id=$1", job.ServiceID).Scan(&serviceStatus); err != nil {
		t.Fatal(err)
	}
	if status != "waiting_manual" || serviceStatus != "provisioning" {
		t.Fatalf("manual state job=%s service=%s", status, serviceStatus)
	}
	delivery := request(h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery", job.ServiceID), map[string]any{"job_id": job.ID, "upstream_id": "cloud-vm-42", "username": "root", "note": "人工核验完成"}, admin, "")
	expect(t, delivery, 200)
	if data(t, delivery)["status"] != "succeeded" {
		t.Fatalf("delivery result: %s", delivery.Body.String())
	}
	if err = db.Pool.QueryRow(context.Background(), "SELECT status,upstream_id FROM services WHERE id=$1", job.ServiceID).Scan(&serviceStatus, &status); err != nil {
		t.Fatal(err)
	}
	if serviceStatus != "active" || status != "cloud-vm-42" {
		t.Fatalf("manual service not active: %s %s", serviceStatus, status)
	}
}
