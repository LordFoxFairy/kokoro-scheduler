package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoadJobsTreatsEmptyConfigAsNoJobs(t *testing.T) {
	for _, raw := range []string{"", " \n\t  "} {
		jobs, err := LoadJobs(raw)
		if err != nil {
			t.Fatalf("LoadJobs(%q) returned error: %v", raw, err)
		}
		if jobs == nil || len(jobs) != 0 {
			t.Fatalf("LoadJobs(%q) = %#v, want a non-nil empty jobs list", raw, jobs)
		}
	}
}

func TestLoadJobsUsesCronSpecAndRejectsUnknownFields(t *testing.T) {
	jobs, err := LoadJobs(`[ {"name":"billing.reconcile","schedule":"@every 1m","url":"http://service.test/command","method":"POST","body":{"tenantId":"TENANT"}} ]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Schedule != "@every 1m" {
		t.Fatalf("unexpected jobs: %#v", jobs)
	}
	if _, err := LoadJobs(`[{"name":"x","schedule":"@every 1m","url":"http://x","extra":true}]`); err == nil {
		t.Fatal("expected unknown field error")
	}
	if _, err := LoadJobs(`[{"name":"x","schedule":"not-a-cron","url":"http://x"}]`); err == nil {
		t.Fatal("expected invalid cron error")
	}
	if _, err := LoadJobs(`[{"name":"x","schedule":"@every 1m","url":"http://x"},{"name":"x","schedule":"@every 2m","url":"http://x"}]`); err == nil {
		t.Fatal("expected duplicate job error")
	}
	if _, err := LoadJobs(`[] []`); err == nil {
		t.Fatal("expected trailing JSON error")
	}
}

func TestLoadJobsParsesRetryPauseAndMisfirePolicy(t *testing.T) {
	jobs, err := LoadJobs(`[{
		"name":"billing.reconcile",
		"schedule":"@every 1m",
		"url":"http://service.test/command",
		"retry":{"max_attempts":3,"backoff_seconds":0},
		"misfire_policy":"fire_once",
		"paused":true
	}]`)
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].Retry.MaxAttempts != 3 || jobs[0].Retry.BackoffSeconds != 0 || jobs[0].MisfirePolicy != MisfireFireOnce || !jobs[0].Paused {
		t.Fatalf("unexpected scheduling policy: %#v", jobs[0])
	}

	if _, err := LoadJobs(`[{"name":"x","schedule":"@every 1m","url":"http://x","retry":{"max_attempts":-1}}]`); err == nil {
		t.Fatal("expected invalid max_attempts error")
	}
	if _, err := LoadJobs(`[{"name":"x","schedule":"@every 1m","url":"http://x","misfire_policy":"run_everything"}]`); err == nil {
		t.Fatal("expected invalid misfire policy error")
	}
}

func TestHTTPJobRunnerDispatchesWithTimeoutAndHeader(t *testing.T) {
	var gotHeader, requestID, idempotencyKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Kokoro-Scheduler-Job")
		requestID = r.Header.Get("X-Request-Id")
		idempotencyKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	job := Job{Name: "billing.reconcile", Schedule: "@every 1m", URL: srv.URL, Method: http.MethodPost, Body: map[string]any{"tenantId": "TENANT"}}
	result := NewHTTPRunner(2*time.Second).RunAt(context.Background(), job, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if result.Err != nil || result.Status != http.StatusAccepted || gotHeader != job.Name || requestID == "" || idempotencyKey == "" {
		t.Fatalf("unexpected result: %#v job=%q request_id=%q idempotency_key=%q", result, gotHeader, requestID, idempotencyKey)
	}
	if requestID != "sched_billing.reconcile_20260102T030405Z" || idempotencyKey != "schedule:billing.reconcile:20260102T030405Z" {
		t.Fatalf("unexpected request identity: request_id=%q idempotency_key=%q", requestID, idempotencyKey)
	}
}

func TestOccurrenceKeyIsSharedByInstancesForTheSameCronWindow(t *testing.T) {
	job := Job{Name: "billing.reconcile", Schedule: "0 * * * *"}
	got := OccurrenceKey(job, time.Date(2026, 8, 31, 12, 0, 30, 0, time.UTC))
	want := "kokoro:scheduler:run:billing.reconcile:202608311200"
	if got != want {
		t.Fatalf("occurrence key = %q, want %q", got, want)
	}
}

func TestRequestIdentityIsStableForAnOccurrence(t *testing.T) {
	job := Job{Name: "daily", Schedule: "0 0 * * *", URL: "http://TARGET/run"}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	requestID, idempotencyKey := RequestIdentity(job, at)
	if requestID == "" || idempotencyKey == "" {
		t.Fatal("request identity must contain both request_id and idempotency key")
	}
	if requestID != "sched_daily_20260102T030405Z" {
		t.Fatalf("unexpected request id: %q", requestID)
	}
	if idempotencyKey != "schedule:daily:20260102T030405Z" {
		t.Fatalf("unexpected idempotency key: %q", idempotencyKey)
	}

	requestIDAgain, idempotencyKeyAgain := RequestIdentity(job, at)
	if requestIDAgain != requestID || idempotencyKeyAgain != idempotencyKey {
		t.Fatal("request identity must be deterministic for the same occurrence")
	}
}

type testLocker struct {
	acquired bool
	key      string
}

func (l *testLocker) Acquire(_ context.Context, key string, _ time.Duration) (func(), bool, error) {
	l.key = key
	if l.acquired {
		return func() {}, false, nil
	}
	l.acquired = true
	return func() { l.acquired = false }, true, nil
}

type testRunner struct{ calls int }

func (r *testRunner) Run(context.Context, Job) RunResult {
	r.calls++
	return RunResult{Status: http.StatusAccepted}
}

type retryRunner struct{ calls int }

func (r *retryRunner) Run(context.Context, Job) RunResult {
	r.calls++
	if r.calls == 1 {
		return RunResult{Status: http.StatusBadGateway, Err: context.DeadlineExceeded}
	}
	return RunResult{Status: http.StatusAccepted}
}

func TestServiceRetriesTransientFailuresAndSupportsPauseResume(t *testing.T) {
	runner := &retryRunner{}
	var logged RunResult
	service := NewService(runner, func(_ Job, result RunResult) { logged = result })
	job := Job{
		Name:     "job",
		Schedule: "@every 1m",
		URL:      "http://service.test",
		Retry:    RetryPolicy{MaxAttempts: 2},
		Paused:   true,
	}
	if err := service.Add(job); err != nil {
		t.Fatal(err)
	}
	service.run(job, time.Unix(100, 0))
	if runner.calls != 0 {
		t.Fatalf("paused runner calls = %d, want 0", runner.calls)
	}
	service.Resume(job.Name)
	service.run(job, time.Unix(100, 0))
	if runner.calls != 2 || logged.Status != http.StatusAccepted || logged.Err != nil || logged.Attempts != 2 || logged.RequestID == "" || logged.IdempotencyKey == "" {
		t.Fatalf("unexpected retry result: calls=%d logged=%#v", runner.calls, logged)
	}
	service.Pause(job.Name)
	service.run(job, time.Unix(101, 0))
	if runner.calls != 2 {
		t.Fatalf("paused-after-resume runner calls = %d, want 2", runner.calls)
	}
}

func TestServiceAppliesMisfirePolicyToExplicitRecoveryTrigger(t *testing.T) {
	runner := &testRunner{}
	service := NewService(runner, func(Job, RunResult) {})
	skip := Job{Name: "skip", Schedule: "@every 1m", URL: "http://service.test", MisfirePolicy: MisfireSkip, Retry: RetryPolicy{MaxAttempts: 1}}
	service.Trigger(skip, time.Unix(100, 0), time.Unix(101, 0))
	if runner.calls != 0 {
		t.Fatalf("skip misfire calls = %d, want 0", runner.calls)
	}
	fireOnce := Job{Name: "fire-once", Schedule: "@every 1m", URL: "http://service.test", MisfirePolicy: MisfireFireOnce, Retry: RetryPolicy{MaxAttempts: 1}}
	service.Trigger(fireOnce, time.Unix(100, 0), time.Unix(101, 0))
	if runner.calls != 1 {
		t.Fatalf("fire_once misfire calls = %d, want 1", runner.calls)
	}
}

func TestServiceDoesNotRunTheSameOccurrenceTwiceAcrossInstances(t *testing.T) {
	locker := &testLocker{acquired: true}
	runner := &testRunner{}
	service := NewService(runner, func(Job, RunResult) {}, locker)
	job := Job{Name: "job", Schedule: "@every 500ms", URL: "http://service.test"}
	service.run(job, time.Unix(100, 500_000_000))
	if runner.calls != 0 {
		t.Fatalf("runner calls = %d, want 0", runner.calls)
	}
	if locker.key == "" {
		t.Fatal("expected occurrence claim")
	}
}

func TestOccurrenceKeySupportsSubsecondEverySchedules(t *testing.T) {
	job := Job{Name: "fast", Schedule: "@every 500ms"}
	first := OccurrenceKey(job, time.Unix(10, 0))
	second := OccurrenceKey(job, time.Unix(10, 500_000_000))
	if first == second {
		t.Fatalf("subsecond occurrences collided: %q", first)
	}
}
