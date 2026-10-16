package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image/png"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
)

func seedCaptcha(t *testing.T, db *store.Store, id, action, answer, nonce string, expires time.Duration) {
	t.Helper()
	a := sha256.Sum256([]byte(answer))
	n := sha256.Sum256([]byte(nonce))
	_, err := db.Pool.Exec(context.Background(), `INSERT INTO auth_captcha_challenges(id,action,answer_hash,nonce_hash,issued_ip,expires_at) VALUES($1,$2,$3,$4,'127.0.0.1',now()+$5::interval)`, tokenHash(id), action, hex.EncodeToString(a[:]), hex.EncodeToString(n[:]), expires.String())
	if err != nil {
		t.Fatal(err)
	}
}

func TestCaptchaDisabledAndPNGContract(t *testing.T) {
	db, h := testServer(t)
	w := request(h, "GET", "/api/v1/auth/captcha?action=login", nil, "", "")
	expect(t, w, 200)
	if data(t, w)["enabled"] != false || len(w.Result().Cookies()) != 0 {
		t.Fatal("disabled captcha changed browser state")
	}
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM auth_captcha_challenges").Scan(&count); err != nil || count != 0 {
		t.Fatal("disabled captcha wrote challenge", err, count)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=true"); err != nil {
		t.Fatal(err)
	}
	w = request(h, "GET", "/api/v1/auth/captcha?action=login", nil, "", "")
	expect(t, w, 200)
	d := data(t, w)
	if d["enabled"] != true || len(d["captcha_id"].(string)) != 64 || len(d["image"].(string)) < 40 {
		t.Fatal("captcha response contract")
	}
	raw, err := base64.StdEncoding.DecodeString(d["image"].(string)[len("data:image/png;base64,"):])
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 190 || img.Bounds().Dy() != 64 {
		t.Fatal("unexpected captcha dimensions", img.Bounds())
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("captcha response headers")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != captchaCookie || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("captcha cookie flags")
	}
	var storedNonce string
	if err := db.Pool.QueryRow(t.Context(), "SELECT nonce_hash FROM auth_captcha_challenges WHERE id=$1", tokenHash(d["captcha_id"].(string))).Scan(&storedNonce); err != nil || storedNonce != tokenHash(cookies[0].Value) {
		t.Fatal("issued challenge cookie binding", err)
	}
	secureHandler := New(db, config.Config{PublicOrigin: "https://example.test", SecureCookie: true}, slog.Default()).Handler()
	secure := request(secureHandler, "GET", "/api/v1/auth/captcha?action=login", nil, "", "")
	expect(t, secure, 200)
	if len(secure.Result().Cookies()) != 1 || !secure.Result().Cookies()[0].Secure {
		t.Fatal("secure deployment issued an insecure captcha cookie")
	}
}

func TestCaptchaProtectsAuthAndConsumesOnce(t *testing.T) {
	db, h := testServer(t)
	userID, _ := user(t, db, h, "captcha-login", "customer")
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=true"); err != nil {
		t.Fatal(err)
	}
	nonce := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	id := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	seedCaptcha(t, db, id, "login", "ABC234", nonce, 5*time.Minute)
	cookie := captchaCookie + "=" + nonce
	wrongCookie := captchaCookie + "=" + "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	w := request(h, "POST", "/api/v1/auth/login", map[string]any{"identifier": "captcha-login@example.test", "password": "integration-password", "captcha_id": id, "captcha_answer": "ABC234"}, wrongCookie, "")
	if errorData(t, w)["code"] != "invalid_captcha" {
		t.Fatal("cookie mismatch accepted")
	}
	seedCaptcha(t, db, "abababababababababababababababababababababababababababababababab", "register", "ABC234", nonce, 5*time.Minute)
	w = request(h, "POST", "/api/v1/auth/login", map[string]any{"identifier": "captcha-login@example.test", "password": "integration-password", "captcha_id": "abababababababababababababababababababababababababababababababab", "captcha_answer": "ABC234"}, cookie, "")
	if errorData(t, w)["code"] != "invalid_captcha" {
		t.Fatal("action mismatch accepted")
	}
	w = request(h, "POST", "/api/v1/auth/login", map[string]any{"identifier": "captcha-login@example.test", "password": "wrong", "captcha_id": id, "captcha_answer": "ABC234"}, cookie, "")
	expect(t, w, 401)
	if errorData(t, w)["code"] != "invalid_credentials" {
		t.Fatal("valid captcha did not reach credential check")
	}
	w = request(h, "POST", "/api/v1/auth/login", map[string]any{"identifier": "captcha-login@example.test", "password": "integration-password", "captcha_id": id, "captcha_answer": "ABC234"}, cookie, "")
	expect(t, w, 401)
	if errorData(t, w)["code"] != "invalid_captcha" {
		t.Fatal("captcha replay accepted")
	}
	var sessions int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM sessions s JOIN users u ON u.id=s.user_id WHERE u.username='captcha-login'").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal("bad credential created session", sessions, err)
	}
	seedCaptcha(t, db, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "login", "ABC234", nonce, 5*time.Minute)
	goodID := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	good := map[string]any{"identifier": "captcha-login@example.test", "password": "integration-password", "captcha_id": goodID, "captcha_answer": "ABC234"}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); statuses <- request(h, "POST", "/api/v1/auth/login", good, cookie, "").Code }()
	}
	wg.Wait()
	close(statuses)
	seen := map[int]int{}
	for status := range statuses {
		seen[status]++
	}
	if seen[200] != 1 || seen[401] != 1 {
		t.Fatal("concurrent captcha consume", seen)
	}
}

func TestCaptchaProtectsRegisterAndForgotPassword(t *testing.T) {
	db, h := testServer(t)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=true"); err != nil {
		t.Fatal(err)
	}
	nonce := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	registerID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	seedCaptcha(t, db, registerID, "register", "ABC234", nonce, 5*time.Minute)
	w := request(h, "POST", "/api/v1/auth/register", map[string]any{"username": "captcha-new", "email": "captcha-new@example.test", "display_name": "验证码用户", "password": "integration-password", "captcha_id": registerID, "captcha_answer": "ABC234"}, captchaCookie+"="+nonce, "")
	expect(t, w, 201)
	forgotID := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	seedCaptcha(t, db, forgotID, "forgot-password", "ABC234", nonce, 5*time.Minute)
	w = request(h, "POST", "/api/v1/auth/password-reset/request", map[string]any{"identifier": "missing-account", "captcha_id": forgotID, "captcha_answer": "ABC234"}, captchaCookie+"="+nonce, "")
	expect(t, w, 202)
	seedCaptcha(t, db, "9999999999999999999999999999999999999999999999999999999999999999", "login", "ABC234", nonce, -time.Minute)
	w = request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "captcha-new", "password": "integration-password", "captcha_id": "9999999999999999999999999999999999999999999999999999999999999999", "captcha_answer": "ABC234"}, captchaCookie+"="+nonce, "")
	if errorData(t, w)["code"] != "invalid_captcha" {
		t.Fatal("expired captcha accepted")
	}
}

func TestIssuedCaptchaCookieRefreshAndLogin(t *testing.T) {
	db, h := testServer(t)
	_, _ = user(t, db, h, "captcha-issued-login", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=true"); err != nil {
		t.Fatal(err)
	}
	first := request(h, "GET", "/api/v1/auth/captcha?action=login", nil, "", "")
	expect(t, first, 200)
	firstID := data(t, first)["captcha_id"].(string)
	cookie := first.Result().Cookies()[0]
	if cookie.Secure || !cookie.HttpOnly || cookie.MaxAge != 300 || !strings.HasPrefix(data(t, first)["image"].(string), "data:image/png;base64,") {
		t.Fatal("issued response security contract")
	}
	if len(data(t, first)) != 4 {
		t.Fatal("challenge response has unexpected fields")
	}
	var storedID string
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM auth_captcha_challenges WHERE id=$1", tokenHash(firstID)).Scan(&storedID); err != nil || storedID == firstID {
		t.Fatal("raw challenge token persisted", err)
	}
	second := request(h, "GET", "/api/v1/auth/captcha?action=login", nil, cookie.Name+"="+cookie.Value, "")
	expect(t, second, 200)
	if second.Result().Cookies()[0].Value != cookie.Value {
		t.Fatal("refresh changed browser nonce")
	}
	answer := "ABC234"
	if _, err := db.Pool.Exec(t.Context(), "UPDATE auth_captcha_challenges SET answer_hash=$1 WHERE id=$2", tokenHash(answer), tokenHash(firstID)); err != nil {
		t.Fatal(err)
	}
	login := map[string]any{"identifier": "captcha-issued-login@example.test", "password": "integration-password", "captcha_id": firstID, "captcha_answer": " abc234 "}
	expect(t, request(h, "POST", "/api/v1/auth/login", login, cookie.Name+"="+cookie.Value, ""), 200)
	if errorData(t, request(h, "POST", "/api/v1/auth/login", login, cookie.Name+"="+cookie.Value, ""))["code"] != "invalid_captcha" {
		t.Fatal("issued challenge replay accepted")
	}
}

func TestCaptchaInvalidWrongAnswerAndBoundedIssue(t *testing.T) {
	db, h := testServer(t)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=true"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/auth/captcha?action=invalid", nil, "", ""), 400)
	issued := request(h, "GET", "/api/v1/auth/captcha?action=login", nil, "", "")
	expect(t, issued, 200)
	id := data(t, issued)["captcha_id"].(string)
	cookie := issued.Result().Cookies()[0]
	if _, err := db.Pool.Exec(t.Context(), "UPDATE auth_captcha_challenges SET answer_hash=$1 WHERE id=$2", tokenHash("ABC234"), tokenHash(id)); err != nil {
		t.Fatal(err)
	}
	post := func(answer string) *http.Response {
		return request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "missing", "password": "password", "captcha_id": id, "captcha_answer": answer}, cookie.Name+"="+cookie.Value, "").Result()
	}
	if post("ABC2347").StatusCode != 401 || post("ZZZ999").StatusCode != 401 || post("ABC234").StatusCode != 401 {
		t.Fatal("invalid answer or replay accepted")
	}
	var consumed bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT consumed_at IS NOT NULL FROM auth_captcha_challenges WHERE id=$1", tokenHash(id)).Scan(&consumed); err != nil || !consumed {
		t.Fatal("wrong answer was not consumed", err)
	}
	malformedID := "1212121212121212121212121212121212121212121212121212121212121212"
	seedCaptcha(t, db, malformedID, "login", "ABC234", cookie.Value, 5*time.Minute)
	malformed := request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "missing", "password": "password", "captcha_id": malformedID, "captcha_answer": strings.Repeat("A", 65)}, cookie.Name+"="+cookie.Value, "")
	if errorData(t, malformed)["code"] != "invalid_captcha" {
		t.Fatal("malformed answer accepted")
	}
	var malformedConsumed bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT consumed_at IS NOT NULL FROM auth_captcha_challenges WHERE id=$1", tokenHash(malformedID)).Scan(&malformedConsumed); err != nil || !malformedConsumed {
		t.Fatal("malformed answer was not consumed", err)
	}
	correct := request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "missing", "password": "password", "captcha_id": malformedID, "captcha_answer": "ABC234"}, cookie.Name+"="+cookie.Value, "")
	if errorData(t, correct)["code"] != "invalid_captcha" {
		t.Fatal("malformed answer challenge replay accepted")
	}
	for i := 0; i < 11; i++ {
		expect(t, request(h, "GET", "/api/v1/auth/captcha?action=login", nil, cookie.Name+"="+cookie.Value, ""), 200)
	}
	expect(t, request(h, "GET", "/api/v1/auth/captcha?action=login", nil, cookie.Name+"="+cookie.Value, ""), 429)
}

func TestCaptchaBoundedExpiryCleanupAndStorageFailure(t *testing.T) {
	db, h := testServer(t)
	userID, _ := user(t, db, h, "captcha-storage-login", "customer")
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=true"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO auth_captcha_challenges(id,action,answer_hash,nonce_hash,issued_ip,expires_at)
SELECT lpad(to_hex(n),64,'0'),'login',repeat('a',64),repeat('b',64),'127.0.0.1',now()-interval '1 minute' FROM generate_series(1,101) n`); err != nil {
		t.Fatal(err)
	}
	issued := request(h, "GET", "/api/v1/auth/captcha?action=login", nil, "", "")
	expect(t, issued, 200)
	var remaining int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM auth_captcha_challenges WHERE expires_at<=now()").Scan(&remaining); err != nil || remaining != 1 {
		t.Fatal("expired challenge cleanup was not bounded", err, remaining)
	}
	id := data(t, issued)["captcha_id"].(string)
	cookie := issued.Result().Cookies()[0]
	if _, err := db.Pool.Exec(t.Context(), "DROP TABLE auth_captcha_challenges"); err != nil {
		t.Fatal(err)
	}
	unavailable := request(h, "GET", "/api/v1/auth/captcha?action=login", nil, cookie.Name+"="+cookie.Value, "")
	expect(t, unavailable, 503)
	if errorData(t, unavailable)["code"] != "captcha_unavailable" {
		t.Fatal("challenge storage failure was unclear")
	}
	login := request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "captcha-storage-login", "password": "integration-password", "captcha_id": id, "captcha_answer": "ABC234"}, cookie.Name+"="+cookie.Value, "")
	expect(t, login, 503)
	if errorData(t, login)["code"] != "captcha_unavailable" || len(login.Result().Cookies()) != 0 {
		t.Fatal("login did not fail closed on captcha storage error")
	}
}
