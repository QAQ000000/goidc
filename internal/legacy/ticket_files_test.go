package legacy

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadTicketFileDirectoryConfinementAndBounds(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "common/ticket")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old^中文.txt"), []byte("工单附件\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b, mt, err := ReadTicketFile(root, "old^中文.txt", "中文.txt")
	if err != nil || string(b) != "工单附件\n" || mt != "text/plain; charset=utf-8" {
		t.Fatal("text charset", err)
	}
	if err := os.Symlink(filepath.Join(dir, "old^中文.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.txt"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "huge.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate((10 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, name := range []string{"../escape.txt", "link.txt", "pipe.txt", "huge.txt"} {
		if _, _, err = ReadTicketFile(root, name, "a.txt"); err == nil {
			t.Fatal("unsafe ticket input accepted", name)
		}
	}
	if err = os.Rename(dir, filepath.Join(root, "common/saved")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(root, "common/saved"), dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ReadTicketFile(root, "old^中文.txt", "a.txt"); err == nil {
		t.Fatal("ticket directory symlink accepted")
	}
	root2 := t.TempDir()
	if err = os.Symlink(filepath.Join(root, "common"), filepath.Join(root2, "common")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ReadTicketFile(root2, "old^中文.txt", "a.txt"); err == nil {
		t.Fatal("common directory symlink accepted")
	}
}
