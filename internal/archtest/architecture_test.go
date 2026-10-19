package archtest

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const baselinePath = "internal/archtest/testdata/http-persistence-baseline.json"

func projectRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate architecture test sources")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func command(root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = root
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w: %s", strings.Join(args[:min(len(args), 4)], " "), err, stderr.String())
	}
	return data, nil
}

func exportImporter(root string) (types.Importer, error) {
	// Compile metadata only: this never imports or initializes application code
	// in this process and never reads application configuration or a database.
	data, err := command(root, filepath.Join(root, "scripts/go.sh"), "list", "-mod=readonly", "-deps", "-export", "-json", "./internal/httpapi")
	if err != nil {
		return nil, err
	}
	paths := make(map[string]string)
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var pkg struct{ ImportPath, Export string }
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		paths[pkg.ImportPath] = pkg.Export
	}
	return importer.ForCompiler(token.NewFileSet(), "gc", func(name string) (io.ReadCloser, error) {
		filename, ok := paths[name]
		if !ok || filename == "" {
			return nil, fmt.Errorf("no compiled export data for %s", name)
		}
		return os.Open(filename)
	}), nil
}

func diskHTTPSources(root string) (map[string][]byte, error) {
	sources := make(map[string][]byte)
	err := filepath.WalkDir(filepath.Join(root, "internal/httpapi"), func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && entry.Name() == "testdata" {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in HTTP sources needs explicit architecture support: %s", name)
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		sources[filepath.ToSlash(relative)] = data
		return nil
	})
	return sources, err
}

func gitFiles(root, ref, prefix string) (map[string][]byte, error) {
	data, err := command(root, "git", "archive", "--format=tar", ref, "--", prefix)
	if err != nil {
		return nil, err
	}
	files := make(map[string][]byte)
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeDir || header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("unsupported Git source entry %s", header.Name)
		}
		contents, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		files[header.Name] = contents
	}
	return files, nil
}

var currentOnce sync.Once
var currentExceptions []exception
var currentError error

func currentBoundary(t *testing.T) []exception {
	t.Helper()
	currentOnce.Do(func() {
		root := projectRoot(t)
		var sources map[string][]byte
		sources, currentError = diskHTTPSources(root)
		if currentError != nil {
			return
		}
		var imp types.Importer
		imp, currentError = exportImporter(root)
		if currentError == nil {
			currentExceptions, currentError = analyzeHTTP(sources, imp)
		}
	})
	if currentError != nil {
		t.Fatal(currentError)
	}
	return currentExceptions
}

func TestHTTPPersistenceBoundary(t *testing.T) {
	actual := currentBoundary(t)
	data, err := os.ReadFile(filepath.Join(projectRoot(t), baselinePath))
	if err != nil {
		t.Fatal(err)
	}
	b, err := decodeBaseline(data)
	if err != nil {
		t.Fatal(err)
	}
	if failures := compareExceptions(b.Entries, actual); len(failures) != 0 {
		t.Fatalf("new/changed HTTP persistence access; extract it into Store:\n%s", strings.Join(failures, "\n"))
	}
	if stale := compareExceptions(actual, b.Entries); len(stale) != 0 {
		t.Fatalf("remove obsolete architecture exceptions; they cannot be retained for reuse:\n%s", strings.Join(stale, "\n"))
	}
	t.Logf("verified %d scoped HTTP persistence exceptions", len(actual))
}

func TestHTTPBaselineOnlyShrinks(t *testing.T) {
	root := projectRoot(t)
	ref := os.Getenv("GOIDC_ARCH_BASE_REF")
	if ref == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("CI requires GOIDC_ARCH_BASE_REF from the PR base or push.before commit")
		}
		ref = "HEAD"
	}
	// Resolve to a commit before using it in any archive/path lookup. A missing
	// ref or failed Git command must never be treated as a bootstrap exception.
	resolved, err := command(root, "git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		t.Fatal(err)
	}
	ref = strings.TrimSpace(string(resolved))
	oldFiles, err := gitFiles(root, ref, "internal")
	if err != nil {
		t.Fatal(err)
	}
	var allowed []exception
	if data, exists := oldFiles[baselinePath]; exists {
		b, err := decodeBaseline(data)
		if err != nil {
			t.Fatalf("invalid historical architecture baseline: %v", err)
		}
		allowed = b.Entries
	} else {
		// First introduction: derive the ceiling from the actual base sources,
		// rather than accepting any baseline generated from changed sources.
		sources := make(map[string][]byte)
		for name, data := range oldFiles {
			if strings.HasPrefix(name, "internal/httpapi/") && !strings.Contains(name, "/testdata/") && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
				sources[name] = data
			}
		}
		imp, err := exportImporter(root)
		if err != nil {
			t.Fatal(err)
		}
		allowed, err = analyzeHTTP(sources, imp)
		if err != nil {
			t.Fatalf("cannot establish initial architecture ceiling from base %s: %v", ref, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, baselinePath))
	if err != nil {
		t.Fatal(err)
	}
	b, err := decodeBaseline(data)
	if err != nil {
		t.Fatal(err)
	}
	if failures := compareExceptions(allowed, b.Entries); len(failures) != 0 {
		t.Fatalf("architecture baseline expanded relative to %s:\n%s", ref, strings.Join(failures, "\n"))
	}
}

func importViolations(files map[string][]byte) ([]string, error) {
	var violations []string
	for name, data := range files {
		if strings.HasPrefix(name, "internal/httpapi/") || strings.Contains(name, "/testdata/") || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, data, parser.ImportsOnly)
		if err != nil {
			return nil, err
		}
		for _, item := range file.Imports {
			nameOfImport, err := strconv.Unquote(item.Path.Value)
			if err != nil {
				return nil, err
			}
			if nameOfImport == modulePath+"/internal/httpapi" || strings.HasPrefix(nameOfImport, modulePath+"/internal/httpapi/") {
				violations = append(violations, name+": core packages must not import "+nameOfImport)
			}
		}
	}
	sort.Strings(violations)
	return violations, nil
}

func TestInternalPackagesDoNotDependOnHTTP(t *testing.T) {
	root := projectRoot(t)
	files := make(map[string][]byte)
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && entry.Name() == "testdata" {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in internal source needs explicit architecture support: %s", name)
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	violations, err := importViolations(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatal(strings.Join(violations, "\n"))
	}
}

func TestGitBaselineSnapshotCannotBeExpandedByWorkingTree(t *testing.T) {
	root := t.TempDir()
	if _, err := command(root, "git", "init", "-q"); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, baselinePath)
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	entry := exception{File: "internal/httpapi/fixture.go", Function: "handler", Operation: "driver-method:Exec", Fingerprint: strings.Repeat("a", 64), Count: 1}
	write := func() {
		t.Helper()
		data, err := json.Marshal(baseline{Version: 1, Entries: []exception{entry}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if _, err := command(root, "git", "add", "--", baselinePath); err != nil {
		t.Fatal(err)
	}
	if _, err := command(root, "git", "-c", "user.name=Architecture fixture", "-c", "user.email=architecture@example.test", "-c", "commit.gpgsign=false", "commit", "-qm", "initial baseline"); err != nil {
		t.Fatal(err)
	}
	entry.Count++
	write()
	files, err := gitFiles(root, "HEAD", "internal")
	if err != nil {
		t.Fatal(err)
	}
	old, err := decodeBaseline(files[baselinePath])
	if err != nil {
		t.Fatal(err)
	}
	if violations := compareExceptions(old.Entries, []exception{entry}); len(violations) == 0 {
		t.Fatal("expanded working-tree baseline replaced the committed ceiling")
	}
	if _, err := gitFiles(root, strings.Repeat("0", 40), "internal"); err == nil {
		t.Fatal("missing Git reference must fail rather than allow a bootstrap baseline")
	}
}
