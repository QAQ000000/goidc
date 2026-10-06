package legacy

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/fileformat"
)

func DownloadTextValid(raw string, max int, multiline bool) bool {
	if !utf8.ValidString(raw) || utf8.RuneCountInString(raw) > max {
		return false
	}
	for _, r := range raw {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t')) {
			return false
		}
	}
	return true
}
func DownloadFilenameValid(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\") && DownloadTextValid(name, 255, false)
}

// Linux deployment: every directory component below the operator-provided root
// and the final file is opened with O_NOFOLLOW. A source symlink cannot cross
// customer directories, and FIFO/device inputs cannot block or be copied.
func ReadDownloadFile(uploadRoot string, customerID int64, stored, display string) ([]byte, string, error) {
	dirs := []string{"support"}
	if customerID > 0 {
		dirs = []string{"clients", strconv.FormatInt(customerID, 10)}
	}
	return readLegacyFile(uploadRoot, dirs, stored, display, 50<<20, fileformat.DownloadMIME)
}
func ReadTicketFile(uploadRoot, stored, display string) ([]byte, string, error) {
	return readLegacyFile(uploadRoot, []string{"common", "ticket"}, stored, display, 10<<20, func(name string, b []byte) (string, bool) { return fileformat.TicketMIME(name, "", b) })
}
func readLegacyFile(uploadRoot string, dirs []string, stored, display string, limit int64, check func(string, []byte) (string, bool)) ([]byte, string, error) {
	if !DownloadFilenameValid(stored) || !DownloadFilenameValid(display) {
		return nil, "", fmt.Errorf("invalid filename")
	}
	if uploadRoot == "" {
		return nil, "", fmt.Errorf("legacy upload root required")
	}
	root, err := os.OpenRoot(uploadRoot)
	if err != nil {
		return nil, "", fmt.Errorf("legacy upload root unavailable")
	}
	defer root.Close()
	dir, err := root.OpenFile(dirs[0], os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", fmt.Errorf("legacy file directory unavailable")
	}
	defer dir.Close()
	fd := int(dir.Fd())
	for _, component := range dirs[1:] {
		next, err := syscall.Openat(fd, component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			return nil, "", fmt.Errorf("legacy customer directory unavailable")
		}
		child := os.NewFile(uintptr(next), "legacy-customer-directory")
		defer child.Close()
		fd = next
	}
	fileFD, err := syscall.Openat(fd, stored, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", fmt.Errorf("legacy file unavailable or unsafe")
	}
	file := os.NewFile(uintptr(fileFD), "legacy-file")
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > limit {
		return nil, "", fmt.Errorf("legacy file must be regular and within size limit")
	}
	b, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("legacy file read failed")
	}
	after, err := file.Stat()
	if err != nil || int64(len(b)) != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, "", fmt.Errorf("legacy file changed during read")
	}
	mt, valid := check(display, b)
	if !valid {
		return nil, "", fmt.Errorf("legacy file extension/content not supported")
	}
	return b, mt, nil
}
