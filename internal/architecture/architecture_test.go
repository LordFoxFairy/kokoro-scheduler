package architecture_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSchedulerArchitectureHasExplicitProductionLayers(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))
	for _, dir := range []string{
		"cmd/scheduler",
		"internal/domain",
		"internal/application",
		"internal/ports",
		"internal/adapters",
		"internal/transport",
	} {
		if info, err := os.Stat(filepath.Join(root, dir)); err != nil || !info.IsDir() {
			t.Fatalf("required layer %s is missing", dir)
		}
	}
	for _, file := range []string{"domain.go", "service.go", "dispatch.go", "internal_http.go"} {
		if _, err := os.Stat(filepath.Join(root, file)); err == nil {
			t.Fatalf("removed root implementation %s still exists", file)
		}
	}
}

func TestDomainDoesNotImportTransportOrInfrastructureSDKs(t *testing.T) {
	root := filepath.Join(filepath.Dir(mustCurrentFile(t)), "..", "domain")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		content := string(body)
		for _, forbidden := range []string{"redis/go-redis", "net/http", "database/sql", "grpc"} {
			if strings.Contains(content, forbidden) {
				t.Fatalf("domain file %s imports forbidden dependency %s", entry.Name(), forbidden)
			}
		}
	}
}

func mustCurrentFile(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("resolve test path")
	}
	return file
}

func TestApplicationAndPortsDoNotDependOnAdaptersOrTransport(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))
	for _, layer := range []string{"internal/application", "internal/ports"} {
		dir := filepath.Join(root, layer)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			content := string(body)
			for _, forbidden := range []string{"/internal/adapters/", "/internal/transport/"} {
				if strings.Contains(content, forbidden) {
					t.Fatalf("%s/%s depends on forbidden layer %s", layer, entry.Name(), forbidden)
				}
			}
		}
	}
}

func TestCanonicalOpenAPIContainsEveryHTTPRoute(t *testing.T) {
	root := filepath.Clean(filepath.Join(filepath.Dir(mustCurrentFile(t)), "../.."))
	body, err := os.ReadFile(filepath.Join(root, "contract/openapi/v1/openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	contract := string(body)
	for _, route := range []string{
		"/healthz:",
		"/readyz:",
		"/internal/scheduler/v1/jobs/{name}:",
		"/internal/scheduler/v1/jobs/{name}/pause:",
		"/internal/scheduler/v1/jobs/{name}/resume:",
	} {
		if !strings.Contains(contract, route) {
			t.Fatalf("canonical OpenAPI is missing route %s", route)
		}
	}
}
