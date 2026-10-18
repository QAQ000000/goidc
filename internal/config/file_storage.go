package config

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/codex/zjmfgo/internal/filestorage"
)

// Locations are deployment configuration, never values supplied by an upload.
// Keep old database writes until an operator explicitly selects a profile.
func loadFileStorage() (*filestorage.Manager, error) {
	defaultID := strings.TrimSpace(env("FILE_STORAGE_DEFAULT", "database"))
	raw := strings.TrimSpace(os.Getenv("FILE_STORAGE_PROFILES"))
	var profiles []filestorage.Profile
	invalid := errors.New("FILE_STORAGE_PROFILES or FILE_STORAGE_DEFAULT is invalid")
	if raw != "" {
		if len(raw) > 128<<10 {
			return nil, invalid
		}
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&profiles); err != nil || len(profiles) > 32 || profiles == nil {
			return nil, invalid
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return nil, invalid
		}
	}
	manager, err := filestorage.NewManager(defaultID, profiles)
	if err != nil {
		return nil, invalid
	}
	return manager, nil
}
