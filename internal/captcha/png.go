package captcha

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"strings"
)

const captchaAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

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

// PNG returns the stateless built-in image provider. It has no credentials.
func PNG() Provider { return pngProvider{} }

type pngProvider struct{}

func (pngProvider) Issue(ctx context.Context, _ Request) (Issued, error) {
	if err := ctx.Err(); err != nil {
		return Issued{}, err
	}
	answer, err := captchaAnswer()
	if err != nil {
		return Issued{}, ErrUnavailable
	}
	image, err := captchaImage(answer)
	if err != nil {
		return Issued{}, ErrUnavailable
	}
	digest := sha256.Sum256([]byte(answer))
	return Issued{Challenge: Challenge{Kind: Image, Image: image, Width: 190, Height: 64, Prompt: "请输入图片中的 6 位字符"}, State: []byte(hex.EncodeToString(digest[:]))}, nil
}

func (pngProvider) Verify(ctx context.Context, _ Request, state []byte, proof Proof) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !ValidateProof(proof) || proof.Kind != Image || len(proof.Answer) > 64 || len(state) != 64 {
		return false, nil
	}
	answer := strings.ToUpper(strings.TrimSpace(proof.Answer))
	if len(answer) != 6 || strings.IndexFunc(answer, func(r rune) bool { return !strings.ContainsRune(captchaAlphabet, r) }) >= 0 {
		return false, nil
	}
	if _, err := hex.DecodeString(string(state)); err != nil {
		return false, nil
	}
	digest := sha256.Sum256([]byte(answer))
	return subtle.ConstantTimeCompare(state, []byte(hex.EncodeToString(digest[:]))) == 1, nil
}
