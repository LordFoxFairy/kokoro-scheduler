package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
	"github.com/LordFoxFairy/kokoro-scheduler/test/doubles"
)

func newTestScheduler(t *testing.T, target *doubles.TargetClient, leaseStore ports.LeaseStore) (*Scheduler, *doubles.ScheduleEngine) {
	t.Helper()
	scheduler, engine, _, _, _ := newTestSchedulerWithRetryDependencies(t, target, leaseStore)
	return scheduler, engine
}

func newTestSchedulerWithRetryDependencies(t *testing.T, target *doubles.TargetClient, leaseStore ports.LeaseStore) (*Scheduler, *doubles.ScheduleEngine, *doubles.Clock, *doubles.Sleeper, *doubles.RandomSource) {
	t.Helper()
	engine := doubles.NewScheduleEngine()
	clock := doubles.NewClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("fixture", -5*60*60)))
	sleeper := &doubles.Sleeper{Clock: clock}
	random := &doubles.RandomSource{}
	scheduler, err := NewScheduler(Dependencies{
		Engine:          engine,
		Clock:           clock,
		Sleeper:         sleeper,
		Random:          random,
		LeaseStore:      leaseStore,
		TargetClient:    target,
		DispatchTimeout: time.Second,
		LeaseTTL:        2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return scheduler, engine, clock, sleeper, random
}

func testJob() domain.Job {
	return domain.Job{Name: "job", Schedule: "@every 1m", URL: "http://service.test/command", Body: []byte(`{"tenant_id":"TENANT"}`), Retry: domain.RetryPolicy{MaxAttempts: 1}}
}

func TestSchedulerRegistersUpdatesPausesAndRemovesJobs(t *testing.T) {
	scheduler, _ := newTestScheduler(t, &doubles.TargetClient{}, nil)
	job := testJob()
	if err := scheduler.Register(job); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Register(job); !errors.Is(err, domain.ErrJobAlreadyExists) {
		t.Fatalf("duplicate registration error = %v", err)
	}
	if err := scheduler.Pause(job.Name); err != nil {
		t.Fatal(err)
	}
	stored, ok := scheduler.Job(job.Name)
	if !ok || !stored.Paused {
		t.Fatalf("paused job = %#v, exists=%v", stored, ok)
	}
	job.Retry.MaxAttempts = 2
	if err := scheduler.Update(job); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Resume(job.Name); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Remove(job.Name); err != nil {
		t.Fatal(err)
	}
	if _, ok := scheduler.Job(job.Name); ok {
		t.Fatal("removed job is still registered")
	}
}

func TestSchedulerReadinessTracksLifecycle(t *testing.T) {
	scheduler, _ := newTestScheduler(t, &doubles.TargetClient{}, nil)
	if scheduler.Ready() {
		t.Fatal("new scheduler must not be ready before Start")
	}
	scheduler.Start()
	if !scheduler.Ready() {
		t.Fatal("started scheduler must be ready")
	}
	if err := scheduler.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scheduler.Ready() {
		t.Fatal("stopped scheduler must not be ready")
	}
}

func TestSchedulerRetriesTransientTargetFailureWithStableIdentity(t *testing.T) {
	target := &doubles.TargetClient{Results: []domain.RunResult{
		{Status: 503, Err: errors.New("temporarily unavailable")},
		{Status: 202},
	}}
	scheduler, _ := newTestScheduler(t, target, nil)
	job := testJob()
	job.Retry.MaxAttempts = 2
	job.Retry.BackoffSeconds = 0
	var observed domain.RunResult
	scheduler.observer = ports.RunObserverFunc(func(_ domain.Job, result domain.RunResult) { observed = result })
	result := scheduler.Dispatch(context.Background(), job, time.Unix(100, 0), time.Unix(100, 0))
	if result.Err != nil || result.Status != 202 || result.Attempts != 2 || target.CallCount() != 2 {
		t.Fatalf("result=%#v calls=%d", result, target.CallCount())
	}
	if result.RequestID != "sched_job_19700101T000140Z" || result.IdempotencyKey != "schedule:job:19700101T000140Z" {
		t.Fatalf("unstable request identity: %#v", result)
	}
	if observed.Attempts != 2 {
		t.Fatalf("observer result = %#v", observed)
	}
}

func TestSchedulerLeasePreventsDuplicateOccurrenceConcurrently(t *testing.T) {
	target := &doubles.TargetClient{}
	leaseStore := doubles.NewLeaseStore()
	scheduler, _ := newTestScheduler(t, target, leaseStore)
	job := testJob()
	const calls = 32
	var wg sync.WaitGroup
	wg.Add(calls)
	for i := 0; i < calls; i++ {
		go func() {
			defer wg.Done()
			scheduler.Dispatch(context.Background(), job, time.Unix(100, 0), time.Unix(100, 0))
		}()
	}
	wg.Wait()
	if got := target.CallCount(); got != 1 {
		t.Fatalf("target calls = %d, want exactly one", got)
	}
}

func TestSchedulerCancelsAnInFlightDispatchOnStop(t *testing.T) {
	block := make(chan struct{})
	target := &doubles.TargetClient{Block: block}
	scheduler, _ := newTestScheduler(t, target, nil)
	job := testJob()
	resultCh := make(chan domain.RunResult, 1)
	go func() {
		resultCh <- scheduler.Dispatch(context.Background(), job, time.Unix(100, 0), time.Unix(100, 0))
	}()
	deadline := time.After(time.Second)
	for target.CallCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("dispatch did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := scheduler.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-resultCh:
		if !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("cancellation result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatch did not stop")
	}
}

func TestSchedulerRenewsLongRunningLease(t *testing.T) {
	block := make(chan struct{})
	target := &doubles.TargetClient{Block: block}
	leaseStore := doubles.NewLeaseStore()
	scheduler, _ := newTestScheduler(t, target, leaseStore)
	scheduler.dispatchTimeout = 3 * time.Second
	job := testJob()
	resultCh := make(chan domain.RunResult, 1)
	go func() {
		resultCh <- scheduler.Dispatch(context.Background(), job, time.Unix(100, 0), time.Unix(100, 0))
	}()
	deadline := time.After(time.Second)
	for target.CallCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("dispatch did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	time.Sleep(1100 * time.Millisecond)
	close(block)
	result := <-resultCh
	if result.Err != nil || leaseStore.RenewalCount() == 0 {
		t.Fatalf("result=%#v renewals=%d", result, leaseStore.RenewalCount())
	}
}

func TestSchedulerUsesCappedExponentialBackoffWithFullJitter(t *testing.T) {
	target := &doubles.TargetClient{Results: []domain.RunResult{
		{Status: 503, Err: errors.New("temporarily unavailable")},
		{Status: 503, Err: errors.New("temporarily unavailable")},
		{Status: 503, Err: errors.New("temporarily unavailable")},
		{Status: 202},
	}}
	scheduler, _, _, sleeper, random := newTestSchedulerWithRetryDependencies(t, target, nil)
	random.Values = []int64{int64(3 * time.Second), int64(14 * time.Second), int64(15 * time.Second)}
	job := testJob()
	job.Retry = domain.RetryPolicy{
		MaxAttempts:           4,
		BackoffSeconds:        10,
		MaxBackoffSeconds:     15,
		MaxRetryWindowSeconds: 100,
	}

	result := scheduler.Dispatch(context.Background(), job, time.Unix(100, 0), time.Unix(100, 0))
	if result.Err != nil || result.Status != 202 || result.Attempts != 4 {
		t.Fatalf("result = %#v", result)
	}
	wantWaits := []time.Duration{3 * time.Second, 14 * time.Second, 15 * time.Second}
	if got := sleeper.Waits(); !equalDurations(got, wantWaits) {
		t.Fatalf("retry waits = %v, want %v", got, wantWaits)
	}
	wantBounds := []int64{int64(10*time.Second) + 1, int64(15*time.Second) + 1, int64(15*time.Second) + 1}
	if got := random.UpperBounds(); !equalInt64s(got, wantBounds) {
		t.Fatalf("jitter upper bounds = %v, want %v", got, wantBounds)
	}
}

func TestSchedulerDoesNotRetryBeyondMaximumRetryWindow(t *testing.T) {
	target := &doubles.TargetClient{Results: []domain.RunResult{
		{Status: 503, Err: errors.New("temporarily unavailable")},
		{Status: 503, Err: errors.New("temporarily unavailable")},
		{Status: 202},
	}}
	scheduler, _, _, sleeper, random := newTestSchedulerWithRetryDependencies(t, target, nil)
	random.Values = []int64{int64(10 * time.Second), int64(2 * time.Second)}
	job := testJob()
	job.Retry = domain.RetryPolicy{
		MaxAttempts:           5,
		BackoffSeconds:        10,
		MaxBackoffSeconds:     20,
		MaxRetryWindowSeconds: 12,
	}

	result := scheduler.Dispatch(context.Background(), job, time.Unix(100, 0), time.Unix(100, 0))
	if result.Attempts != 2 || target.CallCount() != 2 {
		t.Fatalf("result = %#v, target calls = %d", result, target.CallCount())
	}
	wantWaits := []time.Duration{10 * time.Second, 2 * time.Second}
	if got := sleeper.Waits(); !equalDurations(got, wantWaits) {
		t.Fatalf("retry waits = %v, want %v", got, wantWaits)
	}
}

func TestSchedulerStopsRetryWhenJitterSourceFails(t *testing.T) {
	target := &doubles.TargetClient{Results: []domain.RunResult{
		{Status: 503, Err: errors.New("temporarily unavailable")},
		{Status: 202},
	}}
	scheduler, _, _, sleeper, random := newTestSchedulerWithRetryDependencies(t, target, nil)
	random.Err = errors.New("entropy unavailable")
	job := testJob()
	job.Retry.MaxAttempts = 2

	result := scheduler.Dispatch(context.Background(), job, time.Unix(100, 0), time.Unix(100, 0))
	if result.Code != "SCHEDULER_RETRY_JITTER_UNAVAILABLE" || result.Attempts != 1 || target.CallCount() != 1 {
		t.Fatalf("result = %#v, target calls = %d", result, target.CallCount())
	}
	if len(sleeper.Waits()) != 0 {
		t.Fatalf("unexpected waits after jitter failure: %v", sleeper.Waits())
	}
}

func equalDurations(left, right []time.Duration) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func equalInt64s(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
