package httpapi

import (
	"crypto/md5"
	"encoding/hex"
	"log/slog"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/legacy"
	"golang.org/x/crypto/bcrypt"
)

func TestLegacyCMFPasswordUpgradesOnLogin(t *testing.T) {
	db, _ := testServer(t)
	inner := md5.Sum([]byte("auth-codelegacy-login-password"))
	outer := md5.Sum([]byte(hex.EncodeToString(inner[:])))
	legacyHash := legacy.WrapCMFPasswordHash("###" + hex.EncodeToString(outer[:]))
	var id int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,password_reset_required)
VALUES('legacy-login','legacy-login@example.test',$1,'Legacy Login',true) RETURNING id`, legacyHash).Scan(&id); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{PublicOrigin: "http://localhost:3088", LegacyCMFAuthCode: "auth-code"}, slog.Default()).Handler()
	login := request(h, "POST", "/api/v1/auth/login", map[string]string{"username": "legacy-login", "password": "legacy-login-password"}, "", "")
	expect(t, login, 200)
	var hash string
	var reset bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT password_hash,password_reset_required FROM users WHERE id=$1", id).Scan(&hash, &reset); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$2") || reset || bcrypt.CompareHashAndPassword([]byte(hash), []byte("legacy-login-password")) != nil {
		t.Fatalf("legacy password was not upgraded: hash=%q reset=%v", hash, reset)
	}
}
