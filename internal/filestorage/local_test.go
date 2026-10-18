package filestorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func localTestHash(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func localTestBackend(t *testing.T) (Backend, string) {
	t.Helper()
	path := t.TempDir()
	backend, err := newLocal(path)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}
	return backend, path
}

func TestLocalRequiresExistingRealAbsoluteDirectory(t *testing.T) {
	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(real, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(parent, "missing", "nested")
	for _, path := range []string{"", "relative", missing, file, link, filepath.Join(link, "child"), real + string(filepath.Separator)} {
		if _, err := newLocal(path); !errors.Is(err, ErrConfiguration) {
			t.Errorf("configuration %q: got %v, want configuration error", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(parent, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("constructor created a directory: %v", err)
	}
	if _, err := newLocal(child); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(child)
	if err != nil || len(entries) != 0 {
		t.Fatalf("constructor changed directory contents: %v, %d entries", err, len(entries))
	}
}

func TestLocalRoundTripPermissionsSeekAndDelete(t *testing.T) {
	backend, path := localTestBackend(t)
	data := []byte("private attachment contents")
	key := localTestHash([]byte("opaque identifier"))
	if err := backend.Put(context.Background(), key, data, localTestHash(data)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(path, key))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Errorf("object mode = %v, want regular 0600", info.Mode())
	}
	reader, err := backend.Open(context.Background(), key, int64(len(data)), localTestHash(data))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("round trip: %q, %v", got, err)
	}
	if _, err := reader.Seek(8, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, data[8:]) {
		t.Fatalf("seek: %q, %v", got, err)
	}
	if _, err := reader.Seek(-1, io.SeekStart); !errors.Is(err, ErrInvalidSeek) {
		t.Errorf("negative seek: %v", err)
	}
	if _, err := reader.Seek(0, 99); !errors.Is(err, ErrInvalidSeek) {
		t.Errorf("invalid seek origin: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := backend.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if err := backend.Delete(context.Background(), key); err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
	if _, err := backend.Open(context.Background(), key, int64(len(data)), localTestHash(data)); !errors.Is(err, ErrNotFound) {
		t.Errorf("open deleted object: %v", err)
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Errorf("temporary or deleted object retained: %v, %d entries", err, len(entries))
	}
}

func TestLocalInterruptedUploadIsTrackedByKeyAndCollectedWithoutOverwrite(t *testing.T) {
	backend, directory := localTestBackend(t)
	key := localTestHash([]byte("interrupted-upload"))
	otherKey := localTestHash([]byte("another-upload"))
	temporary := filepath.Join(directory, ".upload-"+key)
	otherTemporary := filepath.Join(directory, ".upload-"+otherKey)
	partial := []byte("partial upload left after process exit")
	for _, filename := range []string{temporary, otherTemporary} {
		if err := os.WriteFile(filename, partial, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	content := []byte("complete file")
	if err := backend.Put(context.Background(), key, content, localTestHash(content)); err != ErrAlreadyExists {
		t.Fatalf("unconfirmed upload was overwritten: %v", err)
	}
	if got, err := os.ReadFile(temporary); err != nil || !bytes.Equal(got, partial) {
		t.Fatal("competing write changed the tracked temporary file")
	}
	if _, err := os.Lstat(filepath.Join(directory, key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted upload was incorrectly published")
	}
	if err := backend.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("collector did not remove the journal-associated temporary file")
	}
	if got, err := os.ReadFile(otherTemporary); err != nil || !bytes.Equal(got, partial) {
		t.Fatal("collector touched another upload's temporary file")
	}
	if err := backend.Put(context.Background(), key, content, localTestHash(content)); err != nil {
		t.Fatal(err)
	}
	// A crash after Link but before unlink can leave both names for one inode.
	if err := os.Link(filepath.Join(directory, key), temporary); err != nil {
		t.Fatal(err)
	}
	if err := backend.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{temporary, filepath.Join(directory, key)} {
		if _, err := os.Lstat(filename); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("collector did not remove both crash-leftover names")
		}
	}
	if err := backend.Delete(context.Background(), key); err != nil {
		t.Fatal("crash recovery delete was not idempotent")
	}
}

func TestLocalTemporaryNamesRejectLinksAndDirectories(t *testing.T) {
	for _, kind := range []string{"symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			backend, directory := localTestBackend(t)
			key := localTestHash([]byte(kind))
			temporary := filepath.Join(directory, ".upload-"+key)
			outside := filepath.Join(t.TempDir(), "untouched")
			content := []byte("unchanged outside data")
			if err := os.WriteFile(outside, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err := os.Symlink(outside, temporary); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(temporary, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := backend.Put(context.Background(), key, content, localTestHash(content)); err != ErrInvalidObject {
				t.Fatalf("special temporary file accepted: %v", err)
			}
			if err := backend.Delete(context.Background(), key); err != ErrInvalidObject {
				t.Fatalf("special temporary file was collected: %v", err)
			}
			if got, err := os.ReadFile(outside); err != nil || !bytes.Equal(got, content) {
				t.Fatal("temporary path escaped the root")
			}
		})
	}
}

func TestLocalRejectsInvalidKeysForEveryOperation(t *testing.T) {
	backend, path := localTestBackend(t)
	data := []byte("valid")
	for _, key := range []string{"", "../secret", "/absolute", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("a", 31) + "/" + strings.Repeat("b", 32), "bad\x00key"} {
		if err := backend.Put(context.Background(), key, data, localTestHash(data)); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Put(%q): %v", key, err)
		}
		if _, err := backend.Open(context.Background(), key, int64(len(data)), localTestHash(data)); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Open(%q): %v", key, err)
		}
		if err := backend.Delete(context.Background(), key); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Delete(%q): %v", key, err)
		}
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Errorf("invalid key changed store: %v, %d entries", err, len(entries))
	}
}

func TestLocalRejectsLinksDirectoriesAndSpecialFiles(t *testing.T) {
	// Unix socket names have a short platform limit; retain an opaque 64-byte
	// object key while keeping this task-owned fixture directory short.
	path, err := os.MkdirTemp("", "fs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(path) })
	backend, err := newLocal(path)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	data := []byte("do not change")
	if err := os.WriteFile(outside, data, 0o600); err != nil {
		t.Fatal(err)
	}
	inside := localTestHash([]byte("inside"))
	if err := os.WriteFile(filepath.Join(path, inside), data, 0o600); err != nil {
		t.Fatal(err)
	}
	kinds := []string{"outside-link", "inside-link", "dangling-link", "directory", "socket"}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			key := localTestHash([]byte(kind))
			objectPath := filepath.Join(path, key)
			switch kind {
			case "outside-link":
				if err := os.Symlink(outside, objectPath); err != nil {
					t.Fatal(err)
				}
			case "inside-link":
				if err := os.Symlink(inside, objectPath); err != nil {
					t.Fatal(err)
				}
			case "dangling-link":
				if err := os.Symlink("missing", objectPath); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(objectPath, 0o700); err != nil {
					t.Fatal(err)
				}
			case "socket":
				listener, err := net.Listen("unix", objectPath)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
			}
			if err := backend.Put(context.Background(), key, []byte("replace"), localTestHash([]byte("replace"))); !errors.Is(err, ErrInvalidObject) {
				t.Errorf("Put special file: %v", err)
			}
			if _, err := backend.Open(context.Background(), key, int64(len(data)), localTestHash(data)); !errors.Is(err, ErrInvalidObject) {
				t.Errorf("Open special file: %v", err)
			}
			if err := backend.Delete(context.Background(), key); !errors.Is(err, ErrInvalidObject) {
				t.Errorf("Delete special file: %v", err)
			}
			if _, err := os.Lstat(objectPath); err != nil {
				t.Errorf("special file was removed: %v", err)
			}
		})
	}
	got, err := os.ReadFile(outside)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("outside file changed: %q, %v", got, err)
	}
}

func TestLocalPutPublishesAtomicallyAndDoesNotOverwrite(t *testing.T) {
	backend, path := localTestBackend(t)
	key := localTestHash([]byte("race"))
	const count = 12
	const size = 512 * 1024
	start := make(chan struct{})
	results := make(chan struct {
		index int
		err   error
	}, count)
	var workers sync.WaitGroup
	for i := range count {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			results <- struct {
				index int
				err   error
			}{index, backend.Put(context.Background(), key, bytes.Repeat([]byte{byte(index)}, size), localTestHash(bytes.Repeat([]byte{byte(index)}, size)))}
		}(i)
	}
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(results)
		close(done)
	}()
	close(start)
	observing := true
	for observing {
		select {
		case <-done:
			observing = false
		default:
			info, err := os.Stat(filepath.Join(path, key))
			if err == nil && info.Size() != size {
				t.Fatalf("partially written object was published: size %d", info.Size())
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
	winner := -1
	for result := range results {
		if result.err == nil {
			if winner >= 0 {
				t.Fatal("multiple writers replaced the same object")
			}
			winner = result.index
		} else if !errors.Is(result.err, ErrAlreadyExists) {
			t.Fatalf("writer failed unexpectedly: %v", result.err)
		}
	}
	if winner < 0 {
		t.Fatal("no writer published an object")
	}
	got, err := os.ReadFile(filepath.Join(path, key))
	if err != nil || !bytes.Equal(got, bytes.Repeat([]byte{byte(winner)}, size)) {
		t.Fatalf("winning object was overwritten or incomplete: size %d, %v", len(got), err)
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 1 || entries[0].Name() != key {
		t.Fatalf("temporary files remain after concurrent writes: %v, %d entries", err, len(entries))
	}
}

func TestLocalOpenVerifiesSizeAndHash(t *testing.T) {
	backend, path := localTestBackend(t)
	data := []byte("verified bytes")
	key := localTestHash([]byte("integrity"))
	if err := backend.Put(context.Background(), key, data, localTestHash(data)); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int64{int64(len(data) - 1), int64(len(data) + 1)} {
		if _, err := backend.Open(context.Background(), key, size, localTestHash(data)); !errors.Is(err, ErrIntegrity) {
			t.Errorf("incorrect size %d: %v", size, err)
		}
	}
	if _, err := backend.Open(context.Background(), key, int64(len(data)), localTestHash([]byte("different"))); !errors.Is(err, ErrIntegrity) {
		t.Errorf("incorrect hash: %v", err)
	}
	if _, err := backend.Open(context.Background(), key, -1, localTestHash(data)); !errors.Is(err, ErrInvalidObject) {
		t.Errorf("negative size: %v", err)
	}
	if _, err := backend.Open(context.Background(), key, int64(len(data)), "invalid"); !errors.Is(err, ErrInvalidObject) {
		t.Errorf("invalid hash: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, key), bytes.Repeat([]byte{'x'}, len(data)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Open(context.Background(), key, int64(len(data)), localTestHash(data)); !errors.Is(err, ErrIntegrity) {
		t.Errorf("corrupt object: %v", err)
	}
}

func TestLocalOpenDoesNotBlockWhenObjectIsReplacedWithFIFO(t *testing.T) {
	backend, path := localTestBackend(t)
	data := []byte("complete regular object")
	key := localTestHash([]byte("FIFO replacement"))
	if err := backend.Put(context.Background(), key, data, localTestHash(data)); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	changed := make(chan error, 1)
	go func() {
		regular, fifo := filepath.Join(path, "replacement-file"), filepath.Join(path, "replacement-fifo")
		for {
			select {
			case <-stop:
				changed <- nil
				return
			default:
			}
			if err := os.WriteFile(regular, data, 0o600); err != nil {
				changed <- err
				return
			}
			if err := os.Rename(regular, filepath.Join(path, key)); err != nil {
				changed <- err
				return
			}
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				changed <- err
				return
			}
			if err := os.Rename(fifo, filepath.Join(path, key)); err != nil {
				changed <- err
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		if err := <-changed; err != nil {
			t.Errorf("replace fixture: %v", err)
		}
	})
	finished := make(chan error, 1)
	go func() {
		for range 300 {
			reader, err := backend.Open(context.Background(), key, int64(len(data)), localTestHash(data))
			if err != nil {
				if errors.Is(err, ErrInvalidObject) || errors.Is(err, ErrStorage) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrIntegrity) {
					continue
				}
				finished <- err
				return
			}
			got, readErr := io.ReadAll(reader)
			reader.Close()
			if readErr != nil {
				finished <- readErr
				return
			}
			if !bytes.Equal(got, data) {
				finished <- ErrIntegrity
				return
			}
		}
		finished <- nil
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a substituted FIFO blocked")
	}
}

func TestLocalCancellationPreservesObjectAndCancelsReader(t *testing.T) {
	backend, path := localTestBackend(t)
	data := []byte("keep me")
	key := localTestHash([]byte("cancel"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := backend.Put(ctx, key, data, localTestHash(data)); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled put: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled put wrote object: %v", err)
	}
	if err := backend.Put(context.Background(), key, data, localTestHash(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Open(ctx, key, int64(len(data)), localTestHash(data)); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled open: %v", err)
	}
	if err := backend.Delete(ctx, key); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled delete: %v", err)
	}
	readerCtx, readerCancel := context.WithCancel(context.Background())
	reader, err := backend.Open(readerCtx, key, int64(len(data)), localTestHash(data))
	if err != nil {
		t.Fatal(err)
	}
	readerCancel()
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled read: %v", err)
	}
	if _, err := reader.Seek(0, io.SeekStart); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled seek: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close remains available after cancellation: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(path, key))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("cancelled delete changed object: %q, %v", got, err)
	}
}

func TestLocalRejectsReplacedRootAndDoesNotExposePaths(t *testing.T) {
	for _, replacement := range []string{"directory", "same-directory-link", "missing"} {
		t.Run(replacement, func(t *testing.T) {
			backend, path := localTestBackend(t)
			key := localTestHash([]byte("root replacement"))
			data := []byte("original")
			if err := backend.Put(context.Background(), key, data, localTestHash(data)); err != nil {
				t.Fatal(err)
			}
			original := path + "-original"
			if err := os.Rename(path, original); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(original) })
			switch replacement {
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "same-directory-link":
				if err := os.Symlink(original, path); err != nil {
					t.Fatal(err)
				}
			}
			_, openErr := backend.Open(context.Background(), key, int64(len(data)), localTestHash(data))
			errorsToCheck := []error{
				backend.Put(context.Background(), localTestHash([]byte("new")), data, localTestHash(data)),
				openErr,
				backend.Delete(context.Background(), key),
			}
			for _, err := range errorsToCheck {
				if !errors.Is(err, ErrStorage) {
					t.Errorf("replaced root: %v", err)
				}
				if err != nil && (strings.Contains(err.Error(), path) || strings.Contains(err.Error(), key)) {
					t.Errorf("error exposed storage path: %v", err)
				}
			}
			got, err := os.ReadFile(filepath.Join(original, key))
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("old object changed: %q, %v", got, err)
			}
		})
	}
}
