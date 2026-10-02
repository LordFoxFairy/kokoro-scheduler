package integration_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	postgresadapter "github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/postgres"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/recurrence"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/application"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/config"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
	transporthttp "github.com/LordFoxFairy/kokoro-scheduler/internal/transport/http"
	"github.com/LordFoxFairy/kokoro-scheduler/test/doubles"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestApplySchemaInstallsOnlyIntoAnEmptyDatabaseNamespace(t *testing.T) {
	rawURL := os.Getenv("SCHEDULER_DATABASE_TEST_URL")
	if rawURL == "" {
		t.Skip("SCHEDULER_DATABASE_TEST_URL is not configured")
	}
	ctx := context.Background()
	schemaName := fmt.Sprintf("scheduler_bootstrap_%d", time.Now().UnixNano())
	target := namespaceBoundaryTarget(t, rawURL, schemaName)
	admin, err := pgxpool.New(ctx, target.DriverURL())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	identifier := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE") }()

	poolConfig, err := pgxpool.ParseConfig(target.DriverURL())
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := postgresadapter.ApplySchemaToEmptyDatabase(ctx, pool, databaseTargetForPool(t, pool)); err != nil {
		t.Fatalf("apply fresh schema: %v", err)
	}
	var tableCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname = current_schema()`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 4 {
		t.Fatalf("installed tables = %d, want 4", tableCount)
	}
	if err := postgresadapter.ApplySchemaToEmptyDatabase(ctx, pool, databaseTargetForPool(t, pool)); err == nil || !strings.Contains(err.Error(), "requires an empty database") {
		t.Fatalf("second apply error = %v, want non-empty rejection", err)
	}
}

func TestIntegrationStoreIsolatesStoresSharingBaseURL(t *testing.T) {
	admin, baseSchema, neighborSchema := prepareStoreBoundaryFixture(t)
	_, firstPool := openIntegrationStore(t)
	firstSchema := integrationStoreSchema(t, firstPool)
	seedBoundarySentinel(t, firstPool, "first-store")
	firstSnapshot := boundarySentinelSnapshot(t, admin, firstSchema, "first-store")

	_, secondPool := openIntegrationStore(t)
	secondSchema := integrationStoreSchema(t, secondPool)
	if firstSchema == secondSchema {
		t.Errorf("same base URL produced the same store schema %q", firstSchema)
	}
	for _, schema := range []string{firstSchema, secondSchema} {
		if schema == baseSchema || schema == neighborSchema {
			t.Errorf("store reused fixture neighbor namespace %q instead of owning an isolated schema", schema)
		}
	}
	if got := boundarySentinelSnapshot(t, admin, firstSchema, "first-store"); got != firstSnapshot {
		t.Errorf("second store initialization changed first store facts: got %s, want %s", got, firstSnapshot)
	}
}

func TestIntegrationStorePreservesNeighborSentinels(t *testing.T) {
	admin, baseSchema, neighborSchema := prepareStoreBoundaryFixture(t)
	baseBefore := boundarySentinelSnapshot(t, admin, baseSchema, "fixture-sentinel")
	neighborBefore := boundarySentinelSnapshot(t, admin, neighborSchema, "fixture-sentinel")
	openIntegrationStore(t)
	if got := boundarySentinelSnapshot(t, admin, baseSchema, "fixture-sentinel"); got != baseBefore {
		t.Errorf("store initialization changed base namespace sentinel: got %s, want %s", got, baseBefore)
	}
	if got := boundarySentinelSnapshot(t, admin, neighborSchema, "fixture-sentinel"); got != neighborBefore {
		t.Errorf("store initialization changed adjacent namespace sentinel: got %s, want %s", got, neighborBefore)
	}
}

func TestIntegrationStoreCleanupDropsOnlyOwnNamespace(t *testing.T) {
	admin, baseSchema, neighborSchema := prepareStoreBoundaryFixture(t)
	neighborBefore := boundarySentinelSnapshot(t, admin, neighborSchema, "fixture-sentinel")
	_, survivorPool := openIntegrationStore(t)
	survivorSchema := integrationStoreSchema(t, survivorPool)
	seedBoundarySentinel(t, survivorPool, "survivor-store")
	survivorBefore := boundarySentinelSnapshot(t, admin, survivorSchema, "survivor-store")
	var ownedSchema string
	t.Run("owned store lifetime", func(t *testing.T) {
		_, ownedPool := openIntegrationStore(t)
		ownedSchema = integrationStoreSchema(t, ownedPool)
		if ownedSchema == survivorSchema || ownedSchema == baseSchema || ownedSchema == neighborSchema {
			t.Errorf("child store does not own a distinct namespace: %q", ownedSchema)
		}
	})
	if ownedSchema == "" {
		t.Fatal("child store did not expose its actual namespace")
	}
	if boundarySchemaExists(t, admin, ownedSchema) {
		t.Errorf("store cleanup left its namespace %q behind", ownedSchema)
	}
	for _, schema := range []string{baseSchema, neighborSchema, survivorSchema} {
		if !boundarySchemaExists(t, admin, schema) {
			t.Errorf("store cleanup deleted another fixture's namespace %q", schema)
		}
	}
	if got := boundarySentinelSnapshot(t, admin, survivorSchema, "survivor-store"); got != survivorBefore {
		t.Errorf("child store lifetime changed survivor facts: got %s, want %s", got, survivorBefore)
	}
	if got := boundarySentinelSnapshot(t, admin, neighborSchema, "fixture-sentinel"); got != neighborBefore {
		t.Errorf("child store lifetime changed adjacent sentinel: got %s, want %s", got, neighborBefore)
	}
}

// All namespaces in these regression tests belong to this test invocation in a
// Root-provided temporary database. Even the buggy helper only sees that owned
// base namespace; no RED path points at an application schema.
func prepareStoreBoundaryFixture(t *testing.T) (*pgxpool.Pool, string, string) {
	t.Helper()
	rawURL := os.Getenv("SCHEDULER_DATABASE_TEST_URL")
	if rawURL == "" {
		t.Skip("SCHEDULER_DATABASE_TEST_URL is not configured")
	}
	suffix := strings.ToLower(rand.Text())
	baseSchema := "scheduler_boundary_base_" + suffix
	neighborSchema := "scheduler_boundary_neighbor_" + suffix
	adminTarget := namespaceBoundaryTarget(t, rawURL, baseSchema)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminTarget.DriverURL())
	if err != nil {
		t.Fatal("open boundary fixture pool failed")
	}
	t.Cleanup(admin.Close)
	var databaseName string
	if err := admin.QueryRow(ctx, "SELECT current_database()").Scan(&databaseName); err != nil {
		t.Fatal("inspect boundary fixture database failed")
	}
	if !strings.HasPrefix(databaseName, "kokoro_scheduler_test") {
		t.Fatal("boundary fixture requires a Root-owned kokoro_scheduler_test temporary database")
	}
	for _, schemaName := range []string{baseSchema, neighborSchema} {
		identifier := pgx.Identifier{schemaName}.Sanitize()
		if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
			t.Fatalf("create owned boundary namespace %q: %v", schemaName, err)
		}
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
				t.Errorf("clean owned boundary namespace %q: %v", schemaName, err)
			}
		})
		target := namespaceBoundaryTarget(t, rawURL, schemaName)
		poolConfig, err := pgxpool.ParseConfig(target.DriverURL())
		if err != nil {
			t.Fatal("parse boundary fixture connection failed")
		}
		pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			t.Fatal("open owned boundary namespace pool failed")
		}
		t.Cleanup(pool.Close)
		if err := postgresadapter.ApplySchemaToEmptyDatabase(ctx, pool, target); err != nil {
			t.Fatalf("install owned boundary namespace %q: %v", schemaName, err)
		}
		seedBoundarySentinel(t, pool, "fixture-sentinel")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal("parse boundary fixture URL failed")
	}
	query := parsed.Query()
	query.Set("schema", baseSchema)
	parsed.RawQuery = query.Encode()
	t.Setenv("SCHEDULER_DATABASE_TEST_URL", parsed.String())
	return admin, baseSchema, neighborSchema
}

func seedBoundarySentinel(t *testing.T, pool *pgxpool.Pool, name string) {
	t.Helper()
	clock := doubles.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	service, err := application.NewService(postgresadapter.NewStore(pool, databaseTargetForPool(t, pool)), clock, recurrence.NewCalculator())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	receipt, err := service.Execute(ctx, createCommand("boundary-tenant", name, "key-"+name, "request-"+name))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Result.Code != domain.ResultRegistered || receipt.Result.Schedule == nil {
		t.Fatalf("sentinel seed = %#v, want a registered schedule", receipt.Result)
	}
}

func integrationStoreSchema(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var schemaName string
	if err := pool.QueryRow(ctx, "SELECT current_schema()").Scan(&schemaName); err != nil {
		t.Fatal(err)
	}
	return schemaName
}

func boundarySentinelSnapshot(t *testing.T, pool *pgxpool.Pool, schemaName, name string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	query := fmt.Sprintf(`SELECT jsonb_build_object(
		'schedule', (SELECT to_jsonb(s) FROM %s s WHERE tenant_id = $1 AND name = $2),
		'receipt', (SELECT to_jsonb(r) FROM %s r WHERE tenant_id = $1 AND idempotency_key = $3)
	)::text`, pgx.Identifier{schemaName, "scheduler_schedule"}.Sanitize(), pgx.Identifier{schemaName, "scheduler_command_receipt"}.Sanitize())
	var snapshot string
	if err := pool.QueryRow(ctx, query, "boundary-tenant", name, "key-"+name).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func boundarySchemaExists(t *testing.T, pool *pgxpool.Pool, schemaName string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = $1)", schemaName).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestPostgresCommandReceiptSurvivesServiceReconstructionAndIsolatesTenants(t *testing.T) {
	store, pool := openIntegrationStore(t)
	clock := doubles.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	first, err := application.NewService(store, clock, recurrence.NewCalculator())
	if err != nil {
		t.Fatal(err)
	}
	command := createCommand("tenant-a", "same-name", "key-a", "request-a")
	receipt, err := first.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}

	reconstructed, err := application.NewService(store, clock, recurrence.NewCalculator())
	if err != nil {
		t.Fatal(err)
	}
	replay, err := reconstructed.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if replay.RequestID != receipt.RequestID || replay.Result.Schedule == nil || receipt.Result.Schedule == nil || replay.Result.Schedule.ID != receipt.Result.Schedule.ID {
		t.Fatalf("durable replay = %#v, original = %#v", replay, receipt)
	}

	otherTenant := createCommand("tenant-b", "same-name", "key-b", "request-b")
	if _, err := reconstructed.Execute(context.Background(), otherTenant); err != nil {
		t.Fatalf("same schedule name in another tenant: %v", err)
	}
	var schedules, receipts int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM scheduler_schedule WHERE name = 'same-name'").Scan(&schedules); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM scheduler_command_receipt").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if schedules != 2 || receipts != 2 {
		t.Fatalf("schedule count=%d receipt count=%d, want 2/2", schedules, receipts)
	}

	conflict := command
	conflict.RequestDigest = digest("different-payload")
	if _, err := reconstructed.Execute(context.Background(), conflict); !errors.Is(err, application.ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
}

func TestPostgresDuplicateCreateWithNewKeyPersistsAndReplaysAlreadyExists(t *testing.T) {
	store, pool := openIntegrationStore(t)
	clock := doubles.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	service, err := application.NewService(store, clock, recurrence.NewCalculator())
	if err != nil {
		t.Fatal(err)
	}

	createdCommand := createCommand("tenant-a", "durable-duplicate", "key-created", "request-created")
	created, err := service.Execute(context.Background(), createdCommand)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if created.Result.Code != domain.ResultRegistered || created.Result.Schedule == nil {
		t.Fatalf("first result = %#v, want registered schedule", created.Result)
	}

	duplicateCommand := createCommand("tenant-a", "durable-duplicate", "key-duplicate", "request-duplicate")
	duplicate, err := service.Execute(context.Background(), duplicateCommand)
	if err != nil {
		t.Fatalf("duplicate create with new key: %v", err)
	}
	if duplicate.Result.Code != domain.ResultAlreadyExists || duplicate.Result.Schedule != nil {
		t.Fatalf("duplicate result = %#v, want schedule_already_exists without schedule", duplicate.Result)
	}

	var schedules, receipts int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*)
		  FROM scheduler_schedule
		 WHERE tenant_id = $1 AND name = $2`, "tenant-a", "durable-duplicate").Scan(&schedules); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*)
		  FROM scheduler_command_receipt
		 WHERE tenant_id = $1 AND command_scope = $2`, "tenant-a", createdCommand.CommandScope).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if schedules != 1 || receipts != 2 {
		t.Fatalf("schedule count=%d receipt count=%d, want 1/2", schedules, receipts)
	}

	reconstructed, err := application.NewService(store, clock, recurrence.NewCalculator())
	if err != nil {
		t.Fatal(err)
	}
	for _, expectation := range []struct {
		name     string
		command  application.Command
		original domain.CommandReceipt
	}{
		{name: "created", command: createdCommand, original: created},
		{name: "already exists", command: duplicateCommand, original: duplicate},
	} {
		t.Run(expectation.name, func(t *testing.T) {
			replayCommand := expectation.command
			replayCommand.RequestID = "request-replay-" + strings.ReplaceAll(expectation.name, " ", "-")
			replay, err := reconstructed.Execute(context.Background(), replayCommand)
			if err != nil {
				t.Fatal(err)
			}
			if replay.RequestID != expectation.original.RequestID || !reflect.DeepEqual(replay.Result, expectation.original.Result) {
				t.Fatalf("replay request/result = %#v/%#v, original = %#v/%#v", replay.RequestID, replay.Result, expectation.original.RequestID, expectation.original.Result)
			}
		})
	}
}

func TestPostgresConcurrentDuplicateCreatesWithNewKeysConverge(t *testing.T) {
	store, pool := openIntegrationStore(t)
	clock := doubles.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	service, err := application.NewService(store, clock, recurrence.NewCalculator())
	if err != nil {
		t.Fatal(err)
	}

	commands := []application.Command{
		createCommand("tenant-a", "concurrent-duplicate", "key-concurrent-a", "request-concurrent-a"),
		createCommand("tenant-a", "concurrent-duplicate", "key-concurrent-b", "request-concurrent-b"),
	}
	type outcome struct {
		receipt domain.CommandReceipt
		err     error
	}
	outcomes := make(chan outcome, len(commands))
	start := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, command := range commands {
		command := command
		go func() {
			<-start
			receipt, err := service.Execute(ctx, command)
			outcomes <- outcome{receipt: receipt, err: err}
		}()
	}
	close(start)

	resultCounts := map[string]int{}
	for range commands {
		select {
		case result := <-outcomes:
			if result.err != nil {
				t.Fatalf("concurrent create: %v", result.err)
			}
			resultCounts[result.receipt.Result.Code]++
		case <-ctx.Done():
			t.Fatalf("concurrent creates did not finish within bound: %v", ctx.Err())
		}
	}
	if resultCounts[domain.ResultRegistered] != 1 || resultCounts[domain.ResultAlreadyExists] != 1 {
		t.Fatalf("concurrent result counts = %#v, want one registered and one already exists", resultCounts)
	}

	var schedules, receipts int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*)
		  FROM scheduler_schedule
		 WHERE tenant_id = $1 AND name = $2`, "tenant-a", "concurrent-duplicate").Scan(&schedules); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*)
		  FROM scheduler_command_receipt
		 WHERE tenant_id = $1 AND command_scope = $2`, "tenant-a", commands[0].CommandScope).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if schedules != 1 || receipts != 2 {
		t.Fatalf("schedule count=%d receipt count=%d, want 1/2", schedules, receipts)
	}
}

func TestPostgresCommandReceiptPreservesOpaqueIdempotencyIdentity(t *testing.T) {
	store, pool := openIntegrationStore(t)
	clock := doubles.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	service, err := application.NewService(store, clock, recurrence.NewCalculator())
	if err != nil {
		t.Fatal(err)
	}

	coreKey := `AbC-._~:/?@!$&'()*+,;=[]{}#%`
	opaqueKey := "\u00a0" + coreKey + "\u00a0"
	scope := "DELETE:/internal/scheduler/v1/schedules/opaque-key"
	opaque := application.Command{
		Operation: domain.CommandDelete, TenantID: "tenant-a", Name: "opaque-key",
		CommandScope: scope, IdempotencyKey: opaqueKey, RequestDigest: digest(scope + ":payload"), RequestID: "request-opaque",
	}
	first, err := service.Execute(context.Background(), opaque)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Execute(context.Background(), opaque)
	if err != nil {
		t.Fatal(err)
	}
	if replay.RequestID != first.RequestID || replay.IdempotencyKey != opaqueKey {
		t.Fatalf("opaque replay=%#v, original=%#v", replay, first)
	}

	trimmed := opaque
	trimmed.IdempotencyKey = coreKey
	trimmed.RequestID = "request-trimmed"
	second, err := service.Execute(context.Background(), trimmed)
	if err != nil {
		t.Fatal(err)
	}
	if second.RequestID != "request-trimmed" || second.IdempotencyKey != coreKey {
		t.Fatalf("distinct trimmed key was aliased to opaque receipt: %#v", second)
	}

	conflict := opaque
	conflict.RequestDigest = digest("opaque-different-payload")
	if _, err := service.Execute(context.Background(), conflict); !errors.Is(err, application.ErrIdempotencyConflict) {
		t.Fatalf("same opaque key with different digest error=%v, want idempotency conflict", err)
	}

	rows, err := pool.Query(context.Background(), `
		SELECT idempotency_key
		  FROM scheduler_command_receipt
		 WHERE tenant_id = $1 AND command_scope = $2`, opaque.TenantID, opaque.CommandScope)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	keys := make(map[string]bool)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys[key] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || !keys[opaqueKey] || !keys[coreKey] {
		t.Fatalf("persisted receipt keys=%#v, want distinct opaque and trimmed identities", keys)
	}
}

func TestPostgresDuePlanningIsAtomicAndDuplicateSafeAcrossWorkers(t *testing.T) {
	store, pool := openIntegrationStore(t)
	clock := doubles.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	service, _ := application.NewService(store, clock, recurrence.NewCalculator())
	if _, err := service.Execute(context.Background(), createCommand("tenant-a", "recover", "key-recover", "request-recover")); err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Minute)
	first, _ := application.NewPlanner(store, clock, recurrence.NewCalculator(), "worker-a", time.Minute, 10)
	second, _ := application.NewPlanner(store, clock, recurrence.NewCalculator(), "worker-b", time.Minute, 10)
	var wait sync.WaitGroup
	wait.Add(2)
	for _, planner := range []*application.Planner{first, second} {
		go func(candidate *application.Planner) {
			defer wait.Done()
			if err := candidate.Run(context.Background()); err != nil {
				t.Errorf("planner run: %v", err)
			}
		}(planner)
	}
	wait.Wait()

	for _, query := range []string{
		"SELECT count(*) FROM scheduler_occurrence",
		"SELECT count(*) FROM scheduler_dispatch_outbox",
	} {
		var count int
		if err := pool.QueryRow(context.Background(), query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s = %d, want 1", query, count)
		}
	}
	var nextDueAt time.Time
	if err := pool.QueryRow(context.Background(), "SELECT next_due_at FROM scheduler_schedule WHERE tenant_id = 'tenant-a' AND name = 'recover'").Scan(&nextDueAt); err != nil {
		t.Fatal(err)
	}
	if want := clock.Now().Add(time.Minute); !nextDueAt.Equal(want) {
		t.Fatalf("next_due_at = %s, want %s", nextDueAt, want)
	}

	restarted, _ := application.NewPlanner(store, clock, recurrence.NewCalculator(), "worker-after-restart", time.Minute, 10)
	if err := restarted.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM scheduler_occurrence").Scan(&count); err != nil || count != 1 {
		t.Fatalf("occurrences after restart = %d, err=%v", count, err)
	}
}

func TestPostgresBoundedCatchUpPersistsOverlapAndBoundOutcomes(t *testing.T) {
	store, pool := openIntegrationStore(t)
	clock := doubles.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	service, _ := application.NewService(store, clock, recurrence.NewCalculator())
	command := createCommand("tenant-a", "bounded", "key-bounded", "request-bounded")
	command.Schedule.MisfirePolicy = domain.MisfireCatchUpBounded
	command.Schedule.CatchUpLimit = 3
	command.Schedule.OverlapPolicy = domain.OverlapForbid
	if _, err := service.Execute(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Minute)
	planner, _ := application.NewPlanner(store, clock, recurrence.NewCalculator(), "bounded-worker", time.Minute, 10)
	if err := planner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var occurrences, outbox, overlapSkipped, boundSkipped int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*),
		       count(*) FILTER (WHERE outcome_code = $1),
		       count(*) FILTER (WHERE outcome_code = $2)
		  FROM scheduler_occurrence
		 WHERE tenant_id = 'tenant-a'`, domain.CodeOverlapBlocked, domain.CodeMisfireBoundExceeded).Scan(&occurrences, &overlapSkipped, &boundSkipped); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM scheduler_dispatch_outbox WHERE tenant_id = 'tenant-a'`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if occurrences != 4 || outbox != 1 || overlapSkipped != 2 || boundSkipped != 1 {
		t.Fatalf("occurrences=%d outbox=%d overlap=%d bound=%d", occurrences, outbox, overlapSkipped, boundSkipped)
	}
}

func TestPostgresExpiredOutboxClaimIsRecovered(t *testing.T) {
	store, _ := openIntegrationStore(t)
	clock := seedDueOutbox(t, store)
	var first []domain.DispatchWork
	if err := store.WithinTx(context.Background(), func(tx ports.TxStore) error {
		var err error
		first, err = tx.ClaimDispatches(context.Background(), clock.Now(), "dead-worker", clock.Now().Add(time.Minute), 10)
		return err
	}); err != nil || len(first) != 1 {
		t.Fatalf("first claim=%d err=%v", len(first), err)
	}
	clock.Advance(2 * time.Minute)
	var recovered []domain.DispatchWork
	if err := store.WithinTx(context.Background(), func(tx ports.TxStore) error {
		var err error
		recovered, err = tx.ClaimDispatches(context.Background(), clock.Now(), "recovery-worker", clock.Now().Add(time.Minute), 10)
		return err
	}); err != nil || len(recovered) != 1 || recovered[0].ID != first[0].ID {
		t.Fatalf("recovered claim=%#v err=%v", recovered, err)
	}
}

func TestPostgresCrashAfterFinalAttemptBecomesObservableFailure(t *testing.T) {
	store, pool := openIntegrationStore(t)
	clock := doubles.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	service, _ := application.NewService(store, clock, recurrence.NewCalculator())
	if _, err := service.Execute(context.Background(), createCommand("tenant-a", "exhausted", "key-exhausted", "request-exhausted")); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	planner, _ := application.NewPlanner(store, clock, recurrence.NewCalculator(), "planner", time.Minute, 10)
	if err := planner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var claimed, begun domain.DispatchWork
	if err := store.WithinTx(context.Background(), func(tx ports.TxStore) error {
		items, err := tx.ClaimDispatches(context.Background(), clock.Now(), "dead-worker", clock.Now().Add(time.Minute), 1)
		if err != nil || len(items) != 1 {
			return fmt.Errorf("claim final attempt: count=%d: %w", len(items), err)
		}
		claimed = items[0]
		begun, err = tx.BeginDispatch(context.Background(), claimed, "dead-worker", clock.Now())
		return err
	}); err != nil || begun.AttemptCount != 1 {
		t.Fatalf("begin final attempt=%#v err=%v", begun, err)
	}
	clock.Advance(2 * time.Minute)
	target := &doubles.TargetClient{}
	dispatcher, _ := application.NewDispatcher(application.DispatcherDependencies{
		Store: store, Clock: clock, Random: &doubles.RandomSource{}, Target: target,
		WorkerID: "recovery-worker", ClaimTTL: time.Minute, DispatchTimeout: time.Second, BatchSize: 10,
	})
	if err := dispatcher.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var outboxStatus, occurrenceStatus, outcomeCode string
	if err := pool.QueryRow(context.Background(), `
		SELECT o.status, c.status, c.outcome_code
		  FROM scheduler_dispatch_outbox o
		  JOIN scheduler_occurrence c
		    ON c.tenant_id = o.tenant_id AND c.id = o.occurrence_id`).Scan(&outboxStatus, &occurrenceStatus, &outcomeCode); err != nil {
		t.Fatal(err)
	}
	if outboxStatus != domain.OutboxFailed || occurrenceStatus != domain.OccurrenceFailed || outcomeCode != domain.CodeDispatchRecoveryExhausted || target.CallCount() != 0 {
		t.Fatalf("outbox=%s occurrence=%s code=%s target_calls=%d", outboxStatus, occurrenceStatus, outcomeCode, target.CallCount())
	}
}

func TestPostgresPersistsRetryThenSuccessForHTTP425(t *testing.T) {
	store, pool := openIntegrationStore(t)
	clock := seedDueOutbox(t, store)
	target := &doubles.TargetClient{Results: []domain.DispatchResult{
		{Status: 425, Err: errors.New("too early")},
		{Status: 202},
	}}
	newDispatcher := func(worker string) *application.Dispatcher {
		dispatcher, err := application.NewDispatcher(application.DispatcherDependencies{
			Store: store, Clock: clock, Random: &doubles.RandomSource{}, Target: target,
			WorkerID: worker, ClaimTTL: time.Minute, DispatchTimeout: time.Second, BatchSize: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		return dispatcher
	}
	if err := newDispatcher("worker-first").Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int
	if err := pool.QueryRow(context.Background(), "SELECT status, attempt_count FROM scheduler_dispatch_outbox").Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != domain.OutboxRetrying || attempts != 1 {
		t.Fatalf("after 425 status=%s attempts=%d", status, attempts)
	}
	if err := newDispatcher("worker-after-restart").Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), "SELECT status, attempt_count FROM scheduler_dispatch_outbox").Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != domain.OutboxSucceeded || attempts != 2 || target.CallCount() != 2 {
		t.Fatalf("final status=%s attempts=%d calls=%d", status, attempts, target.CallCount())
	}
}

func seedDueOutbox(t *testing.T, store ports.Store) *doubles.Clock {
	t.Helper()
	clock := doubles.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	service, err := application.NewService(store, clock, recurrence.NewCalculator())
	if err != nil {
		t.Fatal(err)
	}
	command := createCommand("tenant-a", "dispatch", "key-dispatch", "request-dispatch")
	command.Schedule.Retry.MaxAttempts = 2
	if _, err := service.Execute(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	planner, _ := application.NewPlanner(store, clock, recurrence.NewCalculator(), "planner", time.Minute, 10)
	if err := planner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return clock
}

func createCommand(tenantID, name, key, requestID string) application.Command {
	scope := "POST:/internal/scheduler/v1/schedules/" + name
	return application.Command{
		Operation: domain.CommandCreate, TenantID: tenantID, Name: name,
		CommandScope: scope, IdempotencyKey: key, RequestDigest: digest(scope + ":payload"), RequestID: requestID,
		Schedule: domain.Schedule{
			Rule: "@every 1m", TargetURL: "http://service.test/command", Payload: []byte(`{}`),
			Retry: domain.DefaultRetryPolicy(), MisfirePolicy: domain.MisfireFireOnce,
			CatchUpLimit: 1, OverlapPolicy: domain.OverlapForbid,
		},
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func openIntegrationStore(t *testing.T) (*postgresadapter.Store, *pgxpool.Pool) {
	t.Helper()
	rawURL := os.Getenv("SCHEDULER_DATABASE_TEST_URL")
	if rawURL == "" {
		t.Skip("SCHEDULER_DATABASE_TEST_URL is not configured")
	}
	schemaName := "scheduler_integration_" + strings.ToLower(rand.Text())
	target := namespaceBoundaryTarget(t, rawURL, schemaName)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, target.DriverURL())
	if err != nil {
		t.Fatal("open integration fixture administration pool failed")
	}
	identifier := pgx.Identifier{schemaName}.Sanitize()
	schemaCreated := false
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		defer admin.Close()
		if pool != nil {
			pool.Close()
		}
		if !schemaCreated {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("clean owned integration namespace %q: %v", schemaName, err)
		}
	})
	var databaseName string
	if err := admin.QueryRow(ctx, "SELECT current_database()").Scan(&databaseName); err != nil {
		t.Fatal("inspect integration fixture database failed")
	}
	if len(databaseName) < len("kokoro_scheduler_test") || databaseName[:len("kokoro_scheduler_test")] != "kokoro_scheduler_test" {
		t.Fatalf("integration database %q must use the kokoro_scheduler_test prefix", databaseName)
	}
	// The database name admits a test target; it never grants ownership of its
	// existing tables. Only the namespace created below belongs to this store.
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatalf("create owned integration namespace %q: %v", schemaName, err)
	}
	schemaCreated = true
	poolConfig, err := pgxpool.ParseConfig(target.DriverURL())
	if err != nil {
		t.Fatal("parse integration fixture connection failed")
	}
	pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal("open owned integration namespace pool failed")
	}
	if err := postgresadapter.ApplySchemaToEmptyDatabase(ctx, pool, target); err != nil {
		t.Fatalf("install owned integration namespace %q: %v", schemaName, err)
	}
	return postgresadapter.NewStore(pool, target), pool
}

func TestApplySchemaRejectsViewOnlyNamespaceWithoutChangingNeighbors(t *testing.T) {
	admin, baseSchema, neighborSchema := prepareStoreBoundaryFixture(t)
	basePool := openNamespaceBoundaryPool(t, baseSchema)
	neighborPool := openNamespaceBoundaryPool(t, neighborSchema)
	seedNamespaceBoundaryFacts(t, basePool)
	seedNamespaceBoundaryFacts(t, neighborPool)
	baseBefore := namespaceBoundaryFactsSnapshot(t, admin, baseSchema)
	neighborBefore := namespaceBoundaryFactsSnapshot(t, admin, neighborSchema)

	targetSchema := "scheduler_view_only_" + strings.ToLower(rand.Text())
	identifier := pgx.Identifier{targetSchema}.Sanitize()
	created := false
	t.Cleanup(func() {
		if !created {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("clean owned view-only namespace %q: %v", targetSchema, err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal("create owned view-only namespace failed")
	}
	created = true
	viewIdentifier := pgx.Identifier{targetSchema, "unrelated_view"}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE VIEW "+viewIdentifier+" AS SELECT 1 AS marker"); err != nil {
		t.Fatal("create owned view sentinel failed")
	}
	pool := openNamespaceBoundaryPool(t, targetSchema)
	var viewBefore string
	if err := admin.QueryRow(ctx, "SELECT pg_get_viewdef($1::regclass, true)", viewIdentifier).Scan(&viewBefore); err != nil {
		t.Fatal("inspect owned view sentinel failed")
	}
	if err := postgresadapter.ApplySchemaToEmptyDatabase(ctx, pool, databaseTargetForPool(t, pool)); err == nil {
		t.Error("installer must reject a target namespace containing only a view")
	}
	var tables int
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname = $1", targetSchema).Scan(&tables); err != nil {
		t.Fatal("inspect target tables failed")
	}
	if tables != 0 {
		t.Errorf("rejected installation added %d tables to the view-only namespace", tables)
	}
	var viewAfter string
	if err := admin.QueryRow(ctx, "SELECT pg_get_viewdef($1::regclass, true)", viewIdentifier).Scan(&viewAfter); err != nil {
		t.Fatal("inspect retained view sentinel failed")
	}
	if viewAfter != viewBefore {
		t.Error("rejected installation changed the existing view definition")
	}
	if namespaceBoundaryFactsSnapshot(t, admin, baseSchema) != baseBefore {
		t.Error("view-only installation changed base namespace facts")
	}
	if namespaceBoundaryFactsSnapshot(t, admin, neighborSchema) != neighborBefore {
		t.Error("view-only installation changed adjacent namespace facts")
	}
}

func TestPostgresReadinessRejectsMissingNamespaceDespiteHealthyNeighbor(t *testing.T) {
	admin, baseSchema, neighborSchema := prepareStoreBoundaryFixture(t)
	basePool := openNamespaceBoundaryPool(t, baseSchema)
	neighborPool := openNamespaceBoundaryPool(t, neighborSchema)
	seedNamespaceBoundaryFacts(t, basePool)
	seedNamespaceBoundaryFacts(t, neighborPool)
	baseBefore := namespaceBoundaryFactsSnapshot(t, admin, baseSchema)
	neighborBefore := namespaceBoundaryFactsSnapshot(t, admin, neighborSchema)
	missingSchema := "scheduler_missing_" + strings.ToLower(rand.Text())
	if boundarySchemaExists(t, admin, missingSchema) {
		t.Fatal("random missing namespace already exists")
	}
	missingPool := openNamespaceBoundaryPool(t, missingSchema)
	store := postgresadapter.NewStore(missingPool, databaseTargetForPool(t, missingPool))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err == nil {
		t.Error("Store.Ping must reject a missing target namespace despite a live database")
	}
	recorder := httptest.NewRecorder()
	transporthttp.NewHTTPHandler(nil, "", store.Ping).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("missing namespace readiness status = %d, want 503", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"scheduler_not_ready"`) {
		t.Error("missing namespace readiness must return scheduler_not_ready")
	}

	healthyStore := postgresadapter.NewStore(neighborPool, databaseTargetForPool(t, neighborPool))
	if err := healthyStore.Ping(ctx); err != nil {
		t.Error("installed owner namespace must remain healthy")
	}
	healthyRecorder := httptest.NewRecorder()
	transporthttp.NewHTTPHandler(nil, "", healthyStore.Ping).ServeHTTP(healthyRecorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if healthyRecorder.Code != http.StatusOK {
		t.Errorf("installed owner namespace readiness status = %d, want 200", healthyRecorder.Code)
	}
	if boundarySchemaExists(t, admin, missingSchema) {
		t.Error("readiness created a missing namespace")
	}
	if namespaceBoundaryFactsSnapshot(t, admin, baseSchema) != baseBefore {
		t.Error("readiness changed base namespace facts")
	}
	if namespaceBoundaryFactsSnapshot(t, admin, neighborSchema) != neighborBefore {
		t.Error("readiness changed adjacent namespace facts")
	}
}

// This helper only opens a pool after prepareStoreBoundaryFixture has admitted
// the Root-owned test database. A missing namespace is never created or dropped.
func openNamespaceBoundaryPool(t *testing.T, schemaName string) *pgxpool.Pool {
	t.Helper()
	target := namespaceBoundaryTarget(t, os.Getenv("SCHEDULER_DATABASE_TEST_URL"), schemaName)
	poolConfig, err := pgxpool.ParseConfig(target.DriverURL())
	if err != nil {
		t.Fatal("parse namespace boundary connection failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal("open namespace boundary pool failed")
	}
	t.Cleanup(pool.Close)
	var actualSearchPath, actualTimezone string
	if err := pool.QueryRow(ctx, "SELECT current_setting('search_path'), current_setting('TimeZone')").Scan(&actualSearchPath, &actualTimezone); err != nil {
		t.Fatal("inspect actual namespace boundary connection settings failed")
	}
	if actualSearchPath != schemaName || actualTimezone != "UTC" {
		t.Fatalf("actual boundary settings = %q/%q, want %q/UTC", actualSearchPath, actualTimezone, schemaName)
	}
	return pool
}

func seedNamespaceBoundaryFacts(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	clock := doubles.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := postgresadapter.NewStore(pool, databaseTargetForPool(t, pool))
	service, err := application.NewService(store, clock, recurrence.NewCalculator())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := service.Execute(ctx, createCommand("boundary-tenant", "namespace-sentinel", "namespace-key", "namespace-request")); err != nil {
		t.Fatal("seed namespace boundary schedule and receipt failed")
	}
	clock.Advance(time.Minute)
	planner, err := application.NewPlanner(store, clock, recurrence.NewCalculator(), "boundary-planner", time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := planner.Run(ctx); err != nil {
		t.Fatal("seed namespace boundary occurrence and outbox failed")
	}
}

func namespaceBoundaryFactsSnapshot(t *testing.T, pool *pgxpool.Pool, schemaName string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var snapshots []string
	for _, table := range []string{"scheduler_schedule", "scheduler_command_receipt", "scheduler_occurrence", "scheduler_dispatch_outbox"} {
		query := "SELECT count(*), COALESCE(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text), '[]'::jsonb)::text FROM " + pgx.Identifier{schemaName, table}.Sanitize() + " r"
		var count int
		var snapshot string
		if err := pool.QueryRow(ctx, query).Scan(&count, &snapshot); err != nil {
			var pgError *pgconn.PgError
			if errors.As(err, &pgError) {
				reason := map[string]string{
					"42P01": "undefined_table",
					"42703": "undefined_column",
					"42501": "insufficient_privilege",
					"57014": "query_cancelled",
				}[pgError.Code]
				if reason == "" {
					reason = "postgres_query_failed"
				}
				t.Fatalf("snapshot owned namespace table %s failed: SQLSTATE=%s reason=%s", table, pgError.Code, reason)
			}
			reason := "connection_or_query_failed"
			if errors.Is(err, context.DeadlineExceeded) {
				reason = "deadline_exceeded"
			} else if errors.Is(err, context.Canceled) {
				reason = "context_cancelled"
			}
			t.Fatalf("snapshot owned namespace table %s failed: reason=%s", table, reason)
		}
		if count == 0 {
			t.Fatalf("owned namespace table %s lacks a sentinel fact", table)
		}
		snapshots = append(snapshots, table+":"+snapshot)
	}
	return strings.Join(snapshots, "\n")
}

// Fixture assembly changes only the test-owned selector. Production URL rules
// remain exclusively in config.ParseDatabaseURL; all other input keys survive.
func namespaceBoundaryTarget(t *testing.T, rawURL, schemaName string) config.DatabaseTarget {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal("parse fixture base URL failed")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		t.Fatal("parse fixture base URL query failed")
	}
	query.Set("schema", schemaName)
	parsed.RawQuery = query.Encode()
	target, err := config.ParseDatabaseURL(parsed.String())
	if err != nil {
		t.Fatal("parse explicit fixture database target failed")
	}
	return target
}

func databaseTargetForPool(t *testing.T, pool *pgxpool.Pool) config.DatabaseTarget {
	t.Helper()
	return namespaceBoundaryTarget(t, os.Getenv("SCHEDULER_DATABASE_TEST_URL"), pool.Config().ConnConfig.RuntimeParams["search_path"])
}

func TestConcurrentSchemaInstallersSerializeAndPreserveNeighbors(t *testing.T) {
	admin, baseSchema, neighborSchema := prepareStoreBoundaryFixture(t)
	basePool := openNamespaceBoundaryPool(t, baseSchema)
	neighborPool := openNamespaceBoundaryPool(t, neighborSchema)
	seedNamespaceBoundaryFacts(t, basePool)
	seedNamespaceBoundaryFacts(t, neighborPool)
	baseBefore := namespaceBoundaryFactsSnapshot(t, admin, baseSchema)
	neighborBefore := namespaceBoundaryFactsSnapshot(t, admin, neighborSchema)
	targetSchema := "scheduler_concurrent_" + strings.ToLower(rand.Text())
	if boundarySchemaExists(t, admin, targetSchema) {
		t.Fatal("random concurrent installation target already exists")
	}
	target := namespaceBoundaryTarget(t, os.Getenv("SCHEDULER_DATABASE_TEST_URL"), targetSchema)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var databaseName string
	if err := admin.QueryRow(ctx, "SELECT current_database()").Scan(&databaseName); err != nil {
		t.Fatal("inspect controlled installer database failed")
	}
	lockIdentity := "kokoro-scheduler:db:apply-schema:" + databaseName + ":" + targetSchema
	gate, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal("begin controlled installation lock gate failed")
	}
	t.Cleanup(func() {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rollbackCancel()
		_ = gate.Rollback(rollbackCtx)
	})
	if _, err := gate.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", lockIdentity); err != nil {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rollbackCancel()
		_ = gate.Rollback(rollbackCtx)
		t.Fatal("acquire controlled installation lock gate failed")
	}
	var workers sync.WaitGroup
	completed := make(chan ownedInstallerOutcome, 2)
	targetCreated := false
	// Registered before pool closes: after the worker-drain cleanup below, only
	// a namespace proven absent before this invocation may be removed.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := admin.QueryRow(cleanupCtx, "SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = $1)", targetSchema).Scan(&targetCreated); err != nil {
			t.Error("inspect owned concurrent installation target during cleanup failed")
			return
		}
		if targetCreated {
			if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{targetSchema}.Sanitize()+" CASCADE"); err != nil {
				t.Error("clean owned concurrent installation target failed")
			}
		}
	})
	firstPool, firstPID := openSingleConnectionInstallerPool(t, ctx, target)
	secondPool, secondPID := openSingleConnectionInstallerPool(t, ctx, target)
	t.Cleanup(func() {
		cancel()
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = gate.Rollback(rollbackCtx)
		rollbackCancel()
		drained := make(chan struct{})
		go func() {
			workers.Wait()
			close(drained)
		}()
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer drainCancel()
		select {
		case <-drained:
		case <-drainCtx.Done():
			t.Error("owned installer workers did not drain within the cleanup deadline")
		}
	})
	if firstPID == secondPID || firstPID == 0 || secondPID == 0 {
		t.Fatal("concurrent installers must use two distinct real PostgreSQL connections")
	}
	start := func(position int, pool *pgxpool.Pool) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			completed <- ownedInstallerOutcome{position: position, err: postgresadapter.ApplySchemaToEmptyDatabase(ctx, pool, target)}
		}()
	}
	start(1, firstPool)
	waitForOwnedInstallerWaiters(t, ctx, admin, []int32{firstPID}, lockIdentity, 1, completed)
	start(2, secondPool)
	waitForOwnedInstallerWaiters(t, ctx, admin, []int32{firstPID, secondPID}, lockIdentity, 2, completed)
	if boundarySchemaExists(t, admin, targetSchema) {
		t.Error("blocked installers created a namespace before the controlled gate released")
	}
	// No sleep determines overlap: both backend PIDs have been observed waiting
	// on the exact owner/database/schema advisory key before this release.
	if err := gate.Commit(ctx); err != nil {
		t.Fatal("release controlled installation lock gate failed")
	}

	successes, nonemptyRejections := 0, 0
	for range 2 {
		select {
		case outcome := <-completed:
			if outcome.err == nil {
				successes++
			} else if strings.Contains(outcome.err.Error(), "requires an empty database schema") {
				nonemptyRejections++
			} else {
				t.Errorf("installer %d returned an unexpected failure classification", outcome.position)
			}
		case <-ctx.Done():
			t.Fatal("controlled concurrent installation did not complete within its deadline")
		}
	}
	if successes != 1 || nonemptyRejections != 1 {
		t.Errorf("installer outcomes = %d successful/%d nonempty rejected, want 1/1", successes, nonemptyRejections)
	}
	targetCreated = boundarySchemaExists(t, admin, targetSchema)
	if !targetCreated {
		t.Error("successful concurrent installer did not persist its owned target")
	}
	var tableCount int
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname = $1", targetSchema).Scan(&tableCount); err != nil {
		t.Fatal("inspect concurrent installation target tables failed")
	}
	if tableCount != 4 {
		t.Errorf("concurrent installation persisted %d tables, want exactly four", tableCount)
	}
	if err := postgresadapter.NewStore(firstPool, target).Ping(ctx); err != nil {
		t.Error("concurrent installation winner must leave a ready owner namespace")
	}
	if namespaceBoundaryFactsSnapshot(t, admin, baseSchema) != baseBefore {
		t.Error("concurrent installation changed base namespace facts")
	}
	if namespaceBoundaryFactsSnapshot(t, admin, neighborSchema) != neighborBefore {
		t.Error("concurrent installation changed adjacent namespace facts")
	}
}

type ownedInstallerOutcome struct {
	position int
	err      error
}

func openSingleConnectionInstallerPool(t *testing.T, ctx context.Context, target config.DatabaseTarget) (*pgxpool.Pool, int32) {
	t.Helper()
	poolConfig, err := pgxpool.ParseConfig(target.DriverURL())
	if err != nil {
		t.Fatal("parse controlled installer pool configuration failed")
	}
	poolConfig.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal("open controlled single-connection installer pool failed")
	}
	t.Cleanup(pool.Close)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal("acquire real controlled installer connection failed")
	}
	pid := int32(conn.Conn().PgConn().PID())
	conn.Release()
	return pool, pid
}

func waitForOwnedInstallerWaiters(t *testing.T, ctx context.Context, admin *pgxpool.Pool, pids []int32, lockIdentity string, expected int, completed <-chan ownedInstallerOutcome) {
	t.Helper()
	for {
		select {
		case outcome := <-completed:
			t.Fatalf("installer %d completed before its controlled lock gate was released", outcome.position)
		case <-ctx.Done():
			t.Fatal("observe controlled installer lock waiters exceeded its deadline")
		default:
		}
		var waiting int
		if err := admin.QueryRow(ctx, `SELECT count(*)
			FROM pg_catalog.pg_locks l
			WHERE l.pid = ANY($1::integer[]) AND l.locktype = 'advisory'
			  AND NOT l.granted AND l.objsubid = 1
			  AND ((l.classid::bigint << 32) | l.objid::bigint) = hashtextextended($2, 0)
			  AND l.database = (SELECT oid FROM pg_catalog.pg_database WHERE datname = current_database())`, pids, lockIdentity).Scan(&waiting); err != nil {
			t.Fatal("inspect exact owned advisory-lock waiters failed")
		}
		if waiting == expected {
			return
		}
	}
}

func TestSchemaInstallationDDLErrorRollsBackMissingTargetAndEarlierTables(t *testing.T) {
	admin, baseSchema, neighborSchema := prepareStoreBoundaryFixture(t)
	basePool := openNamespaceBoundaryPool(t, baseSchema)
	neighborPool := openNamespaceBoundaryPool(t, neighborSchema)
	seedNamespaceBoundaryFacts(t, basePool)
	seedNamespaceBoundaryFacts(t, neighborPool)
	baseBefore := namespaceBoundaryFactsSnapshot(t, admin, baseSchema)
	neighborBefore := namespaceBoundaryFactsSnapshot(t, admin, neighborSchema)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var canCreateEventTrigger bool
	if err := admin.QueryRow(ctx, "SELECT rolsuper FROM pg_catalog.pg_roles WHERE rolname = current_user").Scan(&canCreateEventTrigger); err != nil {
		t.Fatal("inspect event-trigger fixture capability failed")
	}
	if !canCreateEventTrigger {
		t.Fatal("rollback fixture requires the Root-owned temporary database connection's existing event-trigger creation capability; no role or privilege is changed")
	}

	suffix := strings.ToLower(rand.Text())
	controlSchema := "scheduler_fault_control_" + suffix
	targetSchema := "scheduler_fault_target_" + suffix
	triggerName := "scheduler_ddl_fault_" + suffix
	faultMarker := "owned_scheduler_second_table_fault_" + suffix
	controlIdentifier := pgx.Identifier{controlSchema}.Sanitize()
	triggerIdentifier := pgx.Identifier{triggerName}.Sanitize()
	functionIdentifier := pgx.Identifier{controlSchema, "fail_second_scheduler_table"}.Sanitize()
	if boundarySchemaExists(t, admin, targetSchema) || boundarySchemaExists(t, admin, controlSchema) {
		t.Fatal("random rollback fixture namespace already exists")
	}
	controlCreated, triggerCreated, targetCreated := false, false, false
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if triggerCreated {
			if _, err := admin.Exec(cleanupCtx, "DROP EVENT TRIGGER "+triggerIdentifier); err != nil {
				t.Error("clean owned DDL fault trigger failed")
			}
		}
		if err := admin.QueryRow(cleanupCtx, "SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = $1)", targetSchema).Scan(&targetCreated); err != nil {
			t.Error("inspect owned rollback target during cleanup failed")
		} else if targetCreated {
			if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{targetSchema}.Sanitize()+" CASCADE"); err != nil {
				t.Error("clean owned failed-installation target failed")
			}
		}
		if controlCreated {
			if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+controlIdentifier+" CASCADE"); err != nil {
				t.Error("clean owned DDL fault control namespace failed")
			}
		}
	})
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+controlIdentifier); err != nil {
		t.Fatal("create owned DDL fault control namespace failed")
	}
	controlCreated = true
	// These literals are only generated lowercase ASCII fixture identifiers.
	// The trigger is database-local and faults only this absent owned target.
	functionSQL := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS event_trigger LANGUAGE plpgsql AS $owned_fault$
		BEGIN
			IF TG_TAG = 'CREATE TABLE' AND current_schema() = '%s'
			   AND to_regclass('%s.scheduler_schedule') IS NOT NULL THEN
				RAISE NOTICE USING MESSAGE = '%s';
				RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '%s';
			END IF;
		END;
		$owned_fault$`, functionIdentifier, targetSchema, targetSchema, faultMarker, faultMarker)
	if _, err := admin.Exec(ctx, functionSQL); err != nil {
		t.Fatal("create owned second-table DDL fault function failed")
	}
	if _, err := admin.Exec(ctx, "CREATE EVENT TRIGGER "+triggerIdentifier+" ON ddl_command_start WHEN TAG IN ('CREATE TABLE') EXECUTE FUNCTION "+functionIdentifier+"()"); err != nil {
		t.Fatal("create owned second-table DDL fault trigger failed")
	}
	triggerCreated = true
	target := namespaceBoundaryTarget(t, os.Getenv("SCHEDULER_DATABASE_TEST_URL"), targetSchema)
	poolConfig, err := pgxpool.ParseConfig(target.DriverURL())
	if err != nil {
		t.Fatal("parse controlled rollback installer pool configuration failed")
	}
	faultReached := make(chan struct{}, 1)
	poolConfig.ConnConfig.RuntimeParams["client_min_messages"] = "notice"
	poolConfig.ConnConfig.OnNotice = func(_ *pgconn.PgConn, notice *pgconn.Notice) {
		if notice.Code == "00000" && notice.Message == faultMarker {
			select {
			case faultReached <- struct{}{}:
			default:
			}
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal("open controlled rollback installer pool failed")
	}
	t.Cleanup(pool.Close)
	err = postgresadapter.ApplySchemaToEmptyDatabase(ctx, pool, target)
	if err == nil || !strings.Contains(err.Error(), "execute canonical database/schema.sql") {
		t.Error("controlled second-table DDL fault must reject the canonical installation")
	}
	select {
	case <-faultReached:
		// The server emitted this notice only after the missing namespace and
		// first canonical table existed inside the real installation transaction.
	default:
		t.Error("DDL fault did not prove the missing target and first table were created before failure")
	}
	targetCreated = boundarySchemaExists(t, admin, targetSchema)
	if targetCreated {
		t.Error("DDL failure persisted the installer-created target namespace")
	}
	var relations int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1`, targetSchema).Scan(&relations); err != nil {
		t.Fatal("inspect rolled-back canonical relations failed")
	}
	if relations != 0 {
		t.Errorf("DDL failure persisted %d earlier target relations, want zero", relations)
	}
	if namespaceBoundaryFactsSnapshot(t, admin, baseSchema) != baseBefore {
		t.Error("DDL rollback changed base namespace facts")
	}
	if namespaceBoundaryFactsSnapshot(t, admin, neighborSchema) != neighborBefore {
		t.Error("DDL rollback changed adjacent namespace facts")
	}
}

func TestApplySchemaRejectsFunctionOnlyNamespace(t *testing.T) {
	testApplySchemaRejectsOwnedObjectOnlyNamespace(t, "function")
}

func TestApplySchemaRejectsTypeOnlyNamespace(t *testing.T) {
	testApplySchemaRejectsOwnedObjectOnlyNamespace(t, "type")
}

func testApplySchemaRejectsOwnedObjectOnlyNamespace(t *testing.T, kind string) {
	t.Helper()
	admin, baseSchema, neighborSchema := prepareStoreBoundaryFixture(t)
	seedNamespaceBoundaryFacts(t, openNamespaceBoundaryPool(t, baseSchema))
	seedNamespaceBoundaryFacts(t, openNamespaceBoundaryPool(t, neighborSchema))
	beforeBase := namespaceBoundaryFactsSnapshot(t, admin, baseSchema)
	beforeNeighbor := namespaceBoundaryFactsSnapshot(t, admin, neighborSchema)
	schemaName := "scheduler_object_only_" + strings.ToLower(rand.Text())
	target := namespaceBoundaryTarget(t, os.Getenv("SCHEDULER_DATABASE_TEST_URL"), schemaName)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	identifier := pgx.Identifier{schemaName}.Sanitize()
	created := false
	t.Cleanup(func() {
		if !created {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error("clean owned object-only namespace failed")
		}
	})
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal("create owned object-only namespace failed")
	}
	created = true
	objectName := pgx.Identifier{schemaName, "owned_object"}.Sanitize()
	var ddl, inspect string
	switch kind {
	case "function":
		ddl = "CREATE FUNCTION " + objectName + "() RETURNS integer LANGUAGE sql AS 'SELECT 1'"
		inspect = "SELECT pg_get_functiondef(p.oid) FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = $1 AND p.proname = 'owned_object'"
	case "type":
		ddl = "CREATE TYPE " + objectName + " AS ENUM ('owned')"
		inspect = "SELECT jsonb_agg(e.enumlabel ORDER BY e.enumsortorder)::text FROM pg_catalog.pg_enum e JOIN pg_catalog.pg_type ty ON ty.oid = e.enumtypid JOIN pg_catalog.pg_namespace n ON n.oid = ty.typnamespace WHERE n.nspname = $1 AND ty.typname = 'owned_object'"
	default:
		t.Fatal("unknown object-only fixture kind")
	}
	if _, err := admin.Exec(ctx, ddl); err != nil {
		t.Fatal("create owned object-only fixture failed")
	}
	var before, after string
	if err := admin.QueryRow(ctx, inspect, schemaName).Scan(&before); err != nil {
		t.Fatal("inspect owned object-only fixture failed")
	}
	pool := openNamespaceBoundaryPool(t, schemaName)
	err := postgresadapter.ApplySchemaToEmptyDatabase(ctx, pool, target)
	if err == nil || !strings.Contains(err.Error(), "requires an empty database") {
		t.Error("object-only namespace must reject installation as nonempty")
	}
	if err := admin.QueryRow(ctx, inspect, schemaName).Scan(&after); err != nil {
		t.Fatal("inspect preserved object-only fixture failed")
	}
	if before != after {
		t.Error("rejected installation changed the original owner object")
	}
	var tables int
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relkind = 'r'", schemaName).Scan(&tables); err != nil {
		t.Fatal("inspect rejected installation tables failed")
	}
	if tables != 0 {
		t.Error("rejected object-only installation created canonical tables")
	}
	if namespaceBoundaryFactsSnapshot(t, admin, baseSchema) != beforeBase || namespaceBoundaryFactsSnapshot(t, admin, neighborSchema) != beforeNeighbor {
		t.Error("object-only rejection changed neighbor facts")
	}
}

func TestSchemaCatalogMatchesCanonicalReferenceAcrossNamespaces(t *testing.T) {
	admin, baseSchema, neighborSchema := prepareStoreBoundaryFixture(t)
	seedNamespaceBoundaryFacts(t, openNamespaceBoundaryPool(t, baseSchema))
	seedNamespaceBoundaryFacts(t, openNamespaceBoundaryPool(t, neighborSchema))
	baseBefore := namespaceBoundaryFactsSnapshot(t, admin, baseSchema)
	neighborBefore := namespaceBoundaryFactsSnapshot(t, admin, neighborSchema)
	_, referencePool := openIntegrationStore(t)
	_, actualPool := openIntegrationStore(t)
	referenceTarget := databaseTargetForPool(t, referencePool)
	actualTarget := databaseTargetForPool(t, actualPool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var referenceOID, actualOID uint32
	var serverVersion string
	if err := admin.QueryRow(ctx, "SELECT to_regnamespace($1)::oid, to_regnamespace($2)::oid, current_setting('server_version_num')", referenceTarget.SchemaName(), actualTarget.SchemaName()).Scan(&referenceOID, &actualOID, &serverVersion); err != nil {
		t.Fatal("inspect reference/actual catalog fixture identities failed")
	}
	if referenceTarget.SchemaName() == actualTarget.SchemaName() || referenceOID == actualOID {
		t.Fatal("catalog reference and actual did not acquire distinct owned identities")
	}
	t.Logf("catalog fixture PostgreSQL server_version_num=%s", serverVersion)
	reference := readOwnedSchemaCatalog(t, referencePool)
	actual := readOwnedSchemaCatalog(t, actualPool)
	if len(reference.Tables) != 4 || len(actual.Tables) != 4 {
		t.Fatal("canonical installer did not produce four actual tables")
	}
	if differences := postgresadapter.CompareSchemaCatalog(actual, reference); len(differences) != 0 {
		t.Fatalf("canonical catalogs differ across namespaces: %v", differences)
	}
	if repeated := readOwnedSchemaCatalog(t, actualPool); !reflect.DeepEqual(actual, repeated) {
		t.Error("unchanged actual catalog snapshot was not stable")
	}
	// An explicit target, not the pool's existing search_path, selects ownership.
	sessionPool, sessionPID := openSingleConnectionInstallerPool(t, ctx, referenceTarget)
	var beforeSchema, beforeSearchPath, beforeTimezone string
	var beforePID, afterPID int32
	if err := sessionPool.QueryRow(ctx, "SELECT current_schema(), current_setting('search_path'), current_setting('TimeZone'), pg_backend_pid()").Scan(&beforeSchema, &beforeSearchPath, &beforeTimezone, &beforePID); err != nil {
		t.Fatal("inspect reference session before read failed")
	}
	readByTarget, err := postgresadapter.ReadSchemaCatalog(ctx, sessionPool, actualTarget)
	if err != nil {
		t.Fatal("read explicit actual target through reference pool failed")
	}
	if !reflect.DeepEqual(actual, readByTarget) {
		t.Error("catalog reader inferred its target from the existing pool namespace")
	}
	var afterSchema, afterSearchPath, afterTimezone string
	if err := sessionPool.QueryRow(ctx, "SELECT current_schema(), current_setting('search_path'), current_setting('TimeZone'), pg_backend_pid()").Scan(&afterSchema, &afterSearchPath, &afterTimezone, &afterPID); err != nil {
		t.Fatal("inspect reference session after read failed")
	}
	if beforeSchema != afterSchema || beforeSearchPath != afterSearchPath || beforeTimezone != afterTimezone {
		t.Error("read-only catalog collection leaked local session changes")
	}
	if beforePID != sessionPID || afterPID != sessionPID {
		t.Error("catalog session restoration was not observed on the same backend")
	}
	if !reflect.DeepEqual(reference, readOwnedSchemaCatalog(t, referencePool)) {
		t.Error("catalog collection modified canonical reference definitions")
	}
	if namespaceBoundaryFactsSnapshot(t, admin, baseSchema) != baseBefore || namespaceBoundaryFactsSnapshot(t, admin, neighborSchema) != neighborBefore {
		t.Error("catalog collection changed neighbor business facts")
	}
}

func TestSchemaCatalogRejectsSameTableCountDrift(t *testing.T) {
	admin, baseSchema, neighborSchema := prepareStoreBoundaryFixture(t)
	seedNamespaceBoundaryFacts(t, openNamespaceBoundaryPool(t, baseSchema))
	seedNamespaceBoundaryFacts(t, openNamespaceBoundaryPool(t, neighborSchema))
	baseBefore := namespaceBoundaryFactsSnapshot(t, admin, baseSchema)
	neighborBefore := namespaceBoundaryFactsSnapshot(t, admin, neighborSchema)
	_, referencePool := openIntegrationStore(t)
	reference := readOwnedSchemaCatalog(t, referencePool)
	type driftCase struct {
		name, ddl, field string
		literal          bool
	}
	cases := []driftCase{
		{"missing_column", "ALTER TABLE %s.scheduler_dispatch_outbox DROP COLUMN last_error_message", "Columns", false},
		{"column_type", "ALTER TABLE %s.scheduler_schedule ALTER COLUMN version TYPE integer", "Type.Name", false},
		{"nullability", "ALTER TABLE %s.scheduler_schedule ALTER COLUMN target_url DROP NOT NULL", "NotNull", false},
		{"timestamp_precision", "ALTER TABLE %s.scheduler_schedule ALTER COLUMN next_due_at TYPE timestamptz(6)", "TypeModifier", false},
		{"column_storage", "ALTER TABLE %s.scheduler_schedule ALTER COLUMN target_url SET STORAGE EXTERNAL", "Storage", false},
		{"column_comment", "COMMENT ON COLUMN %s.scheduler_schedule.target_url IS 'owned column metadata drift'", "Comment", false},
		{"default_value", "ALTER TABLE %s.scheduler_schedule ALTER COLUMN status SET DEFAULT 'paused'", "Default", false},
		{"literal_namespace", "", "Default", true},
		{"check_expression", "ALTER TABLE %s.scheduler_dispatch_outbox DROP CONSTRAINT ck_scheduler_dispatch_outbox_attempts, ADD CONSTRAINT ck_scheduler_dispatch_outbox_attempts CHECK (max_attempts BETWEEN 1 AND 10 AND attempt_count BETWEEN 0 AND 100)", "Expression", false},
		{"check_not_valid", "ALTER TABLE %s.scheduler_dispatch_outbox DROP CONSTRAINT ck_scheduler_dispatch_outbox_attempts, ADD CONSTRAINT ck_scheduler_dispatch_outbox_attempts CHECK (max_attempts BETWEEN 1 AND 10 AND attempt_count BETWEEN 0 AND max_attempts) NOT VALID", "Validated", false},
		{"unique_deferred", "ALTER TABLE %s.scheduler_schedule DROP CONSTRAINT uq_scheduler_schedule_tenant_name, ADD CONSTRAINT uq_scheduler_schedule_tenant_name UNIQUE (tenant_id, name) DEFERRABLE INITIALLY DEFERRED", "Deferred", false},
		{"partial_predicate", "DROP INDEX %[1]s.ix_scheduler_schedule_due; CREATE INDEX ix_scheduler_schedule_due ON %[1]s.scheduler_schedule (next_due_at, id) WHERE status = 'paused'", "Predicate", false},
		{"index_direction", "DROP INDEX %[1]s.ix_scheduler_occurrence_tenant_schedule; CREATE INDEX ix_scheduler_occurrence_tenant_schedule ON %[1]s.scheduler_occurrence (tenant_id, schedule_id, scheduled_at ASC, id DESC)", "Options", false},
		{"index_nulls", "DROP INDEX %[1]s.ix_scheduler_occurrence_tenant_schedule; CREATE INDEX ix_scheduler_occurrence_tenant_schedule ON %[1]s.scheduler_occurrence (tenant_id, schedule_id, scheduled_at DESC NULLS LAST, id DESC)", "Options", false},
		{"index_include", "DROP INDEX %[1]s.ix_scheduler_command_receipt_created; CREATE INDEX ix_scheduler_command_receipt_created ON %[1]s.scheduler_command_receipt (created_at) INCLUDE (id)", "KeyCount", false},
		{"index_expression", "DROP INDEX %[1]s.ix_scheduler_schedule_due; CREATE INDEX ix_scheduler_schedule_due ON %[1]s.scheduler_schedule ((version + 1), id) WHERE status = 'active'", "Expressions", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, pool := openIntegrationStore(t)
			target := databaseTargetForPool(t, pool)
			baseline := readOwnedSchemaCatalog(t, pool)
			if differences := postgresadapter.CompareSchemaCatalog(baseline, reference); len(differences) != 0 {
				t.Fatalf("drift fixture baseline differs from canonical reference: %v", differences)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			ddl := fmt.Sprintf(tc.ddl, pgx.Identifier{target.SchemaName()}.Sanitize())
			if tc.literal {
				// The literal is a generated lowercase ASCII fixture identifier.
				ddl = fmt.Sprintf("ALTER TABLE %s.scheduler_schedule ALTER COLUMN target_url SET DEFAULT '%s'", pgx.Identifier{target.SchemaName()}.Sanitize(), target.SchemaName())
			}
			if _, err := pool.Exec(ctx, ddl); err != nil {
				t.Fatalf("execute %s drift fixture failed", tc.name)
			}
			var tableCount int
			if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relkind = 'r'", target.SchemaName()).Scan(&tableCount); err != nil {
				t.Fatal("inspect same-table-count drift failed")
			}
			if tableCount != 4 {
				t.Fatal("drift fixture no longer has four ordinary tables")
			}
			if err := store.Ping(ctx); err != nil {
				t.Error("same-table-count drift unexpectedly failed the lightweight readiness positive control")
			}
			actual := readOwnedSchemaCatalog(t, pool)
			differences := postgresadapter.CompareSchemaCatalog(actual, reference)
			foundField := false
			for _, difference := range differences {
				if strings.Contains(difference.Field, tc.field) {
					foundField = true
				}
			}
			if len(differences) == 0 || !foundField {
				t.Errorf("%s drift was not detected at the required structural field %s: %v", tc.name, tc.field, differences)
			}
			if tc.literal {
				column := ownedCatalogColumn(t, actual, "scheduler_schedule", "target_url")
				if column.Default == nil || !strings.Contains(*column.Default, target.SchemaName()) || strings.Contains(*column.Default, "SELF") {
					t.Error("catalog default lost or rewrote the namespace-shaped literal")
				}
			}
			if !reflect.DeepEqual(reference, readOwnedSchemaCatalog(t, referencePool)) {
				t.Error("drift test modified the canonical reference")
			}
			if namespaceBoundaryFactsSnapshot(t, admin, baseSchema) != baseBefore || namespaceBoundaryFactsSnapshot(t, admin, neighborSchema) != neighborBefore {
				t.Error("target drift changed neighbor business facts")
			}
		})
	}
}

func TestSchemaCatalogRejectsExtraOwnerObjects(t *testing.T) {
	admin, baseSchema, neighborSchema := prepareStoreBoundaryFixture(t)
	seedNamespaceBoundaryFacts(t, openNamespaceBoundaryPool(t, baseSchema))
	seedNamespaceBoundaryFacts(t, openNamespaceBoundaryPool(t, neighborSchema))
	baseBefore := namespaceBoundaryFactsSnapshot(t, admin, baseSchema)
	neighborBefore := namespaceBoundaryFactsSnapshot(t, admin, neighborSchema)
	for _, kind := range []string{"function", "type", "view", "sequence", "trigger"} {
		t.Run(kind, func(t *testing.T) {
			_, pool := openIntegrationStore(t)
			before := readOwnedSchemaCatalog(t, pool)
			target := databaseTargetForPool(t, pool)
			schema := pgx.Identifier{target.SchemaName()}.Sanitize()
			var ddl string
			switch kind {
			case "function":
				ddl = "CREATE FUNCTION " + schema + ".owned_extra() RETURNS integer LANGUAGE sql AS 'SELECT 1'"
			case "type":
				ddl = "CREATE TYPE " + schema + ".owned_extra AS ENUM ('owned')"
			case "view":
				ddl = "CREATE VIEW " + schema + ".owned_extra AS SELECT 1 AS owned"
			case "sequence":
				ddl = "CREATE SEQUENCE " + schema + ".owned_extra"
			case "trigger":
				// Use a builtin function so this case proves the table-bound
				// trigger inventory, not rejection of a second owner function.
				ddl = "CREATE TRIGGER owned_extra BEFORE UPDATE ON " + schema + ".scheduler_schedule FOR EACH ROW EXECUTE FUNCTION pg_catalog.suppress_redundant_updates_trigger()"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, err := pool.Exec(ctx, ddl); err != nil {
				t.Fatal("create extra owner object failed")
			}
			var tableCount int
			if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relkind = 'r'", target.SchemaName()).Scan(&tableCount); err != nil || tableCount != len(before.Tables) {
				t.Fatal("extra-object fixture changed ordinary-table count")
			}
			if _, err := postgresadapter.ReadSchemaCatalog(ctx, pool, target); err == nil || !strings.Contains(err.Error(), "unsupported owner object") {
				t.Error("catalog silently accepted or filtered an unsupported extra owner object")
			}
			if namespaceBoundaryFactsSnapshot(t, admin, baseSchema) != baseBefore || namespaceBoundaryFactsSnapshot(t, admin, neighborSchema) != neighborBefore {
				t.Error("extra owner object fixture changed neighbor facts")
			}
		})
	}
}

func TestSchemaCatalogRejectsMissingTargetAndCancellationWithoutDDL(t *testing.T) {
	admin, baseSchema, neighborSchema := prepareStoreBoundaryFixture(t)
	seedNamespaceBoundaryFacts(t, openNamespaceBoundaryPool(t, baseSchema))
	seedNamespaceBoundaryFacts(t, openNamespaceBoundaryPool(t, neighborSchema))
	baseBefore := namespaceBoundaryFactsSnapshot(t, admin, baseSchema)
	neighborBefore := namespaceBoundaryFactsSnapshot(t, admin, neighborSchema)
	_, pool := openIntegrationStore(t)
	before := readOwnedSchemaCatalog(t, pool)
	target := databaseTargetForPool(t, pool)
	missingName := "scheduler_catalog_absent_" + strings.ToLower(rand.Text())
	missing := namespaceBoundaryTarget(t, os.Getenv("SCHEDULER_DATABASE_TEST_URL"), missingName)
	if boundarySchemaExists(t, admin, missingName) {
		t.Fatal("random missing catalog target unexpectedly exists")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := postgresadapter.ReadSchemaCatalog(ctx, pool, missing); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Error("catalog reader did not reject a missing explicit target")
	}
	if boundarySchemaExists(t, admin, missingName) {
		t.Error("catalog reader performed DDL for a missing target")
	}
	cancelled, cancelRead := context.WithCancel(ctx)
	cancelRead()
	if _, err := postgresadapter.ReadSchemaCatalog(cancelled, pool, target); !errors.Is(err, context.Canceled) {
		t.Error("catalog reader did not preserve cancellation identity")
	}
	if _, err := postgresadapter.ReadSchemaCatalog(ctx, nil, target); err == nil {
		t.Error("catalog reader accepted a nil pool")
	}
	if _, err := postgresadapter.ReadSchemaCatalog(ctx, pool, config.DatabaseTarget{}); err == nil {
		t.Error("catalog reader accepted an empty owner target")
	}
	if !reflect.DeepEqual(before, readOwnedSchemaCatalog(t, pool)) {
		t.Error("failed catalog reads modified target definitions")
	}
	if namespaceBoundaryFactsSnapshot(t, admin, baseSchema) != baseBefore || namespaceBoundaryFactsSnapshot(t, admin, neighborSchema) != neighborBefore {
		t.Error("failed catalog reads modified neighbor facts")
	}
}

func TestCompareSchemaCatalogIsPureAndPreservesOrderedValues(t *testing.T) {
	literal := "'owned_namespace'::text"
	identity := postgresadapter.SchemaCatalogIdentity{Namespace: "SELF", Name: "table"}
	table := postgresadapter.SchemaCatalogTable{
		Identity: identity,
		Columns: []postgresadapter.SchemaCatalogColumn{
			{Position: 1, Name: "first", Default: &literal},
			{Position: 2, Name: "second"},
		},
		Constraints: []postgresadapter.SchemaCatalogConstraint{{Name: "z", Definition: "CHECK (first > 0)"}, {Name: "a", Definition: "UNIQUE (second)"}},
		Indexes: []postgresadapter.SchemaCatalogIndex{
			{Identity: postgresadapter.SchemaCatalogIdentity{Namespace: "SELF", Name: "z"}, Columns: []postgresadapter.SchemaCatalogIndexColumn{{Position: 1, Definition: "first"}, {Position: 2, Definition: "second"}}},
			{Identity: postgresadapter.SchemaCatalogIdentity{Namespace: "SELF", Name: "a"}},
		},
	}
	reference := postgresadapter.SchemaCatalog{Tables: []postgresadapter.SchemaCatalogTable{table}}
	actualTable := table
	actualTable.Constraints = []postgresadapter.SchemaCatalogConstraint{table.Constraints[1], table.Constraints[0]}
	actualTable.Indexes = []postgresadapter.SchemaCatalogIndex{table.Indexes[1], table.Indexes[0]}
	actual := postgresadapter.SchemaCatalog{Tables: []postgresadapter.SchemaCatalogTable{actualTable}}
	if differences := postgresadapter.CompareSchemaCatalog(actual, reference); len(differences) != 0 {
		t.Errorf("object collection order was treated as semantic drift: %v", differences)
	}
	if actual.Tables[0].Constraints[0].Name != "a" || reference.Tables[0].Constraints[0].Name != "z" {
		t.Error("pure comparison mutated a caller's constraint collection")
	}
	if actual.Tables[0].Indexes[0].Identity.Name != "a" || reference.Tables[0].Indexes[0].Identity.Name != "z" {
		t.Error("pure comparison mutated a caller's index collection")
	}
	actualTable.Indexes = append([]postgresadapter.SchemaCatalogIndex(nil), table.Indexes...)
	actualTable.Indexes[0].Columns = []postgresadapter.SchemaCatalogIndexColumn{table.Indexes[0].Columns[1], table.Indexes[0].Columns[0]}
	actual.Tables[0] = actualTable
	first := postgresadapter.CompareSchemaCatalog(actual, reference)
	second := postgresadapter.CompareSchemaCatalog(actual, reference)
	if len(first) == 0 || !reflect.DeepEqual(first, second) {
		t.Error("comparison lost ordered index members or stable differences")
	}
	for _, difference := range first {
		if strings.Contains(fmt.Sprintf("%v", difference), literal) {
			t.Error("comparison exposed literal values in its diagnostic paths")
		}
	}
}

func readOwnedSchemaCatalog(t *testing.T, pool *pgxpool.Pool) postgresadapter.SchemaCatalog {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	snapshot, err := postgresadapter.ReadSchemaCatalog(ctx, pool, databaseTargetForPool(t, pool))
	if err != nil {
		t.Fatal("read actual owned schema catalog failed")
	}
	return snapshot
}

func ownedCatalogColumn(t *testing.T, snapshot postgresadapter.SchemaCatalog, tableName, columnName string) postgresadapter.SchemaCatalogColumn {
	t.Helper()
	for _, table := range snapshot.Tables {
		if table.Identity.Name == tableName {
			for _, column := range table.Columns {
				if column.Name == columnName && !column.Dropped {
					return column
				}
			}
		}
	}
	t.Fatal("catalog fixture column was not found")
	return postgresadapter.SchemaCatalogColumn{}
}
