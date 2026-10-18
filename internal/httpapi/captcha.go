package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/captcha"
	"github.com/jackc/pgx/v5"
)

const captchaCookie = "zjmfgo_captcha_nonce"
const captchaLifetime = 5 * time.Minute
const captchaProviderTimeout = 8 * time.Second

var errInvalidCaptcha = errors.New("invalid captcha")

func captchaRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

func (s *Server) captchaManager() (*captcha.Manager, error) {
	if s.cfg.Captcha != nil {
		return s.cfg.Captcha, nil
	}
	return captcha.NewManager(nil, captcha.BuiltinProfileID)
}

func (s *Server) captchaEnabled(ctx context.Context) (bool, error) {
	var enabled bool
	err := s.db.Pool.QueryRow(ctx, "SELECT auth_captcha_enabled FROM site_settings WHERE id=1").Scan(&enabled)
	return enabled, err
}

func (s *Server) captchaUnavailable(w http.ResponseWriter, r *http.Request) {
	s.log.Error("captcha unavailable", "path", r.URL.Path)
	writeError(w, http.StatusServiceUnavailable, "captcha_unavailable", "验证码暂时不可用，请稍后重试")
}

func (s *Server) issueCaptcha(w http.ResponseWriter, r *http.Request) {
	// Disabled responses and errors must not be cached either.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	action := r.URL.Query().Get("action")
	if action != "login" && action != "register" && action != "forgot-password" {
		writeError(w, 400, "invalid_captcha", "验证码请求无效")
		return
	}
	enabled, err := s.captchaEnabled(r.Context())
	if err != nil {
		s.captchaUnavailable(w, r)
		return
	}
	if !enabled {
		write(w, 200, map[string]any{"enabled": false})
		return
	}
	if !s.authLimit(w, r, "captcha-issue:"+action, 12) {
		return
	}
	manager, err := s.captchaManager()
	if err != nil {
		s.captchaUnavailable(w, r)
		return
	}
	binding, provider, err := manager.Active()
	if err != nil {
		s.captchaUnavailable(w, r)
		return
	}
	idRaw, err := captchaRandomBytes(32)
	if err != nil {
		s.captchaUnavailable(w, r)
		return
	}
	id := hex.EncodeToString(idRaw)
	nonceValue := ""
	if cookie, cookieErr := r.Cookie(captchaCookie); cookieErr == nil && validCaptchaHex(cookie.Value) {
		nonceValue = cookie.Value
	} else {
		nonce, randomErr := captchaRandomBytes(32)
		if randomErr != nil {
			s.captchaUnavailable(w, r)
			return
		}
		nonceValue = hex.EncodeToString(nonce)
	}
	expires := time.Now().Add(captchaLifetime).UTC()
	providerCtx, cancel := context.WithTimeout(r.Context(), captchaProviderTimeout)
	issued, err := provider.Issue(providerCtx, captcha.Request{Action: action, ChallengeID: id, ClientIP: s.clientIP(r), ExpiresAt: expires})
	contextErr := providerCtx.Err()
	cancel()
	if err != nil || contextErr != nil || captcha.ValidateIssued(issued) != nil {
		s.captchaUnavailable(w, r)
		return
	}
	if _, err = s.db.Pool.Exec(r.Context(), `DELETE FROM auth_captcha_challenges WHERE ctid IN (SELECT ctid FROM auth_captcha_challenges WHERE expires_at<=now() ORDER BY expires_at LIMIT 100)`); err != nil {
		s.captchaUnavailable(w, r)
		return
	}
	// Preserve the old answer_hash column for migration/old issued challenges.
	if issued.State == nil {
		issued.State = []byte{}
	}
	answerHash := tokenHash(string(issued.State))
	if binding.ImplementationID == captcha.BuiltinImplementationID {
		answerHash = string(issued.State)
		issued.State = []byte{}
	}
	_, err = s.db.Pool.Exec(r.Context(), `INSERT INTO auth_captcha_challenges(id,action,answer_hash,nonce_hash,issued_ip,expires_at,provider_id,profile_id,profile_fingerprint,challenge_kind,provider_state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, tokenHash(id), action, answerHash, tokenHash(nonceValue), s.clientIP(r), expires, binding.ImplementationID, binding.ProfileID, binding.Fingerprint, string(issued.Challenge.Kind), issued.State)
	if err != nil {
		s.captchaUnavailable(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: captchaCookie, Value: nonceValue, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: int(captchaLifetime.Seconds())})
	out := map[string]any{"enabled": true, "captcha_id": id, "expires_at": expires, "challenge": issued.Challenge}
	if issued.Challenge.Kind == captcha.Image {
		out["image"] = issued.Challenge.Image
	}
	write(w, 200, out)
}

// Claim durably before invoking a possibly remote verifier. Failed, timed out,
// cancelled or crashed verifications require a fresh challenge, never a retry.
// No transaction/row lock is held across provider I/O.
func (s *Server) consumeCaptcha(ctx context.Context, r *http.Request, action, id, answer string, proofs ...*captcha.Proof) error {
	enabled, err := s.captchaEnabled(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	id = strings.TrimSpace(id)
	if !validCaptchaHex(id) {
		return errInvalidCaptcha
	}
	cookie, err := r.Cookie(captchaCookie)
	if err != nil || !validCaptchaHex(cookie.Value) {
		return errInvalidCaptcha
	}
	var binding captcha.Binding
	var expected, kind string
	var state []byte
	var expires time.Time
	err = s.db.Pool.QueryRow(ctx, `UPDATE auth_captcha_challenges SET consumed_at=now() WHERE id=$1 AND action=$2 AND nonce_hash=$3 AND consumed_at IS NULL AND expires_at>now() RETURNING provider_id,profile_id,profile_fingerprint,challenge_kind,provider_state,answer_hash,expires_at`, tokenHash(id), action, tokenHash(cookie.Value)).Scan(&binding.ImplementationID, &binding.ProfileID, &binding.Fingerprint, &kind, &state, &expected, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return errInvalidCaptcha
	}
	if err != nil {
		return err
	}
	var proof captcha.Proof
	if len(proofs) > 0 && proofs[0] != nil {
		proof = *proofs[0]
	} else {
		if binding.ImplementationID != captcha.BuiltinImplementationID || kind != string(captcha.Image) {
			return errInvalidCaptcha
		}
		proof = captcha.Proof{Kind: captcha.Image, Answer: answer}
	}
	if string(proof.Kind) != kind || !captcha.ValidateProof(proof) {
		return errInvalidCaptcha
	}
	if binding.ImplementationID == captcha.BuiltinImplementationID {
		state = []byte(expected)
	}
	manager, err := s.captchaManager()
	if err != nil {
		return err
	}
	provider, err := manager.Resolve(binding)
	if err != nil {
		return err
	}
	providerCtx, cancel := context.WithTimeout(ctx, captchaProviderTimeout)
	defer cancel()
	valid, err := provider.Verify(providerCtx, captcha.Request{Action: action, ChallengeID: id, ClientIP: s.clientIP(r), ExpiresAt: expires}, state, proof)
	if err != nil {
		return captcha.ErrUnavailable
	}
	if providerCtx.Err() != nil {
		return providerCtx.Err()
	}
	if !valid || !time.Now().Before(expires) {
		return errInvalidCaptcha
	}
	return nil
}

func validCaptchaHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
