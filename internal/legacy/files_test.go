package legacy

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestReadDownloadFileConfinementAndContent(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"support", "clients/10", "clients/11"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	put := func(path string, b []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	put("support/old.txt", []byte("公开旧文件\n"))
	put("clients/10/uuid^中文.txt", []byte("客户十"))
	put("clients/11/secret.txt", []byte("客户十一"))
	for _, c := range []struct {
		uid                   int64
		stored, display, want string
	}{{0, "old.txt", "原文件.txt", "公开旧文件\n"}, {10, "uuid^中文.txt", "中文.txt", "客户十"}} {
		b, mt, err := ReadDownloadFile(root, c.uid, c.stored, c.display)
		if err != nil || string(b) != c.want || mt != "text/plain; charset=utf-8" {
			t.Fatalf("valid file: %s %v", mt, err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "clients/11/secret.txt"), filepath.Join(root, "clients/10/link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "clients/11"), filepath.Join(root, "clients/12")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "support/fifo.txt"), 0600); err != nil {
		t.Fatal(err)
	}
	put("support/empty.txt", nil)
	put("support/fake.png", []byte("not an image"))
	put("support/script.txt", []byte("<html><script>alert(1)</script></html>"))
	large, err := os.Create(filepath.Join(root, "support/huge.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err = large.Truncate((50 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	large.Close()
	for _, c := range []struct {
		name            string
		uid             int64
		stored, display string
	}{
		{"traversal", 0, "../clients/11/secret.txt", "a.txt"}, {"absolute", 0, "/etc/passwd", "a.txt"}, {"backslash", 0, "..\\secret.txt", "a.txt"},
		{"display-path", 0, "old.txt", "../a.txt"}, {"missing", 0, "missing.txt", "a.txt"}, {"foreign-owner", 10, "secret.txt", "a.txt"},
		{"file-symlink", 10, "link.txt", "a.txt"}, {"customer-symlink", 12, "secret.txt", "a.txt"}, {"fifo", 0, "fifo.txt", "a.txt"},
		{"empty", 0, "empty.txt", "a.txt"}, {"size", 0, "huge.txt", "a.txt"}, {"signature", 0, "fake.png", "a.png"}, {"html", 0, "script.txt", "a.txt"},
		{"unsupported", 0, "old.txt", "a.php"}, {"control", 0, "old.txt", "a\n.txt"}, {"dot", 0, ".", "a.txt"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := ReadDownloadFile(root, c.uid, c.stored, c.display); err == nil || strings.Contains(err.Error(), root) {
				t.Fatalf("unsafe file accepted or path leaked: %v", err)
			}
		})
	}
	if err := os.Rename(filepath.Join(root, "support"), filepath.Join(root, "saved-support")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "saved-support"), filepath.Join(root, "support")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ReadDownloadFile(root, 0, "old.txt", "a.txt"); err == nil {
		t.Fatal("support symlink accepted")
	}
	if _, _, err = ReadDownloadFile("", 0, "old.txt", "a.txt"); err == nil {
		t.Fatal("unconfigured root accepted")
	}
}
