package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const captchaCookie = "zjmfgo_captcha_nonce"
const captchaLifetime = 5 * time.Minute
const captchaAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

var errInvalidCaptcha = errors.New("invalid captcha")

var captchaGlyphs = map[byte][7]uint8{
	'2': {0x3e, 0x41, 0x01, 0x0e, 0x30, 0x40, 0x7f}, '3': {0x3e, 0x41, 0x06, 0x01, 0x41, 0x41, 0x3e},
	'4': {0x06, 0x0e, 0x16, 0x26, 0x7f, 0x06, 0x06}, '5': {0x7f, 0x40, 0x7e, 0x01, 0x01, 0x41, 0x3e},
	'6': {0x1e, 0x20, 0x40, 0x7e, 0x41, 0x41, 0x3e}, '7': {0x7f, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08},
	'8': {0x3e, 0x41, 0x41, 0x3e, 0x41, 0x41, 0x3e}, '9': {0x3e, 0x41, 0x41, 0x3f, 0x01, 0x02, 0x3c},
	'A': {0x1c, 0x22, 0x41, 0x7f, 0x41, 0x41, 0x41}, 'B': {0x7e, 0x41, 0x41, 0x7e, 0x41, 0x41, 0x7e},
	'C': {0x3e, 0x41, 0x40, 0x40, 0x40, 0x41, 0x3e}, 'D': {0x7c, 0x42, 0x41, 0x41, 0x41, 0x42, 0x7c},
	'E': {0x7f, 0x40, 0x40, 0x7e, 0x40, 0x40, 0x7f}, 'F': {0x7f, 0x40, 0x40, 0x7e, 0x40, 0x40, 0x40},
	'G': {0x3e, 0x41, 0x40, 0x4f, 0x41, 0x41, 0x3e}, 'H': {0x41, 0x41, 0x41, 0x7f, 0x41, 0x41, 0x41},
	'J': {0x1f, 0x02, 0x02, 0x02, 0x42, 0x42, 0x3c}, 'K': {0x41, 0x42, 0x44, 0x78, 0x44, 0x42, 0x41},
	'L': {0x40, 0x40, 0x40, 0x40, 0x40, 0x40, 0x7f}, 'M': {0x41, 0x63, 0x55, 0x49, 0x41, 0x41, 0x41},
	'N': {0x41, 0x61, 0x51, 0x49, 0x45, 0x43, 0x41}, 'P': {0x7e, 0x41, 0x41, 0x7e, 0x40, 0x40, 0x40},
	'Q': {0x3e, 0x41, 0x41, 0x41, 0x49, 0x42, 0x3d}, 'R': {0x7e, 0x41, 0x41, 0x7e, 0x44, 0x42, 0x41},
	'S': {0x3f, 0x40, 0x40, 0x3e, 0x01, 0x01, 0x7e}, 'T': {0x7f, 0x08, 0x08, 0x08, 0x08, 0x08, 0x08},
	'U': {0x41, 0x41, 0x41, 0x41, 0x41, 0x41, 0x3e}, 'V': {0x41, 0x41, 0x41, 0x41, 0x22, 0x22, 0x1c},
	'W': {0x41, 0x41, 0x49, 0x49, 0x55, 0x63, 0x41}, 'X': {0x41, 0x22, 0x14, 0x08, 0x14, 0x22, 0x41},
	'Y': {0x41, 0x22, 0x14, 0x08, 0x08, 0x08, 0x08}, 'Z': {0x7f, 0x02, 0x04, 0x08, 0x10, 0x20, 0x7f},
}

func captchaRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

func captchaAnswer() (string, error) {
	var b strings.Builder
	for i := 0; i < 6; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(captchaAlphabet))))
		if err != nil {
			return "", err
		}
		b.WriteByte(captchaAlphabet[n.Int64()])
	}
	return b.String(), nil
}

func captchaImage(answer string) (string, error) {
	const width, height, scale = 190, 64, 4
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{248, 250, 252, 255})
		}
	}
	for i := 0; i < 18; i++ {
		a, err := rand.Int(rand.Reader, big.NewInt(width))
		if err != nil {
			return "", err
		}
		b, err := rand.Int(rand.Reader, big.NewInt(height))
		if err != nil {
			return "", err
		}
		for j := 0; j < 18; j++ {
			x := int(a.Int64()) + j
			y := int(b.Int64()) + (j * int(a.Int64()) % 11) - 5
			if x >= 0 && x < width && y >= 0 && y < height {
				img.Set(x, y, color.RGBA{190, 205, 220, 130})
			}
		}
	}
	for i := 0; i < len(answer); i++ {
		glyph := captchaGlyphs[answer[i]]
		ox := 9 + i*30
		oy := 15 + (i%3)*2
		ink := color.RGBA{25, 63, 94, 255}
		for row, bits := range glyph {
			for col := 0; col < 7; col++ {
				if bits&(1<<uint(6-col)) == 0 {
					continue
				}
				for sy := 0; sy < scale; sy++ {
					for sx := 0; sx < scale; sx++ {
						img.Set(ox+col*scale+sx, oy+row*scale+sy, ink)
					}
				}
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(out.Bytes()), nil
}

func (s *Server) captchaEnabled(ctx context.Context) (bool, error) {
	var enabled bool
	err := s.db.Pool.QueryRow(ctx, "SELECT auth_captcha_enabled FROM site_settings WHERE id=1").Scan(&enabled)
	return enabled, err
}

func (s *Server) captchaUnavailable(w http.ResponseWriter, r *http.Request) {
	s.log.Error("captcha storage unavailable", "path", r.URL.Path)
	writeError(w, http.StatusServiceUnavailable, "captcha_unavailable", "验证码暂时不可用，请稍后重试")
}

func (s *Server) issueCaptcha(w http.ResponseWriter, r *http.Request) {
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
	answer, err := captchaAnswer()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	idRaw, err := captchaRandomBytes(32)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id := hex.EncodeToString(idRaw)
	nonceValue := ""
	if cookie, cookieErr := r.Cookie(captchaCookie); cookieErr == nil && validCaptchaHex(cookie.Value) {
		nonceValue = cookie.Value
	} else {
		nonce, randomErr := captchaRandomBytes(32)
		if randomErr != nil {
			s.fail(w, r, randomErr)
			return
		}
		nonceValue = hex.EncodeToString(nonce)
	}
	answerHash := sha256.Sum256([]byte(answer))
	nonceHash := sha256.Sum256([]byte(nonceValue))
	if _, err = s.db.Pool.Exec(r.Context(), `DELETE FROM auth_captcha_challenges WHERE ctid IN (SELECT ctid FROM auth_captcha_challenges WHERE expires_at<=now() ORDER BY expires_at LIMIT 100)`); err != nil {
		s.captchaUnavailable(w, r)
		return
	}
	if _, err = s.db.Pool.Exec(r.Context(), "INSERT INTO auth_captcha_challenges(id,action,answer_hash,nonce_hash,issued_ip,expires_at) VALUES($1,$2,$3,$4,$5,now()+interval '5 minutes')", tokenHash(id), action, hex.EncodeToString(answerHash[:]), hex.EncodeToString(nonceHash[:]), r.RemoteAddr); err != nil {
		s.captchaUnavailable(w, r)
		return
	}
	img, err := captchaImage(answer)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: captchaCookie, Value: nonceValue, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: int(captchaLifetime.Seconds())})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	write(w, 200, map[string]any{"enabled": true, "captcha_id": id, "image": img, "expires_at": time.Now().Add(captchaLifetime).UTC()})
}

func (s *Server) consumeCaptcha(ctx context.Context, r *http.Request, action, id, answer string) error {
	enabled, err := s.captchaEnabled(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	id = strings.TrimSpace(id)
	answer = strings.ToUpper(strings.TrimSpace(answer))
	if !validCaptchaHex(id) || len(answer) != 6 || strings.IndexFunc(answer, func(r rune) bool { return !strings.ContainsRune(captchaAlphabet, r) }) >= 0 {
		return errInvalidCaptcha
	}
	cookie, err := r.Cookie(captchaCookie)
	if err != nil || !validCaptchaHex(cookie.Value) {
		return errInvalidCaptcha
	}
	answerHash := sha256.Sum256([]byte(answer))
	nonceHash := sha256.Sum256([]byte(cookie.Value))
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var expected string
	err = tx.QueryRow(ctx, "UPDATE auth_captcha_challenges SET consumed_at=now() WHERE id=$1 AND action=$2 AND nonce_hash=$3 AND consumed_at IS NULL AND expires_at>now() RETURNING answer_hash", tokenHash(id), action, hex.EncodeToString(nonceHash[:])).Scan(&expected)
	if errors.Is(err, pgx.ErrNoRows) {
		return errInvalidCaptcha
	}
	if err != nil {
		return err
	}
	if !constantTimeHex(expected, hex.EncodeToString(answerHash[:])) {
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return errInvalidCaptcha
	}
	return tx.Commit(ctx)
}

func validCaptchaHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func constantTimeHex(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
