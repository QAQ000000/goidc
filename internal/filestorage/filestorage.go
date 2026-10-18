// Package filestorage contains compiled-in private object storage adapters.
// Profiles select code already linked into the binary; they never load code.
package filestorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/codex/zjmfgo/internal/module"
)

var (
	ErrConfiguration = errors.New("invalid file storage configuration")
	ErrInvalidKey    = errors.New("invalid file storage object key")
	ErrInvalidObject = errors.New("invalid file storage object metadata")
	ErrInvalidSeek   = errors.New("invalid file storage seek")
	ErrNotFound      = errors.New("file storage object not found")
	ErrAlreadyExists = errors.New("file storage object already exists")
	ErrIntegrity     = errors.New("file storage object integrity mismatch")
	ErrStorage       = errors.New("file storage operation failed")
)

type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

type Backend interface {
	// Put atomically publishes new content only when its digest matches the
	// supplied SHA-256. An existing key is never overwritten.
	Put(context.Context, string, []byte, string) error
	Open(context.Context, string, int64, string) (ReadSeekCloser, error)
	Delete(context.Context, string) error
}

type Profile struct {
	ID             string            `json:"id"`
	Implementation string            `json:"implementation"`
	Values         map[string]any    `json:"config"`
	Secrets        map[string]string `json:"secrets"`
}

type managedBackend struct {
	backend        Backend
	fingerprint    string
	implementation string
}

// ProfileSummary deliberately omits deployment locations and credentials.
type ProfileSummary struct {
	ID             string `json:"id"`
	Implementation string `json:"implementation"`
	Default        bool   `json:"default"`
}

// Manager is immutable after construction. Changing a default never changes
// the profile identity persisted alongside an existing object.
type Manager struct {
	defaultID string
	profiles  map[string]managedBackend
}

var profileID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var objectKey = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validateKey(key string) error {
	if !objectKey.MatchString(key) {
		return ErrInvalidKey
	}
	return nil
}

func validateObject(size int64, hash string) error {
	if size < 0 || !objectKey.MatchString(hash) {
		return ErrInvalidObject
	}
	return nil
}

func validatePayload(content []byte, expectedHash string) error {
	if !objectKey.MatchString(expectedHash) {
		return ErrInvalidObject
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != expectedHash {
		return ErrIntegrity
	}
	return nil
}

func canonicalLocalRoot(root string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", ErrConfiguration
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return "", ErrConfiguration
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrConfiguration
	}
	return canonical, nil
}

func locationFingerprint(implementation string, values map[string]any) (string, error) {
	var identity any
	switch implementation {
	case "local":
		root, ok := values["root"].(string)
		if !ok {
			return "", ErrConfiguration
		}
		root, err := canonicalLocalRoot(root)
		if err != nil {
			return "", err
		}
		identity = struct{ Implementation, Root string }{"local", root}
	case "s3":
		cfg, err := parseS3Config(values, nil, false)
		if err != nil {
			return "", err
		}
		identity = struct{ Implementation, Endpoint, Region, Bucket, Prefix string }{"s3", cfg.endpoint.String(), cfg.region, cfg.bucket, cfg.prefix}
	default:
		return "", ErrConfiguration
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return "", ErrConfiguration
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func NewManager(defaultID string, profiles []Profile) (*Manager, error) {
	if defaultID == "" {
		defaultID = "database"
	}
	if defaultID != "database" && !profileID.MatchString(defaultID) {
		return nil, ErrConfiguration
	}
	m := &Manager{defaultID: defaultID, profiles: make(map[string]managedBackend, len(profiles))}
	for _, profile := range profiles {
		if profile.ID == "database" || !profileID.MatchString(profile.ID) {
			return nil, ErrConfiguration
		}
		if _, exists := m.profiles[profile.ID]; exists {
			return nil, ErrConfiguration
		}
		values, secrets, err := normalizeConfiguration(profile.Implementation, module.Config{Values: profile.Values, Secrets: profile.Secrets})
		if err != nil {
			return nil, ErrConfiguration
		}
		fingerprint, err := locationFingerprint(profile.Implementation, values)
		if err != nil {
			return nil, ErrConfiguration
		}
		backend, err := backends.New(profile.Implementation, module.Config{Values: values, Secrets: secrets})
		if err != nil || backend == nil {
			return nil, ErrConfiguration
		}
		m.profiles[profile.ID] = managedBackend{backend: backend, fingerprint: fingerprint, implementation: profile.Implementation}
	}
	if defaultID != "database" {
		if _, exists := m.profiles[defaultID]; !exists {
			return nil, ErrConfiguration
		}
	}
	return m, nil
}

func (m *Manager) Default() (string, Backend, string, error) {
	if m == nil {
		return "", nil, "", ErrConfiguration
	}
	if m.defaultID == "database" {
		return "database", nil, "", nil
	}
	backend, fingerprint, err := m.Resolve(m.defaultID)
	return m.defaultID, backend, fingerprint, err
}

func (m *Manager) Resolve(id string) (Backend, string, error) {
	if m == nil {
		return nil, "", ErrConfiguration
	}
	profile, exists := m.profiles[id]
	if !exists {
		return nil, "", ErrConfiguration
	}
	return profile.backend, profile.fingerprint, nil
}

func (m *Manager) Summaries() []ProfileSummary {
	items := []ProfileSummary{}
	if m == nil {
		return items
	}
	for id, profile := range m.profiles {
		items = append(items, ProfileSummary{ID: id, Implementation: profile.implementation, Default: id == m.defaultID})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}
