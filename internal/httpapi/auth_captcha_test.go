package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image/png"
	"net/http"
	"sync"
	"testing"
	"time"

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
	w := request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "captcha-login", "password": "integration-password", "captcha_id": id, "captcha_answer": "ABC234"}, wrongCookie, "")
	if errorData(t, w)["code"] != "invalid_captcha" {
		t.Fatal("cookie mismatch accepted")
	}
	seedCaptcha(t, db, "abababababababababababababababababababababababababababababababab", "register", "ABC234", nonce, 5*time.Minute)
	w = request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "captcha-login", "password": "integration-password", "captcha_id": "abababababababababababababababababababababababababababababababab", "captcha_answer": "ABC234"}, cookie, "")
	if errorData(t, w)["code"] != "invalid_captcha" {
		t.Fatal("action mismatch accepted")
	}
	w = request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "captcha-login", "password": "wrong", "captcha_id": id, "captcha_answer": "ABC234"}, cookie, "")
	expect(t, w, 401)
	if errorData(t, w)["code"] != "invalid_credentials" {
		t.Fatal("valid captcha did not reach credential check")
	}
	w = request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "captcha-login", "password": "integration-password", "captcha_id": id, "captcha_answer": "ABC234"}, cookie, "")
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
	good := map[string]any{"username": "captcha-login", "password": "integration-password", "captcha_id": goodID, "captcha_answer": "ABC234"}
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
