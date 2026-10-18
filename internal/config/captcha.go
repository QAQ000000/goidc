package config

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/codex/zjmfgo/internal/captcha"
)

func loadCaptcha() (*captcha.Manager, error) {
	invalid := errors.New("CAPTCHA_PROFILES or CAPTCHA_DEFAULT is invalid")
	raw := strings.TrimSpace(os.Getenv("CAPTCHA_PROFILES"))
	var profiles []captcha.Profile
	if raw != "" {
		if len(raw) > 128<<10 {
			return nil, invalid
		}
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&profiles); err != nil || profiles == nil || len(profiles) > 32 {
			return nil, invalid
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return nil, invalid
		}
	}
	manager, err := captcha.NewManager(profiles, strings.TrimSpace(env("CAPTCHA_DEFAULT", "builtin")))
	if err != nil {
		return nil, invalid
	}
	return manager, nil
}
