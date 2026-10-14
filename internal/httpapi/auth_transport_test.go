package httpapi

import (
	"net"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
)

func trustedLoopback(t *testing.T) *net.IPNet {
	t.Helper()
	_, network, err := net.ParseCIDR("127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	return network
}

// TestLoginRateLimitUsesResolvedClientIPBehindTrustedProxy proves the login
// rate limit buckets requests by the resolved client address when the request
// arrives from a configured trusted proxy, and keeps the historical shared
// socket-peer bucket when no proxy is trusted.
func TestLoginRateLimitUsesResolvedClientIPBehindTrustedProxy(t *testing.T) {
	db, h := testServerConfig(t, func(cfg *config.Config) {
		cfg.TrustedProxyCIDRs = []*net.IPNet{trustedLoopback(t)}
	})
	login := func(remoteAddr, forwarded string) int {
		return requestFrom(h, "POST", "/api/v1/auth/login", map[string]any{"username": "", "password": ""}, remoteAddr, "",
			map[string]string{"X-Forwarded-For": forwarded}).Code
	}
	for i := 0; i < 30; i++ {
		if status := login("127.0.0.1:5000", "203.0.113.10"); status != 400 {
			t.Fatalf("attempt %d status=%d, want 400", i+1, status)
		}
	}
	if status := login("127.0.0.1:5000", "203.0.113.10"); status != 429 {
		t.Fatalf("budget for the forwarded client not exhausted: %d", status)
	}
	if status := login("127.0.0.1:5000", "203.0.113.11"); status != 400 {
		t.Fatalf("a second forwarded client shared the first client's bucket: %d", status)
	}
	// Buckets are keyed by the resolved client address, not the proxy hop.
	var bucketCount int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM auth_rate_limits WHERE key IN ($1,$2)", tokenHash("login:203.0.113.10"), tokenHash("login:203.0.113.11")).Scan(&bucketCount); err != nil || bucketCount != 2 {
		t.Fatalf("per-client login buckets=%d err=%v", bucketCount, err)
	}

	// Without TRUSTED_PROXY_CIDRS the forwarded header is meaningless and all
	// requests share the socket peer bucket, matching the previous behaviour.
	_, plain := testServer(t)
	plainLogin := func(forwarded string) int {
		return requestFrom(plain, "POST", "/api/v1/auth/login", map[string]any{"username": "", "password": ""}, "198.51.100.20:5000", "",
			map[string]string{"X-Forwarded-For": forwarded}).Code
	}
	for i := 0; i < 30; i++ {
		if status := plainLogin("203.0.113.20"); status != 400 {
			t.Fatalf("attempt %d status=%d, want 400", i+1, status)
		}
	}
	if status := plainLogin("203.0.113.21"); status != 429 {
		t.Fatalf("untrusted X-Forwarded-For split the peer bucket: %d", status)
	}
}

// TestCaptchaAndAdminAuditUseResolvedClientIP verifies captcha issuance and
// admin API audit logs attribute the resolved client address instead of the
// loopback proxy hop, and that forwarded headers stay ignored by default.
func TestCaptchaAndAdminAuditUseResolvedClientIP(t *testing.T) {
	db, h := testServerConfig(t, func(cfg *config.Config) {
		cfg.TrustedProxyCIDRs = []*net.IPNet{trustedLoopback(t)}
	})
	// Create the administrator before captcha is enabled so the login helper
	// keeps working, then turn captcha on for the issuance assertions.
	_, admin := user(t, db, h, "proxy-audit-admin", "super_admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=true"); err != nil {
		t.Fatal(err)
	}
	issued := requestFrom(h, "GET", "/api/v1/auth/captcha?action=login", nil, "127.0.0.1:5000", "",
		map[string]string{"X-Forwarded-For": "203.0.113.50, 10.0.0.9"})
	expect(t, issued, 200)
	id := data(t, issued)["captcha_id"].(string)
	var issuedIP string
	if err := db.Pool.QueryRow(t.Context(), "SELECT issued_ip FROM auth_captcha_challenges WHERE id=$1", tokenHash(id)).Scan(&issuedIP); err != nil {
		t.Fatal(err)
	}
	// The right-most hop that is not itself a trusted proxy is the client.
	if issuedIP != "10.0.0.9" {
		t.Fatalf("captcha issued_ip=%q, want the right-most untrusted forwarded hop", issuedIP)
	}

	expect(t, requestFrom(h, "GET", "/api/v1/admin/dashboard", nil, "127.0.0.1:5000", admin,
		map[string]string{"X-Forwarded-For": "198.51.100.77"}), 200)
	var loggedIP string
	if err := db.Pool.QueryRow(t.Context(), "SELECT replace(host(remote_ip),'::ffff:','') FROM admin_api_logs WHERE actor_id=(SELECT id FROM users WHERE username='proxy-audit-admin') ORDER BY id DESC LIMIT 1").Scan(&loggedIP); err != nil {
		t.Fatal(err)
	}
	if loggedIP != "198.51.100.77" {
		t.Fatalf("admin_api_logs remote_ip=%q, want the resolved client address", loggedIP)
	}

	// Default configuration (no trusted proxies) must ignore the headers.
	plainDB, plain := testServer(t)
	_, plainAdmin := user(t, plainDB, plain, "proxy-audit-admin-plain", "super_admin")
	if _, err := plainDB.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=true"); err != nil {
		t.Fatal(err)
	}
	plainIssued := requestFrom(plain, "GET", "/api/v1/auth/captcha?action=login", nil, "192.0.2.9:5000", "",
		map[string]string{"X-Forwarded-For": "203.0.113.99"})
	expect(t, plainIssued, 200)
	plainID := data(t, plainIssued)["captcha_id"].(string)
	if err := plainDB.Pool.QueryRow(t.Context(), "SELECT issued_ip FROM auth_captcha_challenges WHERE id=$1", tokenHash(plainID)).Scan(&issuedIP); err != nil {
		t.Fatal(err)
	}
	if issuedIP != "192.0.2.9" {
		t.Fatalf("untrusted X-Forwarded-For changed captcha issued_ip to %q", issuedIP)
	}
	expect(t, requestFrom(plain, "GET", "/api/v1/admin/dashboard", nil, "192.0.2.9:5000", plainAdmin,
		map[string]string{"X-Forwarded-For": "203.0.113.99"}), 200)
	if err := plainDB.Pool.QueryRow(t.Context(), "SELECT replace(host(remote_ip),'::ffff:','') FROM admin_api_logs WHERE actor_id=(SELECT id FROM users WHERE username='proxy-audit-admin-plain') ORDER BY id DESC LIMIT 1").Scan(&loggedIP); err != nil {
		t.Fatal(err)
	}
	if loggedIP != "192.0.2.9" {
		t.Fatalf("untrusted audit remote_ip=%q, want the socket peer", loggedIP)
	}
}

// TestAutomaticAdminBlockIsPerSourceNotGlobal reproduces the denial-of-service
// regression: three failed logins from one address must not lock an
// administrator account for every other address. The automatic blacklist entry
// is per-source IP, the username itself is never blocked automatically, and a
// correct login from a different address still succeeds.
func TestAutomaticAdminBlockIsPerSourceNotGlobal(t *testing.T) {
	db, h := testServer(t)
	_, _ = user(t, db, h, "lock-target", "super_admin")
	wrong := map[string]any{"username": "lock-target", "password": "wrong-password"}
	for i := 0; i < 3; i++ {
		w := requestFrom(h, "POST", "/api/v1/auth/login", wrong, "198.51.100.10:5000", "", nil)
		expect(t, w, 401)
	}
	expect(t, requestFrom(h, "POST", "/api/v1/auth/login", wrong, "198.51.100.10:5000", "", nil), 403)
	var usernameRows, ipRows int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM auth_blacklist WHERE subject_type='username' AND subject='lock-target'").Scan(&usernameRows); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM auth_blacklist WHERE subject_type='ip' AND subject='198.51.100.10'").Scan(&ipRows); err != nil {
		t.Fatal(err)
	}
	if usernameRows != 0 || ipRows != 1 {
		t.Fatalf("automatic blacklist rows username=%d ip=%d; the account must never be auto-blocked globally", usernameRows, ipRows)
	}
	var auditCount int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='auth.login_blocked' AND payload->>'subject_type'='ip'").Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("login_blocked audit count=%d err=%v", auditCount, err)
	}
	// The legitimate administrator from any other address can still log in.
	good := map[string]any{"username": "lock-target", "password": "integration-password"}
	expect(t, requestFrom(h, "POST", "/api/v1/auth/login", good, "198.51.100.11:5000", "", nil), 200)
	// The attacking address itself stays blocked for the configured window.
	expect(t, requestFrom(h, "POST", "/api/v1/auth/login", good, "198.51.100.10:5000", "", nil), 403)
}

// TestAdminFailureBackoffGrowsAndResetsOnSuccess covers the cross-source
// protection that replaces the removed global username lock: failures from
// distinct addresses only add exponentially growing delay to further failed
// attempts, and a verified password clears the pressure immediately.
func TestAdminFailureBackoffGrowsAndResetsOnSuccess(t *testing.T) {
	db, h := testServer(t)
	_, _ = user(t, db, h, "backoff-admin", "super_admin")
	wrong := map[string]any{"username": "backoff-admin", "password": "wrong-password"}
	for _, attempt := range []struct {
		addr  string
		delay bool
	}{
		{"203.0.113.1:5000", false},
		{"203.0.113.2:5000", false},
		{"203.0.113.3:5000", false},
		{"203.0.113.4:5000", true},
	} {
		started := time.Now()
		expect(t, requestFrom(h, "POST", "/api/v1/auth/login", wrong, attempt.addr, "", nil), 401)
		elapsed := time.Since(started)
		if attempt.delay && elapsed < 900*time.Millisecond {
			t.Fatalf("failure %s returned after %v, want the exponential delay", attempt.addr, elapsed)
		}
		if !attempt.delay && elapsed > 900*time.Millisecond {
			t.Fatalf("failure %s was delayed by %v before the threshold", attempt.addr, elapsed)
		}
	}
	// A correct password always authenticates promptly and forgets the history.
	good := map[string]any{"username": "backoff-admin", "password": "integration-password"}
	started := time.Now()
	expect(t, requestFrom(h, "POST", "/api/v1/auth/login", good, "203.0.113.9:5000", "", nil), 200)
	if elapsed := time.Since(started); elapsed > 900*time.Millisecond {
		t.Fatalf("valid credentials were delayed by the failure backoff: %v", elapsed)
	}
	var pressure int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM auth_rate_limits WHERE key=$1", tokenHash("admin-login-account:backoff-admin")).Scan(&pressure); err != nil || pressure != 0 {
		t.Fatalf("account pressure not cleared on success: %d, %v", pressure, err)
	}
	started = time.Now()
	expect(t, requestFrom(h, "POST", "/api/v1/auth/login", wrong, "203.0.113.12:5000", "", nil), 401)
	if elapsed := time.Since(started); elapsed > 900*time.Millisecond {
		t.Fatalf("cleared pressure still delayed a failed attempt: %v", elapsed)
	}
}

// TestAdminFailureDelayCurve pins the exponential backoff that replaces the
// former global account lock: nothing happens at or below the configured
// threshold, the delay then doubles per additional failure and is capped.
func TestAdminFailureDelayCurve(t *testing.T) {
	cases := []struct {
		attempts, max int
		want          time.Duration
	}{
		{1, 3, 0},
		{3, 3, 0},
		{4, 3, time.Second},
		{5, 3, 2 * time.Second},
		{6, 3, 4 * time.Second},
		{7, 3, 8 * time.Second},
		{13, 3, adminFailureMaxDelay},
		{100, 3, adminFailureMaxDelay},
		{2, 20, 0},
	}
	for _, tc := range cases {
		if got := adminFailureDelay(tc.attempts, tc.max); got != tc.want {
			t.Fatalf("adminFailureDelay(%d,%d)=%v, want %v", tc.attempts, tc.max, got, tc.want)
		}
	}
}

// TestManualUsernameBlacklistStaysGlobal pins the manual semantics: an
// administrator-created username entry keeps blocking every source address.
func TestManualUsernameBlacklistStaysGlobal(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "manual-block-root", "super_admin")
	_, _ = user(t, db, h, "manual-block-target", "super_admin")
	created := request(h, "POST", "/api/v1/admin/blacklist", map[string]any{
		"subject_type": "username", "subject": "manual-block-target", "operation_type": "login", "remote_addr": "192.0.2.4", "reason": "手工阻断", "expected_version": 0,
	}, root, "manual-block-create")
	expect(t, created, 201)
	good := map[string]any{"username": "manual-block-target", "password": "integration-password"}
	expect(t, requestFrom(h, "POST", "/api/v1/auth/login", good, "198.51.100.30:5000", "", nil), 403)
	expect(t, requestFrom(h, "POST", "/api/v1/auth/login", good, "198.51.100.31:5000", "", nil), 403)
}
