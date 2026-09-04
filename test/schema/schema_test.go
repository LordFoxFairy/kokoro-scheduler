package schema_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCanonicalSchemaOwnsDurableSchedulerFacts(t *testing.T) {
	root := repositoryRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "database/schema.sql"))
	if err != nil {
		t.Fatalf("read canonical scheduler schema: %v", err)
	}
	schema := strings.ToLower(string(body))
	for _, table := range []string{
		"scheduler_schedule",
		"scheduler_occurrence",
		"scheduler_command_receipt",
		"scheduler_dispatch_outbox",
	} {
		if !strings.Contains(schema, "create table if not exists "+table) {
			t.Errorf("canonical schema is missing table %s", table)
		}
	}
	if got := strings.Count(schema, "tenant_id text not null"); got != 4 {
		t.Errorf("schema has %d required tenant columns, want 4", got)
	}
	if !strings.Contains(schema, "timestamptz(3)") || strings.Contains(schema, "timestamp without time zone") {
		t.Error("scheduler instants must use TIMESTAMPTZ(3)")
	}
	for _, forbidden := range []string{"foreign key", "references "} {
		if strings.Contains(schema, forbidden) {
			t.Errorf("canonical schema contains forbidden %q", forbidden)
		}
	}
	for _, required := range []string{
		"uq_scheduler_schedule_tenant_name",
		"uq_scheduler_occurrence_identity",
		"uq_scheduler_command_receipt_identity",
		"uq_scheduler_dispatch_outbox_occurrence",
		"ix_scheduler_schedule_due",
		"ix_scheduler_occurrence_tenant_schedule",
		"ix_scheduler_dispatch_outbox_ready",
		"check (status in ('active', 'paused'))",
		"check (misfire_policy in ('skip', 'fire_once', 'catch_up_bounded'))",
	} {
		if !strings.Contains(schema, required) {
			t.Errorf("canonical schema is missing invariant %q", required)
		}
	}
}

func TestPostgresDueAndOutboxClaimsUseSkipLockedAndTenantPredicates(t *testing.T) {
	root := repositoryRoot(t)
	dir := filepath.Join(root, "internal/adapters/postgres")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		source.Write(body)
	}
	upper := strings.ToUpper(source.String())
	if strings.Count(upper, "FOR UPDATE SKIP LOCKED") < 2 {
		t.Fatal("PostgreSQL schedule and outbox claims must both use FOR UPDATE SKIP LOCKED")
	}
	for _, file := range []string{"commands.go", "due.go", "outbox.go"} {
		body, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "tenant_id") {
			t.Errorf("PostgreSQL adapter %s has no explicit tenant predicate/write", file)
		}
	}
}

func TestSchemaIsUniqueAndHasFreshDatabaseCommand(t *testing.T) {
	root := repositoryRoot(t)
	var sqlFiles []string
	if err := filepath.WalkDir(filepath.Join(root, "database"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".sql" {
			sqlFiles = append(sqlFiles, filepath.Base(path))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(sqlFiles) != 1 || sqlFiles[0] != "schema.sql" {
		t.Fatalf("canonical SQL files = %#v, want only database/schema.sql", sqlFiles)
	}
	if _, err := os.Stat(filepath.Join(root, "database/migrations")); !os.IsNotExist(err) {
		t.Fatal("database/migrations must not exist")
	}
	for _, path := range []string{"cmd/db-apply-schema/main.go", "scripts/db-apply-schema"} {
		if info, err := os.Stat(filepath.Join(root, path)); err != nil || info.IsDir() {
			t.Errorf("db:apply-schema entry %s is missing", path)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve schema test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))
}
