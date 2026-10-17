package g09fixture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Request is a private fixture control message, never an HTTP/admin endpoint.
type Request struct {
	RunID    string `json:"runId"`
	Sequence uint64 `json:"sequence"`
	Action   string `json:"action"`
	Slot     *int   `json:"slot,omitempty"`
}
type Response struct {
	RunID    string `json:"runId"`
	Sequence uint64 `json:"sequence"`
	Action   string `json:"action"`
	OK       bool   `json:"ok"`
	Result   any    `json:"result,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ValidateRequest consumes no sequence on malformed input. The controller must
// retain the returned sequence even if execution fails, so mutations cannot replay.
func ValidateRequest(raw []byte, runID string, last uint64) (Request, error) {
	var r Request
	if len(raw) > 4096 {
		return r, errors.New("control request too large")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	// encoding/json otherwise silently keeps the last duplicate key. A private
	// control request must have exactly one interpretation across runtimes.
	keys := json.NewDecoder(bytes.NewReader(raw))
	opening, err := keys.Token()
	if err != nil || opening != json.Delim('{') {
		return r, errors.New("control request must be an object")
	}
	seen := map[string]bool{}
	for keys.More() {
		key, err := keys.Token()
		if err != nil {
			return r, errors.New("invalid control key")
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return r, errors.New("duplicate control key")
		}
		switch name {
		case "runId", "sequence", "action", "slot":
		default:
			return r, errors.New("unsupported control key")
		}
		seen[name] = true
		var value json.RawMessage
		if err := keys.Decode(&value); err != nil {
			return r, errors.New("invalid control value")
		}
	}
	if err := d.Decode(&r); err != nil {
		return r, errors.New("invalid control request")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return r, errors.New("trailing control data")
	}
	if runID == "" || r.RunID != runID || last == ^uint64(0) || r.Sequence != last+1 {
		return r, errors.New("control identity or sequence mismatch")
	}
	switch r.Action {
	case "prepare", "snapshot", "seal-cohorts", "verify-drain", "finish", "abort":
		if r.Slot != nil {
			return r, errors.New("unexpected worker slot")
		}
	case "start-workers", "stop-workers":
		if r.Slot == nil || *r.Slot < 0 || *r.Slot > 1 {
			return r, errors.New("worker slot must be zero or one")
		}
	default:
		return r, errors.New("unsupported control action")
	}
	return r, nil
}

func privateDir(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("absolute clean private directory required")
	}
	for parent := path; ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("private directory ancestors must not be symlinks")
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || st.Uid != uint32(os.Geteuid()) {
		return errors.New("private directory must be owned 0700 and not symlink")
	}
	return nil
}

// WritePrivateJSON creates the first manifest exclusively. ReplacePrivateJSON
// updates it atomically after checking its ownership/type/mode. Both sync data
// and the parent directory before returning; no plaintext belongs in public logs.
func WritePrivateJSON(path string, value any) error   { return writePrivateJSON(path, value, false) }
func ReplacePrivateJSON(path string, value any) error { return writePrivateJSON(path, value, true) }
func writePrivateJSON(path string, value any, replace bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("absolute clean path required")
	}
	if err := privateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if replace {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !ok || st.Uid != uint32(os.Geteuid()) {
			return errors.New("unsafe replacement target")
		}
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	var f *os.File
	if replace {
		f, err = os.CreateTemp(filepath.Dir(path), ".g09-*")
	} else {
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	}
	if err != nil {
		return err
	}
	name := f.Name()
	success := false
	defer func() {
		f.Close()
		if !success {
			os.Remove(name)
		}
	}()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if replace {
		if err = os.Rename(name, path); err != nil {
			return err
		}
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return fmt.Errorf("sync private directory: %w", err)
	}
	success = true
	return nil
}
