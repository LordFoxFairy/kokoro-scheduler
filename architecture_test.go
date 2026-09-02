package scheduler

import (
	"os"
	"strings"
	"testing"
)

func TestApplicationLayersAreExplicit(t *testing.T) {
	for _, name := range []string{"domain.go", "dispatch.go", "service.go", "internal_http.go"} {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("missing scheduler layer %s: %v", name, err)
		}
	}
	for _, name := range []string{"domain.go", "dispatch.go", "service.go", "internal_http.go"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "mysql") || strings.Contains(string(body), "mongodb") || strings.Contains(string(body), "billing") {
			t.Fatalf("generic scheduler layer %s contains a business/database dependency", name)
		}
	}
}
