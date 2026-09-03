package application

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
)

const (
	DefaultDispatchTimeout = 30 * time.Second
	DefaultLeaseTTL        = 26 * time.Hour
)

var ErrInvalidScheduler = errors.New("scheduler dependencies are incomplete")

type Dependencies struct {
	Engine          ports.ScheduleEngine
	Clock           ports.Clock
	Sleeper         ports.Sleeper
	Random          ports.RandomSource
	LeaseStore      ports.LeaseStore
	TargetClient    ports.TargetClient
	Observer        ports.RunObserver
	DispatchTimeout time.Duration
	LeaseTTL        time.Duration
}

type Scheduler struct {
	engine          ports.ScheduleEngine
	clock           ports.Clock
	sleeper         ports.Sleeper
	random          ports.RandomSource
	leaseStore      ports.LeaseStore
	targetClient    ports.TargetClient
	observer        ports.RunObserver
	dispatchTimeout time.Duration
	leaseTTL        time.Duration
	ctx             context.Context
	cancel          context.CancelFunc
	inFlight        sync.WaitGroup
	ready           atomic.Bool

	mu      sync.RWMutex
	jobs    map[string]domain.Job
	entries map[string]ports.EntryID
}

func NewScheduler(deps Dependencies) (*Scheduler, error) {
	if deps.Engine == nil || deps.Clock == nil || deps.Sleeper == nil || deps.Random == nil || deps.TargetClient == nil {
		return nil, ErrInvalidScheduler
	}
	if deps.Observer == nil {
		deps.Observer = ports.NoopObserver{}
	}
	if deps.DispatchTimeout <= 0 {
		deps.DispatchTimeout = DefaultDispatchTimeout
	}
	if deps.LeaseTTL <= 0 {
		deps.LeaseTTL = DefaultLeaseTTL
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		engine:          deps.Engine,
		clock:           deps.Clock,
		sleeper:         deps.Sleeper,
		random:          deps.Random,
		leaseStore:      deps.LeaseStore,
		targetClient:    deps.TargetClient,
		observer:        deps.Observer,
		dispatchTimeout: deps.DispatchTimeout,
		leaseTTL:        deps.LeaseTTL,
		ctx:             ctx,
		cancel:          cancel,
		jobs:            make(map[string]domain.Job),
		entries:         make(map[string]ports.EntryID),
	}, nil
}

func (s *Scheduler) Register(job domain.Job) error {
	normalized, err := job.Normalized()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[normalized.Name]; exists {
		return domain.ErrJobAlreadyExists
	}
	entry, err := s.addEntryLocked(normalized)
	if err != nil {
		return err
	}
	s.jobs[normalized.Name] = normalized
	s.entries[normalized.Name] = entry
	return nil
}

func (s *Scheduler) Update(job domain.Job) error {
	normalized, err := job.Normalized()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oldEntry, exists := s.entries[normalized.Name]
	if !exists {
		return domain.ErrJobNotFound
	}
	newEntry, err := s.addEntryLocked(normalized)
	if err != nil {
		return err
	}
	s.engine.Remove(oldEntry)
	s.jobs[normalized.Name] = normalized
	s.entries[normalized.Name] = newEntry
	return nil
}

func (s *Scheduler) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[name]
	if !exists {
		return domain.ErrJobNotFound
	}
	s.engine.Remove(entry)
	delete(s.jobs, name)
	delete(s.entries, name)
	return nil
}

func (s *Scheduler) Pause(name string) error  { return s.setPaused(name, true) }
func (s *Scheduler) Resume(name string) error { return s.setPaused(name, false) }

func (s *Scheduler) setPaused(name string, paused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, exists := s.jobs[name]
	if !exists {
		return domain.ErrJobNotFound
	}
	job.Paused = paused
	s.jobs[name] = job
	return nil
}

func (s *Scheduler) Job(name string) (domain.Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, exists := s.jobs[name]
	if job.Body != nil {
		job.Body = append([]byte(nil), job.Body...)
	}
	return job, exists
}

func (s *Scheduler) Jobs() []domain.Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	jobs := make([]domain.Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		if job.Body != nil {
			job.Body = append([]byte(nil), job.Body...)
		}
		jobs = append(jobs, job)
	}
	return jobs
}

func (s *Scheduler) addEntryLocked(job domain.Job) (ports.EntryID, error) {
	return s.engine.Add(job.Schedule, func() {
		now := s.clock.Now().UTC()
		s.Dispatch(context.Background(), job, now, now)
	})
}

// Dispatch executes one occurrence. scheduledAt is the intended UTC instant;
// observedAt is when this process noticed it. Keeping both makes restart
// recovery and misfire policy explicit without introducing a business store.
func (s *Scheduler) Dispatch(ctx context.Context, job domain.Job, scheduledAt, observedAt time.Time) (result domain.RunResult) {
	s.inFlight.Add(1)
	defer s.inFlight.Done()
	startedAt := s.clock.Now().UTC()
	observedJob := job
	defer func() {
		result.Duration = s.clock.Now().UTC().Sub(startedAt)
		if result.Duration < 0 {
			result.Duration = 0
		}
		s.observer.Observe(observedJob, result)
	}()

	normalized, err := job.Normalized()
	if err != nil {
		return domain.RunResult{Err: err, Code: "SCHEDULER_CONFIG_INVALID"}
	}
	observedJob = normalized
	normalizedOccurrence := domain.NewOccurrence(normalized.Name, scheduledAt, observedAt)
	requestID, idempotencyKey := domain.RequestIdentity(normalized, normalizedOccurrence.ScheduledAt)
	traceID := domain.TraceIdentity(normalized, normalizedOccurrence.ScheduledAt)
	if normalizedOccurrence.ObservedAt.After(normalizedOccurrence.ScheduledAt) && normalized.MisfirePolicy == domain.MisfireSkip {
		return domain.RunResult{Code: "SCHEDULER_MISFIRED", RequestID: requestID, IdempotencyKey: idempotencyKey, TraceID: traceID}
	}
	if current, registered := s.Job(normalized.Name); registered {
		if current.Paused {
			return domain.RunResult{Code: "SCHEDULER_PAUSED", RequestID: requestID, IdempotencyKey: idempotencyKey, TraceID: traceID}
		}
		if !sameJob(current, normalized) {
			return domain.RunResult{Code: "SCHEDULER_STALE_TRIGGER", RequestID: requestID, IdempotencyKey: idempotencyKey, TraceID: traceID}
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-s.ctx.Done():
			cancel()
		case <-runCtx.Done():
		}
	}()

	var lease ports.Lease
	if s.leaseStore != nil {
		acquiredLease, acquired, acquireErr := s.leaseStore.Acquire(runCtx, domain.OccurrenceKey(normalized, normalizedOccurrence.ScheduledAt), s.leaseTTL)
		if acquireErr != nil {
			return domain.RunResult{Err: fmt.Errorf("scheduler coordination failed: %w", acquireErr), Code: "SCHEDULER_COORDINATION_UNAVAILABLE", RequestID: requestID, IdempotencyKey: idempotencyKey, TraceID: traceID}
		}
		if !acquired {
			return domain.RunResult{Code: "SCHEDULER_OCCURRENCE_ALREADY_CLAIMED", RequestID: requestID, IdempotencyKey: idempotencyKey, TraceID: traceID}
		}
		lease = acquiredLease
	}

	leaseErrs := make(chan error, 1)
	stopRenewal := func() {}
	if lease != nil {
		stopRenewal = startLeaseRenewal(runCtx, lease, s.leaseTTL, leaseErrs, cancel)
	}
	result = s.dispatchWithRetry(runCtx, normalized, normalizedOccurrence)
	// Stop and join the renewal goroutine before inspecting its terminal error
	// or releasing the lease. This prevents a late Renew from racing a failed
	// occurrence's Release.
	stopRenewal()
	select {
	case leaseErr := <-leaseErrs:
		if leaseErr != nil {
			result.Err = fmt.Errorf("scheduler lease lost: %w", leaseErr)
			result.Code = "SCHEDULER_COORDINATION_UNAVAILABLE"
		}
	default:
	}
	if result.RequestID == "" || result.IdempotencyKey == "" {
		result.RequestID, result.IdempotencyKey = requestID, idempotencyKey
	}
	if result.TraceID == "" {
		result.TraceID = traceID
	}
	if lease != nil && !result.Succeeded() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = lease.Release(releaseCtx)
		releaseCancel()
	}
	return result
}

func (s *Scheduler) dispatchWithRetry(ctx context.Context, job domain.Job, occurrence domain.Occurrence) domain.RunResult {
	result := domain.RunResult{}
	retryDeadline := s.clock.Now().UTC().Add(time.Duration(job.Retry.MaxRetryWindowSeconds) * time.Second)
	for attempt := 1; attempt <= job.Retry.MaxAttempts; attempt++ {
		if attempt > 1 && !s.clock.Now().UTC().Before(retryDeadline) {
			return result
		}
		if err := ctx.Err(); err != nil {
			return domain.RunResult{Err: err, Code: "SCHEDULER_CANCELLED", Attempts: attempt - 1}
		}
		attemptCtx, cancel := context.WithTimeout(ctx, s.dispatchTimeout)
		result = s.targetClient.Dispatch(attemptCtx, job, occurrence)
		cancel()
		result.Attempts = attempt
		if err := ctx.Err(); err != nil {
			return domain.RunResult{Err: err, Code: "SCHEDULER_CANCELLED", Attempts: attempt}
		}
		if result.Succeeded() || !domain.Retryable(result) || attempt == job.Retry.MaxAttempts {
			return result
		}
		remaining := retryDeadline.Sub(s.clock.Now().UTC())
		if remaining <= 0 {
			return result
		}
		backoffCeiling := retryBackoff(job.Retry, attempt)
		if backoffCeiling > remaining {
			backoffCeiling = remaining
		}
		backoff, err := s.fullJitter(backoffCeiling)
		if err != nil {
			return domain.RunResult{
				Status:         result.Status,
				Err:            fmt.Errorf("scheduler retry jitter failed: %w", err),
				Code:           "SCHEDULER_RETRY_JITTER_UNAVAILABLE",
				Attempts:       attempt,
				RequestID:      result.RequestID,
				IdempotencyKey: result.IdempotencyKey,
			}
		}
		if err := s.sleeper.Wait(ctx, backoff); err != nil {
			return domain.RunResult{Err: err, Code: "SCHEDULER_CANCELLED", Attempts: attempt}
		}
	}
	return result
}

func retryBackoff(policy domain.RetryPolicy, failedAttempt int) time.Duration {
	base := time.Duration(policy.BackoffSeconds) * time.Second
	maximum := time.Duration(policy.MaxBackoffSeconds) * time.Second
	if base >= maximum {
		return maximum
	}
	multiplier := time.Duration(1) << (failedAttempt - 1)
	if base > maximum/multiplier {
		return maximum
	}
	backoff := base * multiplier
	if backoff > maximum {
		return maximum
	}
	return backoff
}

func (s *Scheduler) fullJitter(ceiling time.Duration) (time.Duration, error) {
	if ceiling <= 0 {
		return 0, nil
	}
	value, err := s.random.Int63n(int64(ceiling) + 1)
	if err != nil {
		return 0, err
	}
	if value < 0 || value > int64(ceiling) {
		return 0, errors.New("random source returned a value outside the requested range")
	}
	return time.Duration(value), nil
}

func startLeaseRenewal(ctx context.Context, lease ports.Lease, ttl time.Duration, errors chan<- error, onLost func()) func() {
	renewCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	interval := ttl / 3
	if interval < time.Second {
		interval = time.Second
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				if err := lease.Renew(renewCtx, ttl); err != nil {
					select {
					case errors <- err:
					default:
					}
					onLost()
					cancel()
					return
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func sameJob(left, right domain.Job) bool {
	return left.Name == right.Name && left.Schedule == right.Schedule && left.URL == right.URL && left.Method == right.Method && left.MisfirePolicy == right.MisfirePolicy && left.Paused == right.Paused && left.Retry == right.Retry && bytes.Equal(left.Body, right.Body)
}

func (s *Scheduler) Start() {
	s.engine.Start()
	s.ready.Store(true)
}

func (s *Scheduler) Ready() bool { return s.ready.Load() }

func (s *Scheduler) Stop(ctx context.Context) error {
	s.ready.Store(false)
	s.cancel()
	if err := s.engine.Stop(ctx); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		s.inFlight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
