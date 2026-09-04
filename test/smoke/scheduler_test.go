package smoke_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	postgresadapter "github.com/LordFoxFairy/kokoro-scheduler/internal/adapters/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSourceBinaryRecoversDurableCommandReceiptAfterRestart(t *testing.T) {
	baseURL := os.Getenv("SCHEDULER_DATABASE_TEST_URL")
	if baseURL == "" {
		t.Skip("SCHEDULER_DATABASE_TEST_URL is not configured")
	}
	root := repositoryRoot(t)
	databaseURL, pool := isolatedDatabaseSchema(t, baseURL)
	binary := filepath.Join(t.TempDir(), "kokoro-scheduler")
	build := exec.Command("go", "build", "-trimpath", "-o", binary, "./cmd/scheduler")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build source scheduler: %v\n%s", err, output)
	}
	address := availableAddress(t)
	serviceURL := "http://" + address
	environment := schedulerEnvironment(databaseURL, address)

	firstProcess := startScheduler(t, binary, root, environment, serviceURL+"/readyz")
	firstStatus, firstBody, firstRequestID := createSchedule(t, serviceURL, "request-before-restart")
	if firstStatus != http.StatusOK || firstRequestID != "request-before-restart" {
		t.Fatalf("first command status=%d request_id=%q body=%s", firstStatus, firstRequestID, firstBody)
	}
	firstProcess.stop(t)

	secondProcess := startScheduler(t, binary, root, environment, serviceURL+"/readyz")
	defer secondProcess.stop(t)
	replayStatus, replayBody, replayRequestID := createSchedule(t, serviceURL, "request-after-restart")
	if replayStatus != firstStatus || replayBody != firstBody || replayRequestID != "request-before-restart" {
		t.Fatalf("replay status=%d request_id=%q body=%s; original status=%d body=%s", replayStatus, replayRequestID, replayBody, firstStatus, firstBody)
	}

	var schedules, receipts int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM scheduler_schedule WHERE tenant_id = 'tenant-smoke'`).Scan(&schedules); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM scheduler_command_receipt WHERE tenant_id = 'tenant-smoke'`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if schedules != 1 || receipts != 1 {
		t.Fatalf("durable smoke facts schedules=%d receipts=%d", schedules, receipts)
	}
}

type schedulerProcess struct {
	command *exec.Cmd
	done    chan error
	log     *bytes.Buffer
}

func startScheduler(t *testing.T, binary, root string, environment []string, readinessURL string) *schedulerProcess {
	t.Helper()
	command := exec.Command(binary)
	command.Dir = root
	command.Env = environment
	log := &bytes.Buffer{}
	command.Stdout = log
	command.Stderr = log
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	process := &schedulerProcess{command: command, done: make(chan error, 1), log: log}
	go func() { process.done <- command.Wait() }()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-process.done:
			t.Fatalf("scheduler exited before readiness: %v\n%s", err, log.String())
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, readinessURL, nil)
		response, err := http.DefaultClient.Do(request)
		cancel()
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return process
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	process.stop(t)
	t.Fatalf("scheduler did not become ready\n%s", log.String())
	return nil
}

func (p *schedulerProcess) stop(t *testing.T) {
	t.Helper()
	if p == nil || p.command == nil || p.command.Process == nil {
		return
	}
	_ = p.command.Process.Signal(os.Interrupt)
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatalf("scheduler shutdown: %v\n%s", err, p.log.String())
		}
	case <-time.After(10 * time.Second):
		_ = p.command.Process.Kill()
		<-p.done
		t.Fatalf("scheduler shutdown timed out\n%s", p.log.String())
	}
	p.command = nil
}

func createSchedule(t *testing.T, baseURL, requestID string) (int, string, string) {
	t.Helper()
	body := `{"schedule":"@every 1h","timezone":"America/New_York","url":"http://service.test/command","misfire_policy":"fire_once","overlap_policy":"forbid"}`
	request, err := http.NewRequest(http.MethodPost, baseURL+"/internal/scheduler/v1/schedules/smoke.recover", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer smoke-token")
	request.Header.Set("X-Kokoro-Tenant-Id", "tenant-smoke")
	request.Header.Set("X-Request-Id", requestID)
	request.Header.Set("Idempotency-Key", "smoke-restart-key")
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(responseBody), response.Header.Get("X-Request-Id")
}

func schedulerEnvironment(databaseURL, address string) []string {
	keys := map[string]struct{}{
		"SCHEDULER_DATABASE_URL": {}, "SCHEDULER_REDIS_URL": {}, "SCHEDULER_HTTP_ADDR": {},
		"SCHEDULER_INTERNAL_SERVICE_TOKEN": {}, "SCHEDULER_WAKEUP_INTERVAL": {},
		"SCHEDULER_CLAIM_TTL": {}, "SCHEDULER_DISPATCH_TIMEOUT": {}, "SCHEDULER_WORKER_ID": {},
	}
	environment := make([]string, 0, len(os.Environ())+8)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if _, replaced := keys[name]; !replaced {
			environment = append(environment, value)
		}
	}
	environment = append(environment,
		"SCHEDULER_DATABASE_URL="+databaseURL,
		"SCHEDULER_HTTP_ADDR="+address,
		"SCHEDULER_INTERNAL_SERVICE_TOKEN=smoke-token",
		"SCHEDULER_WAKEUP_INTERVAL=50ms",
		"SCHEDULER_CLAIM_TTL=3s",
		"SCHEDULER_DISPATCH_TIMEOUT=1s",
		"SCHEDULER_WORKER_ID=smoke-worker",
	)
	if redisURL := os.Getenv("SCHEDULER_REDIS_TEST_URL"); redisURL != "" {
		environment = append(environment, "SCHEDULER_REDIS_URL="+redisURL)
	}
	return environment
}

func isolatedDatabaseSchema(t *testing.T, rawURL string) (string, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schemaName := fmt.Sprintf("scheduler_smoke_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE") })
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schemaName)
	parsed.RawQuery = query.Encode()
	isolatedURL := parsed.String()
	poolConfig, err := pgxpool.ParseConfig(isolatedURL)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := postgresadapter.ApplySchemaToEmptyDatabase(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return isolatedURL, pool
}

func availableAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve smoke test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))
}
