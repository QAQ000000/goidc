// Package captcha provides compiled-in challenge adapters. Providers issue and
// verify proofs; the authentication host owns browser binding, expiry, rate
// limits, and atomic single-use consumption.
package captcha

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	_ "golang.org/x/image/webp"
)

type Kind string

const (
	Image  Kind = "image"
	Click  Kind = "click"
	Widget Kind = "widget"

	MaxStateBytes     = 16 * 1024
	MaxChallengeBytes = 64 * 1024
	MaxProofBytes     = 16 * 1024
)

var (
	ErrConfiguration = errors.New("invalid captcha configuration")
	ErrChallenge     = errors.New("invalid captcha challenge")
	ErrUnavailable   = errors.New("captcha provider unavailable")
)

// Challenge contains only public presentation data. Credentials and expected
// answers belong in provider state or deployment configuration, never here.
type Challenge struct {
	Kind       Kind           `json:"kind"`
	Renderer   string         `json:"renderer,omitempty"`
	Image      string         `json:"image,omitempty"`
	Width      int            `json:"width,omitempty"`
	Height     int            `json:"height,omitempty"`
	Prompt     string         `json:"prompt,omitempty"`
	MaxPoints  int            `json:"max_points,omitempty"`
	PublicData map[string]any `json:"public_data,omitempty"`
}

// Point uses normalized image coordinates, inclusive of the edges [0,1].
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Proof is a discriminated union. Only the field matching Kind may be set.
// A provider must verify the proof itself; a browser success flag is no proof.
type Proof struct {
	Kind   Kind    `json:"kind"`
	Answer string  `json:"answer,omitempty"`
	Points []Point `json:"points,omitempty"`
	Token  string  `json:"token,omitempty"`
}

type Request struct {
	Action      string
	ChallengeID string
	ClientIP    string
	ExpiresAt   time.Time
}

type Issued struct {
	Challenge Challenge
	State     []byte
}

type Provider interface {
	Issue(context.Context, Request) (Issued, error)
	Verify(context.Context, Request, []byte, Proof) (bool, error)
}

var rendererID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// ValidateIssued bounds both persisted state and public output before either
// crosses the host boundary. Remote image/HTML/script URLs are not accepted;
// widgets select a renderer already compiled into the frontend.
func ValidateIssued(issued Issued) error {
	c := issued.Challenge
	if len(issued.State) > MaxStateBytes || len(c.Prompt) > 1024 || !utf8.ValidString(c.Prompt) || (c.Renderer != "" && !rendererID.MatchString(c.Renderer)) {
		return ErrChallenge
	}
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > MaxChallengeBytes {
		return ErrChallenge
	}
	switch c.Kind {
	case Image:
		if c.MaxPoints != 0 || !validImage(c) {
			return ErrChallenge
		}
	case Click:
		if c.MaxPoints < 1 || c.MaxPoints > 32 || !validImage(c) {
			return ErrChallenge
		}
	case Widget:
		if c.Renderer == "" || c.Image != "" || c.Width != 0 || c.Height != 0 || c.MaxPoints != 0 {
			return ErrChallenge
		}
	default:
		return ErrChallenge
	}
	return nil
}

func validImage(c Challenge) bool {
	if c.Width < 1 || c.Height < 1 || c.Width > 4096 || c.Height > 4096 {
		return false
	}
	var format, encoded string
	for _, candidate := range []string{"png", "jpeg", "webp"} {
		prefix := "data:image/" + candidate + ";base64,"
		if strings.HasPrefix(c.Image, prefix) {
			format, encoded = candidate, strings.TrimPrefix(c.Image, prefix)
			break
		}
	}
	if format == "" || len(encoded) > MaxChallengeBytes {
		return false
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) == 0 {
		return false
	}
	cfg, actual, err := image.DecodeConfig(bytes.NewReader(raw))
	return err == nil && actual == format && cfg.Width == c.Width && cfg.Height == c.Height
}

// ValidateProof enforces size and kind bounds without changing vendor data.
// In particular only the built-in PNG provider normalizes a text answer.
func ValidateProof(proof Proof) bool {
	raw, err := json.Marshal(proof)
	if err != nil || len(raw) > MaxProofBytes {
		return false
	}
	switch proof.Kind {
	case Image:
		return len(proof.Answer) <= 1024 && utf8.ValidString(proof.Answer) && strings.TrimSpace(proof.Answer) != "" && len(proof.Points) == 0 && proof.Token == ""
	case Click:
		if proof.Answer != "" || proof.Token != "" || len(proof.Points) < 1 || len(proof.Points) > 32 {
			return false
		}
		for _, p := range proof.Points {
			if math.IsNaN(p.X) || math.IsNaN(p.Y) || math.IsInf(p.X, 0) || math.IsInf(p.Y, 0) || p.X < 0 || p.X > 1 || p.Y < 0 || p.Y > 1 {
				return false
			}
		}
		return true
	case Widget:
		return proof.Answer == "" && len(proof.Points) == 0 && len(proof.Token) <= 12*1024 && utf8.ValidString(proof.Token) && strings.TrimSpace(proof.Token) != ""
	default:
		return false
	}
}
