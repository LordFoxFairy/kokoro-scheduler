package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestSchedulerArchitectureHasExplicitProductionLayers(t *testing.T) {
	root := repositoryRoot(t)
	for _, dir := range []string{
		"cmd/scheduler",
		"cmd/db-apply-schema",
		"internal/domain",
		"internal/application",
		"internal/ports",
		"internal/adapters",
		"internal/transport",
	} {
		if info, err := os.Stat(filepath.Join(root, dir)); err != nil || !info.IsDir() {
			t.Errorf("required layer %s is missing", dir)
		}
	}
	for _, removed := range []string{
		"domain.go",
		"service.go",
		"dispatch.go",
		"internal_http.go",
		"internal/adapters/cron",
		"internal/application/scheduler.go",
		"internal/domain/job.go",
	} {
		if _, err := os.Stat(filepath.Join(root, removed)); err == nil {
			t.Errorf("removed implementation %s still exists", removed)
		}
	}
}

func TestDomainUsesOnlyStandardLibraryAndApplicationDependsInward(t *testing.T) {
	root := repositoryRoot(t)
	walkGoFiles(t, filepath.Join(root, "internal/domain"), true, func(path string, file *ast.File) {
		for _, imported := range file.Imports {
			name, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(name, ".") {
				t.Errorf("domain source %s imports non-standard package %s", path, name)
			}
		}
	})
	for _, layer := range []string{"internal/application", "internal/ports"} {
		walkGoFiles(t, filepath.Join(root, layer), true, func(path string, file *ast.File) {
			for _, imported := range file.Imports {
				name, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(name, "/internal/adapters/") || strings.Contains(name, "/internal/transport/") {
					t.Errorf("%s depends on an outward layer: %s", path, name)
				}
			}
		})
	}
}

func TestGocronIsPinnedAndConfinedToConcreteWakeupAdapter(t *testing.T) {
	root := repositoryRoot(t)
	module, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	var directGocron bool
	for _, line := range strings.Split(string(module), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "github.com/go-co-op/gocron/v2 ") {
			if trimmed != "github.com/go-co-op/gocron/v2 v2.22.0" {
				t.Errorf("gocron dependency must be direct and pinned to v2.22.0, got %q", trimmed)
			}
			directGocron = true
		}
		legacyModule := strings.Join([]string{"github.com", "robfig", "cron", "v3"}, "/")
		if strings.HasPrefix(trimmed, legacyModule+" ") && !strings.HasSuffix(trimmed, "// indirect") {
			t.Errorf("legacy cron module may only remain as a gocron indirect dependency: %q", trimmed)
		}
	}
	if !directGocron {
		t.Fatal("gocron v2.22.0 is not a direct dependency")
	}

	legacyImport := strings.Join([]string{"github.com", "robfig", "cron", "v3"}, "/")
	gocronImport := "github.com/go-co-op/gocron/v2"
	var concreteAdapter bool
	walkGoFiles(t, root, false, func(path string, file *ast.File) {
		for _, imported := range file.Imports {
			name, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if name == legacyImport {
				t.Errorf("production source directly imports legacy cron: %s", path)
			}
			if name == gocronImport {
				adapterDir := filepath.Join(root, "internal/adapters/gocron") + string(filepath.Separator)
				if !strings.HasPrefix(path, adapterDir) {
					t.Errorf("gocron leaked outside wakeup adapter: %s", path)
				}
				concreteAdapter = true
			}
		}
	})
	if !concreteAdapter {
		t.Fatal("actual gocron wakeup adapter source is missing")
	}
}

func TestProductionFactsHaveNoInMemoryOrSilentSkipImplementation(t *testing.T) {
	root := repositoryRoot(t)
	walkGoFiles(t, root, false, func(path string, file *ast.File) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		content := string(body)
		if strings.Contains(content, "SkipIfStillRunning") {
			t.Errorf("production source silently skips overlapping work: %s", path)
		}
		if strings.Contains(content, "receipts map[") || strings.Contains(content, "schedules map[") || strings.Contains(content, "occurrences map[") {
			t.Errorf("production source keeps scheduler facts in a map: %s", path)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			typeSpec, ok := node.(*ast.TypeSpec)
			if !ok {
				return true
			}
			for _, fragment := range []string{"InMemory", "Fake", "Fixture"} {
				if strings.Contains(typeSpec.Name.Name, fragment) {
					t.Errorf("production test-double type %s remains in %s", typeSpec.Name.Name, path)
				}
			}
			return true
		})
	})
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))
}

func walkGoFiles(t *testing.T, root string, includeTests bool, inspect func(string, *ast.File)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || (!includeTests && (path == filepath.Join(root, "test") || strings.Contains(path, string(filepath.Separator)+"test"+string(filepath.Separator)))) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || (!includeTests && strings.HasSuffix(path, "_test.go")) {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		inspect(path, file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
