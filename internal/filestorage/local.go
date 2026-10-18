package filestorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"syscall"
)

// localBackend stores opaque object names in an existing, explicitly configured
// directory. Directory identity is retained so replacing the configured path
// cannot silently redirect an existing profile to a different store.
type localBackend struct {
	path string
	info os.FileInfo
}

func newLocal(path string) (Backend, error) {
	path, err := canonicalLocalRoot(path)
	if err != nil {
		return nil, ErrConfiguration
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrConfiguration
	}
	backend := &localBackend{path: path, info: info}
	root, err := backend.openRoot()
	if err != nil {
		return nil, ErrConfiguration
	}
	if err := root.Close(); err != nil {
		return nil, ErrConfiguration
	}
	return backend, nil
}

func (b *localBackend) openRoot() (*os.Root, error) {
	resolved, err := filepath.EvalSymlinks(b.path)
	if err != nil || resolved != b.path {
		return nil, ErrStorage
	}
	info, err := os.Lstat(b.path)
	if err != nil || !info.IsDir() || !os.SameFile(b.info, info) {
		return nil, ErrStorage
	}
	root, err := os.OpenRoot(b.path)
	if err != nil {
		return nil, ErrStorage
	}
	opened, err := root.Lstat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(b.info, opened) {
		root.Close()
		return nil, ErrStorage
	}
	return root, nil
}

func (b *localBackend) Put(ctx context.Context, key string, data []byte, expectedHash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validatePayload(data, expectedHash); err != nil {
		return err
	}
	root, err := b.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Lstat(key); err == nil {
		if !info.Mode().IsRegular() {
			return ErrInvalidObject
		}
		return ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrStorage
	}

	// The journal's opaque key also identifies a crash-leftover temporary file.
	// A second writer cannot truncate it or publish different bytes for this key.
	temporary := ".upload-" + key
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			if info, statErr := root.Lstat(temporary); statErr == nil && !info.Mode().IsRegular() {
				return ErrInvalidObject
			}
			return ErrAlreadyExists
		}
		return ErrStorage
	}
	defer root.Remove(temporary)
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return ErrStorage
	}
	reader := &localContextReader{ctx: ctx, reader: bytes.NewReader(data)}
	if _, err := io.Copy(file, reader); err != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		return ErrStorage
	}
	if err := file.Sync(); err != nil {
		return ErrStorage
	}
	if err := file.Close(); err != nil {
		return ErrStorage
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// A hard link publishes the completed file atomically and never replaces an
	// existing object, unlike Rename on Unix.
	if err := root.Link(temporary, key); err != nil {
		if errors.Is(err, os.ErrExist) {
			if info, statErr := root.Lstat(key); statErr == nil && !info.Mode().IsRegular() {
				return ErrInvalidObject
			}
			return ErrAlreadyExists
		}
		return ErrStorage
	}
	if err := root.Remove(temporary); err != nil {
		return ErrStorage
	}
	if err := syncLocalDirectory(root); err != nil {
		// The object may already exist: retain the journal's uncertain state.
		return err
	}
	return ctx.Err()
}

func (b *localBackend) Open(ctx context.Context, key string, size int64, hash string) (ReadSeekCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}
	if err := validateObject(size, hash); err != nil {
		return nil, err
	}
	root, err := b.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	before, err := root.Lstat(key)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, ErrStorage
	}
	if !before.Mode().IsRegular() {
		return nil, ErrInvalidObject
	}
	// A special file can replace the object between Lstat and open. Nonblocking
	// mode keeps a substituted FIFO from hanging before we can verify its type.
	file, err := root.OpenFile(key, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if errors.Is(err, syscall.ELOOP) {
		return nil, ErrInvalidObject
	}
	if err != nil {
		return nil, ErrStorage
	}
	valid := false
	defer func() {
		if !valid {
			file.Close()
		}
	}()
	opened, err := file.Stat()
	if err != nil {
		return nil, ErrStorage
	}
	after, err := root.Lstat(key)
	if err != nil || !opened.Mode().IsRegular() || !after.Mode().IsRegular() ||
		!os.SameFile(before, opened) || !os.SameFile(after, opened) {
		return nil, ErrInvalidObject
	}
	if opened.Size() != size {
		return nil, ErrIntegrity
	}
	digest := sha256.New()
	limit := size
	if size < math.MaxInt64 {
		limit++
	}
	read, err := io.Copy(digest, &localContextReader{ctx: ctx, reader: io.LimitReader(file, limit)})
	if err != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, ErrStorage
	}
	if read != size || hex.EncodeToString(digest.Sum(nil)) != hash {
		return nil, ErrIntegrity
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, ErrStorage
	}
	valid = true
	return &localObjectReader{ctx: ctx, file: file}, nil
}

func (b *localBackend) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	root, err := b.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	names := []string{key, ".upload-" + key}
	for _, name := range names {
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return ErrStorage
		}
		if !info.Mode().IsRegular() {
			return ErrInvalidObject
		}
	}
	removed := false
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := root.Remove(name); err == nil {
			removed = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return ErrStorage
		}
	}
	if removed {
		if err := syncLocalDirectory(root); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func syncLocalDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return ErrStorage
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return ErrStorage
	}
	return nil
}

type localContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *localContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type localObjectReader struct {
	ctx  context.Context
	file *os.File
}

func (r *localObjectReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.file.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = ErrStorage
	}
	return n, err
}

func (r *localObjectReader) Seek(offset int64, whence int) (int64, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.file.Seek(offset, whence)
	if err != nil {
		return 0, ErrInvalidSeek
	}
	return n, nil
}

func (r *localObjectReader) Close() error {
	if err := r.file.Close(); err != nil {
		return ErrStorage
	}
	return nil
}
