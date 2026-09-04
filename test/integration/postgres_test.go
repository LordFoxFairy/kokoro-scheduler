package integration_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	postgresadapter "github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/postgres"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/recurrence"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/application"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
	"github.com/LordFoxFairy/kokoro-scheduler/test/doubles"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestApplySchemaInstallsOnlyIntoAnEmptyDatabaseNamespace(t *testing.T) {
	rawURL := os.Getenv("SCHEDULER_DATABASE_TEST_URL")
	if rawURL == "" {
		t.Skip("SCHEDULER_DATABASE_TEST_URL is not configured")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schemaName := fmt.Sprintf("scheduler_bootstrap_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE") }()

	poolConfig, err := pgxpool.ParseConfig(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := postgresadapter.ApplySchemaToEmptyDatabase(ctx, pool); err != nil {
		t.Fatalf("apply fresh schema: %v", err)
	}
	var tableCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname = current_schema()`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 4 {
		t.Fatalf("installed tables = %d, want 4", tableCount)
	}
	if err := postgresadapter.ApplySchemaToEmptyDatabase(ctx, pool); err == nil || !strings.Contains(err.Error(), "requires an empty database") {
		t.Fatalf("second apply error = %v, want non-empty rejection", err)
	}
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
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var databaseName string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	if len(databaseName) < len("kokoro_scheduler_test") || databaseName[:len("kokoro_scheduler_test")] != "kokoro_scheduler_test" {
		t.Fatalf("integration database %q must use the kokoro_scheduler_test prefix", databaseName)
	}
	_, file, _, _ := runtime.Caller(0)
	schemaPath := filepath.Clean(filepath.Join(filepath.Dir(file), "../..", "database", "schema.sql"))
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(schema)); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE scheduler_dispatch_outbox, scheduler_occurrence, scheduler_command_receipt, scheduler_schedule`); err != nil {
		t.Fatal(err)
	}
	return postgresadapter.NewStore(pool), pool
}
