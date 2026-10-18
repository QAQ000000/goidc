package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/captcha"
	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/store"
)

// These providers are intentionally test-only. Their typed proofs exercise
// the real HTTP, storage and authentication boundaries without a vendor SDK,
// a network request, or production credentials.
type captchaModuleProbe struct {
	issues   atomic.Int32
	verifies atomic.Int32
	mu       sync.Mutex
	issued   []captcha.Request
	verified []captcha.Request
	proofs   []captcha.Proof
	check    func(context.Context, captcha.Request) error
}

var captchaModuleProbes sync.Map
var captchaModuleRegistration sync.Once
var captchaModuleRegistrationErr error
var captchaModuleProbeSequence atomic.Uint64

type captchaModuleProvider struct {
	kind   captcha.Kind
	probe  *captchaModuleProbe
	mode   string
	secret string
}

func captchaModulePNG() string {
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewRGBA(image.Rect(0, 0, 100, 80))); err != nil {
		panic(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(out.Bytes())
}

func (p *captchaModuleProvider) Issue(ctx context.Context, req captcha.Request) (captcha.Issued, error) {
	p.probe.issues.Add(1)
	p.probe.mu.Lock()
	p.probe.issued = append(p.probe.issued, req)
	p.probe.mu.Unlock()
	challenge := captcha.Challenge{Kind: p.kind, Renderer: string(p.kind), Prompt: "Complete the test challenge"}
	if p.kind == captcha.Widget {
		challenge.Renderer = "test_widget"
		challenge.PublicData = map[string]any{"site_key": "public-site-key"}
	} else {
		challenge.Image = captchaModulePNG()
		challenge.Width, challenge.Height = 100, 80
		if p.kind == captcha.Click {
			challenge.MaxPoints = 2
		}
	}
	issued := captcha.Issued{Challenge: challenge, State: []byte(p.secret)}
	switch p.mode {
	case "issue_error":
		return captcha.Issued{}, errors.New("test provider unavailable")
	case "state_overflow":
		issued.State = bytes.Repeat([]byte("s"), 2<<20)
	case "public_overflow":
		issued.Challenge.PublicData = map[string]any{"oversized": strings.Repeat("p", 2<<20)}
	}
	return issued, nil
}

func (p *captchaModuleProvider) Verify(ctx context.Context, req captcha.Request, state []byte, proof captcha.Proof) (bool, error) {
	p.probe.verifies.Add(1)
	p.probe.mu.Lock()
	p.probe.verified = append(p.probe.verified, req)
	p.probe.proofs = append(p.probe.proofs, proof)
	p.probe.mu.Unlock()
	if p.probe.check != nil {
		if err := p.probe.check(ctx, req); err != nil {
			return false, err
		}
	}
	if string(state) != p.secret {
		return false, errors.New("private provider state was lost")
	}
	switch p.mode {
	case "verify_error":
		return false, errors.New("test provider unavailable")
	case "verify_timeout":
		<-ctx.Done()
		return false, ctx.Err()
	}
	if proof.Kind != p.kind {
		return false, nil
	}
	switch p.kind {
	case captcha.Image:
		return proof.Answer == "aBc-7", nil
	case captcha.Click:
		return len(proof.Points) == 2 && proof.Points[0].X == 0.2 && proof.Points[0].Y == 0.3 && proof.Points[1].X == 0.7 && proof.Points[1].Y == 0.8, nil
	case captcha.Widget:
		return proof.Token == "verified-by-server", nil
	default:
		return false, nil
	}
}

func registerCaptchaModuleProviders(t *testing.T) {
	t.Helper()
	captchaModuleRegistration.Do(func() {
		for _, kind := range []captcha.Kind{captcha.Image, captcha.Click, captcha.Widget} {
			providerKind := kind
			err := captcha.Register(channel.Descriptor{
				ID: "test_captcha_" + string(kind), Name: "Test CAPTCHA " + string(kind), Category: channel.Captcha,
				Capabilities: []string{"issue", "verify", string(kind)}, SchemaVersion: 1, ContractVersion: channel.ContractVersion, InstanceMode: "profile",
				Fields: []channel.Field{
					{Key: "probe", Label: "Test probe", Type: channel.Text, Required: true},
					{Key: "mode", Label: "Test mode", Type: channel.Text},
					{Key: "secret", Label: "Test secret", Type: channel.Text, Required: true, Secret: true},
				},
			}, func(cfg module.Config) (captcha.Provider, error) {
				value, ok := captchaModuleProbes.Load(cfg.Values["probe"])
				if !ok {
					return nil, errors.New("test probe missing")
				}
				mode, _ := cfg.Values["mode"].(string)
				return &captchaModuleProvider{kind: providerKind, probe: value.(*captchaModuleProbe), mode: mode, secret: cfg.Secrets["secret"]}, nil
			})
			if err != nil {
				captchaModuleRegistrationErr = err
				return
			}
		}
	})
	if captchaModuleRegistrationErr != nil {
		t.Fatal(captchaModuleRegistrationErr)
	}
}

func captchaModuleProfile(t *testing.T, kind captcha.Kind, mode string) (captcha.Profile, *captchaModuleProbe) {
	t.Helper()
	registerCaptchaModuleProviders(t)
	key := fmt.Sprintf("probe-%d", captchaModuleProbeSequence.Add(1))
	probe := &captchaModuleProbe{}
	captchaModuleProbes.Store(key, probe)
	t.Cleanup(func() { captchaModuleProbes.Delete(key) })
	return captcha.Profile{ID: key, ImplementationID: "test_captcha_" + string(kind), Config: map[string]any{"probe": key, "mode": mode}, Secrets: map[string]string{"secret": "private-server-only-state"}}, probe
}

func captchaModuleManager(t *testing.T, active string, profiles ...captcha.Profile) *captcha.Manager {
	t.Helper()
	manager, err := captcha.NewManager(profiles, active)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

type captchaModuleFixture struct {
	db    *store.Store
	h     http.Handler
	uid   int64
	name  string
	probe *captchaModuleProbe
}

func newCaptchaModuleFixture(t *testing.T, kind captcha.Kind, mode string) (captchaModuleFixture, captcha.Profile) {
	t.Helper()
	profile, probe := captchaModuleProfile(t, kind, mode)
	manager := captchaModuleManager(t, profile.ID, profile)
	db, h := testServerConfig(t, func(cfg *config.Config) { cfg.Captcha = manager })
	name := "captcha-module-user"
	uid, _ := user(t, db, h, name, "customer")
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=true"); err != nil {
		t.Fatal(err)
	}
	probe.check = func(ctx context.Context, req captcha.Request) error {
		var consumed bool
		// A separate pool query must see the claim before the external verifier
		// runs. A still-open transaction would make this false.
		if err := db.Pool.QueryRow(ctx, "SELECT consumed_at IS NOT NULL FROM auth_captcha_challenges WHERE id=$1", tokenHash(req.ChallengeID)).Scan(&consumed); err != nil {
			return err
		}
		if !consumed {
			return errors.New("provider called before durable one-time claim")
		}
		return nil
	}
	return captchaModuleFixture{db: db, h: h, uid: uid, name: name, probe: probe}, profile
}

func (f captchaModuleFixture) issue(t *testing.T, action string) (string, string, map[string]any) {
	t.Helper()
	w := request(f.h, "GET", "/api/v1/auth/captcha?action="+action, nil, "", "")
	expect(t, w, 200)
	d := data(t, w)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != captchaCookie {
		t.Fatal("challenge did not bind a browser nonce")
	}
	if bytes.Contains(w.Body.Bytes(), []byte("private-server-only-state")) || bytes.Contains(w.Body.Bytes(), []byte("provider_state")) {
		t.Fatal("challenge exposed private provider state")
	}
	return d["captcha_id"].(string), cookies[0].Name + "=" + cookies[0].Value, d
}

func (f captchaModuleFixture) login(id string, proof any) map[string]any {
	return map[string]any{"identifier": f.name + "@example.test", "password": "integration-password", "captcha_id": id, "captcha_proof": proof}
}

func captchaModuleGoodProof(kind captcha.Kind) captcha.Proof {
	switch kind {
	case captcha.Image:
		return captcha.Proof{Kind: kind, Answer: "aBc-7"}
	case captcha.Click:
		return captcha.Proof{Kind: kind, Points: []captcha.Point{{X: 0.2, Y: 0.3}, {X: 0.7, Y: 0.8}}}
	default:
		return captcha.Proof{Kind: kind, Token: "verified-by-server"}
	}
}

func (f captchaModuleFixture) assertSessions(t *testing.T, want int) {
	t.Helper()
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM sessions WHERE user_id=$1", f.uid).Scan(&count); err != nil || count != want {
		t.Fatalf("sessions=%d want %d: %v", count, want, err)
	}
}

func (f captchaModuleFixture) assertConsumed(t *testing.T, id string) {
	t.Helper()
	var consumed bool
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT consumed_at IS NOT NULL FROM auth_captcha_challenges WHERE id=$1", tokenHash(id)).Scan(&consumed); err != nil || !consumed {
		t.Fatal("challenge was not irreversibly consumed", err)
	}
}

func TestCaptchaModuleTypedChallengesAuthenticate(t *testing.T) {
	for _, kind := range []captcha.Kind{captcha.Image, captcha.Click, captcha.Widget} {
		t.Run(string(kind), func(t *testing.T) {
			f, profile := newCaptchaModuleFixture(t, kind, "")
			id, cookie, d := f.issue(t, "login")
			challenge, ok := d["challenge"].(map[string]any)
			if !ok || challenge["kind"] != string(kind) {
				t.Fatalf("typed challenge missing: %v", d)
			}
			_, legacyImage := d["image"]
			if legacyImage != (kind == captcha.Image) {
				t.Fatalf("legacy image field for %s: %v", kind, legacyImage)
			}
			var implementation, profileID, fingerprint string
			var state []byte
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT provider_id,profile_id,profile_fingerprint,provider_state FROM auth_captcha_challenges WHERE id=$1", tokenHash(id)).Scan(&implementation, &profileID, &fingerprint, &state); err != nil {
				t.Fatal(err)
			}
			if implementation != profile.ImplementationID || profileID != profile.ID || len(fingerprint) != 64 || string(state) != "private-server-only-state" {
				t.Fatal("provider/profile/state binding was not persisted")
			}
			expect(t, request(f.h, "POST", "/api/v1/auth/login", f.login(id, captchaModuleGoodProof(kind)), cookie, ""), 200)
			if f.probe.verifies.Load() != 1 {
				t.Fatal("authentication did not use the bound provider")
			}
			f.probe.mu.Lock()
			issued, verified := f.probe.issued[0], f.probe.verified[0]
			f.probe.mu.Unlock()
			if issued.Action != "login" || issued.ChallengeID != id || issued.ClientIP == "" || issued.ExpiresAt.Before(time.Now()) || issued.Action != verified.Action || issued.ChallengeID != verified.ChallengeID || issued.ClientIP != verified.ClientIP || issued.ExpiresAt.Sub(verified.ExpiresAt).Abs() > time.Microsecond {
				t.Fatalf("provider request binding changed: issued=%+v verified=%+v", issued, verified)
			}
			f.assertConsumed(t, id)
			f.assertSessions(t, 1)
		})
	}
}

func TestCaptchaModuleNonceActionAndReplayBinding(t *testing.T) {
	f, _ := newCaptchaModuleFixture(t, captcha.Click, "")
	id, cookie, _ := f.issue(t, "register")
	wrongAction := request(f.h, "POST", "/api/v1/auth/login", f.login(id, captchaModuleGoodProof(captcha.Click)), cookie, "")
	expect(t, wrongAction, 401)
	if errorData(t, wrongAction)["code"] != "invalid_captcha" || f.probe.verifies.Load() != 0 {
		t.Fatal("cross-action proof reached provider")
	}
	id, cookie, _ = f.issue(t, "login")
	wrongNonce := request(f.h, "POST", "/api/v1/auth/login", f.login(id, captchaModuleGoodProof(captcha.Click)), captchaCookie+"="+strings.Repeat("a", 64), "")
	expect(t, wrongNonce, 401)
	if f.probe.verifies.Load() != 0 {
		t.Fatal("cross-browser proof reached provider")
	}
	falseProof := captcha.Proof{Kind: captcha.Click, Points: []captcha.Point{{X: 0.4, Y: 0.5}}}
	wrong := request(f.h, "POST", "/api/v1/auth/login", f.login(id, falseProof), cookie, "")
	expect(t, wrong, 401)
	if errorData(t, wrong)["code"] != "invalid_captcha" || f.probe.verifies.Load() != 1 {
		t.Fatal("provider false was not rejected")
	}
	f.assertConsumed(t, id)
	expect(t, request(f.h, "POST", "/api/v1/auth/login", f.login(id, captchaModuleGoodProof(captcha.Click)), cookie, ""), 401)
	if f.probe.verifies.Load() != 1 {
		t.Fatal("replay re-entered provider")
	}
	id, cookie, _ = f.issue(t, "login")
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- request(f.h, "POST", "/api/v1/auth/login", f.login(id, captchaModuleGoodProof(captcha.Click)), cookie, "").Code
		}()
	}
	wg.Wait()
	close(statuses)
	seen := map[int]int{}
	for status := range statuses {
		seen[status]++
	}
	if seen[200] != 1 || seen[401] != 1 || f.probe.verifies.Load() != 2 {
		t.Fatal("concurrent proof was not claimed exactly once", seen, f.probe.verifies.Load())
	}
	f.assertSessions(t, 1)
}

func TestCaptchaModuleTypedProofProtectsRegistrationFlows(t *testing.T) {
	f, _ := newCaptchaModuleFixture(t, captcha.Widget, "")
	id, cookie, _ := f.issue(t, "register")
	register := map[string]any{
		"username": "captcha-module-new", "email": "captcha-module-new@example.test", "display_name": "Module customer", "password": "integration-password",
		"captcha_id": id, "captcha_proof": captchaModuleGoodProof(captcha.Widget),
	}
	expect(t, request(f.h, "POST", "/api/v1/auth/register", register, cookie, ""), 201)
	f.assertConsumed(t, id)
	id, cookie, _ = f.issue(t, "register")
	verification := request(f.h, "POST", "/api/v1/auth/verification/request", map[string]any{
		"purpose": "register", "channel": "email", "recipient": "captcha-module-pending@example.test", "captcha_id": id, "captcha_proof": captchaModuleGoodProof(captcha.Widget),
	}, cookie, "")
	expect(t, verification, 202)
	if data(t, verification)["challenge_id"] == id {
		t.Fatal("authentication challenge was reused as the email OTP challenge")
	}
	f.assertConsumed(t, id)
	id, cookie, _ = f.issue(t, "forgot-password")
	reset := request(f.h, "POST", "/api/v1/auth/password-reset/request", map[string]any{
		"identifier": "captcha-module-missing@example.test", "captcha_id": id, "captcha_proof": captchaModuleGoodProof(captcha.Widget),
	}, cookie, "")
	expect(t, reset, 202)
	f.assertConsumed(t, id)
	if f.probe.verifies.Load() != 3 {
		t.Fatal("a protected registration/reset endpoint skipped its bound provider", f.probe.verifies.Load())
	}
	f.assertSessions(t, 0)
}

func TestCaptchaModuleClientSuccessAndLegacyAnswerCannotBypass(t *testing.T) {
	for _, kind := range []captcha.Kind{captcha.Image, captcha.Widget} {
		t.Run(string(kind), func(t *testing.T) {
			f, _ := newCaptchaModuleFixture(t, kind, "")
			id, cookie, _ := f.issue(t, "login")
			body := f.login(id, map[string]any{"kind": string(kind), "success": true, "token": "client-says-success"})
			expect(t, request(f.h, "POST", "/api/v1/auth/login", body, cookie, ""), 400)
			if f.probe.verifies.Load() != 0 {
				t.Fatal("client success flag reached the provider")
			}
			id, cookie, _ = f.issue(t, "login")
			falseProof := captcha.Proof{Kind: kind, Token: "client-says-success"}
			if kind == captcha.Image {
				falseProof = captcha.Proof{Kind: kind, Answer: "client-says-success"}
			}
			expect(t, request(f.h, "POST", "/api/v1/auth/login", f.login(id, falseProof), cookie, ""), 401)
			if f.probe.verifies.Load() != 1 {
				t.Fatal("client proof was not checked by the provider")
			}
			f.assertSessions(t, 0)
			id, cookie, _ = f.issue(t, "login")
			legacy := f.login(id, nil)
			delete(legacy, "captcha_proof")
			legacy["captcha_answer"] = "aBc-7"
			before := f.probe.verifies.Load()
			expect(t, request(f.h, "POST", "/api/v1/auth/login", legacy, cookie, ""), 401)
			if f.probe.verifies.Load() != before {
				t.Fatal("legacy answer was routed to a non-builtin provider")
			}
			f.assertConsumed(t, id)
			f.assertSessions(t, 0)
		})
	}
}

func TestCaptchaModuleProviderFailureAndCancellationConsume(t *testing.T) {
	for _, mode := range []string{"verify_error", "verify_timeout"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := newCaptchaModuleFixture(t, captcha.Widget, mode)
			id, cookie, _ := f.issue(t, "login")
			body, err := json.Marshal(f.login(id, captchaModuleGoodProof(captcha.Widget)))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Requested-With", "zjmfgo")
			r.Header.Set("Origin", "http://localhost:3088")
			r.Header.Set("Cookie", cookie)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if mode == "verify_timeout" {
				check := f.probe.check
				f.probe.check = func(ctx context.Context, req captcha.Request) error {
					if err := check(ctx, req); err != nil {
						return err
					}
					// Cancel only after the durable claim and verifier entry, so
					// slow CI cannot cancel a preceding database operation instead.
					cancel()
					return nil
				}
			}
			w := httptest.NewRecorder()
			f.h.ServeHTTP(w, r.WithContext(ctx))
			expect(t, w, 503)
			if errorData(t, w)["code"] != "captcha_unavailable" || f.probe.verifies.Load() != 1 || len(w.Result().Cookies()) != 0 {
				t.Fatal("provider failure did not fail closed", w.Body.String())
			}
			f.assertConsumed(t, id)
			f.assertSessions(t, 0)
			expect(t, request(f.h, "POST", "/api/v1/auth/login", f.login(id, captchaModuleGoodProof(captcha.Widget)), cookie, ""), 401)
			if f.probe.verifies.Load() != 1 {
				t.Fatal("failed provider challenge could be verified again")
			}
		})
	}
}

func TestCaptchaModuleProfilesFailClosedAndRetainOldBindings(t *testing.T) {
	for _, mode := range []string{"removed", "rotated", "retained", "unknown_provider", "unknown_fingerprint"} {
		t.Run(mode, func(t *testing.T) {
			f, old := newCaptchaModuleFixture(t, captcha.Widget, "")
			id, cookie, _ := f.issue(t, "login")
			next, _ := captchaModuleProfile(t, captcha.Widget, "")
			switch mode {
			case "removed":
				f.h = New(f.db, config.Config{PublicOrigin: "http://localhost:3088", Captcha: captchaModuleManager(t, next.ID, next)}, slog.Default()).Handler()
			case "rotated":
				old.Secrets = map[string]string{"secret": "rotated-private-secret"}
				f.h = New(f.db, config.Config{PublicOrigin: "http://localhost:3088", Captcha: captchaModuleManager(t, old.ID, old)}, slog.Default()).Handler()
			case "retained":
				f.h = New(f.db, config.Config{PublicOrigin: "http://localhost:3088", Captcha: captchaModuleManager(t, next.ID, old, next)}, slog.Default()).Handler()
			case "unknown_provider":
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE auth_captcha_challenges SET provider_id='uncompiled_provider' WHERE id=$1", tokenHash(id)); err != nil {
					t.Fatal(err)
				}
			case "unknown_fingerprint":
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE auth_captcha_challenges SET profile_fingerprint=$1 WHERE id=$2", strings.Repeat("f", 64), tokenHash(id)); err != nil {
					t.Fatal(err)
				}
			}
			w := request(f.h, "POST", "/api/v1/auth/login", f.login(id, captchaModuleGoodProof(captcha.Widget)), cookie, "")
			if mode == "retained" {
				expect(t, w, 200)
				if f.probe.verifies.Load() != 1 {
					t.Fatal("active switch did not resolve retained historical profile")
				}
				f.assertSessions(t, 1)
			} else {
				expect(t, w, 503)
				if errorData(t, w)["code"] != "captcha_unavailable" || f.probe.verifies.Load() != 0 {
					t.Fatal("unavailable binding reached an unrelated provider")
				}
				f.assertSessions(t, 0)
			}
			f.assertConsumed(t, id)
		})
	}
}

func TestCaptchaModuleRejectsUnboundedProviderAndProofData(t *testing.T) {
	for _, mode := range []string{"issue_error", "state_overflow", "public_overflow"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := newCaptchaModuleFixture(t, captcha.Widget, mode)
			w := request(f.h, "GET", "/api/v1/auth/captcha?action=login", nil, "", "")
			expect(t, w, 503)
			if errorData(t, w)["code"] != "captcha_unavailable" || len(w.Result().Cookies()) != 0 {
				t.Fatal("invalid provider output became a public challenge")
			}
			var count int
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM auth_captcha_challenges").Scan(&count); err != nil || count != 0 {
				t.Fatal("invalid provider output was persisted", count, err)
			}
		})
	}
	for _, proof := range []captcha.Proof{
		{Kind: captcha.Widget, Token: strings.Repeat("t", 20<<10)},
		{Kind: captcha.Image, Answer: strings.Repeat("a", 1025)},
		{Kind: captcha.Click, Points: []captcha.Point{{X: 1.1, Y: 0.5}}},
	} {
		t.Run("proof_"+string(proof.Kind), func(t *testing.T) {
			f, _ := newCaptchaModuleFixture(t, proof.Kind, "")
			id, cookie, _ := f.issue(t, "login")
			expect(t, request(f.h, "POST", "/api/v1/auth/login", f.login(id, proof), cookie, ""), 401)
			if f.probe.verifies.Load() != 0 {
				t.Fatal("unbounded or invalid proof reached the provider")
			}
			f.assertConsumed(t, id)
			f.assertSessions(t, 0)
		})
	}
}

func TestCaptchaModuleSchema167UpgradePreservesLegacyChallenge(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "captcha-upgrade-user", "customer")
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `ALTER TABLE auth_captcha_challenges DROP COLUMN provider_id, DROP COLUMN profile_id, DROP COLUMN profile_fingerprint, DROP COLUMN challenge_kind, DROP COLUMN provider_state;
DELETE FROM schema_migrations WHERE version LIKE '168_%';`); err != nil {
		t.Fatal(err)
	}
	id, nonce := strings.Repeat("b", 64), strings.Repeat("c", 64)
	seedCaptcha(t, db, id, "login", "ABC234", nonce, 5*time.Minute)
	for attempt := range 2 {
		if err := db.Migrate(t.Context()); err != nil {
			t.Fatalf("schema167 upgrade/repeat %d: %v", attempt, err)
		}
		if err := db.CheckSchema(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var provider, profile, fingerprint, kind, answer, nonceHash string
	var state []byte
	if err := db.Pool.QueryRow(t.Context(), "SELECT provider_id,profile_id,profile_fingerprint,challenge_kind,provider_state,answer_hash,nonce_hash FROM auth_captcha_challenges WHERE id=$1", tokenHash(id)).Scan(&provider, &profile, &fingerprint, &kind, &state, &answer, &nonceHash); err != nil {
		t.Fatal(err)
	}
	if provider != "builtin_png" || profile != "builtin" || fingerprint != "" || kind != "image" || len(state) != 0 || answer != tokenHash("ABC234") || nonceHash != tokenHash(nonce) {
		t.Fatal("schema upgrade changed the legacy proof or binding")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=true"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/auth/login", map[string]any{"identifier": "captcha-upgrade-user@example.test", "password": "integration-password", "captcha_id": id, "captcha_answer": " abc234 "}, captchaCookie+"="+nonce, ""), 200)
}
