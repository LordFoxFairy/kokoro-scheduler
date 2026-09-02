package scheduler

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

type Service struct {
	cron   *cron.Cron
	runner Runner
	logger func(Job, RunResult)
	locker Locker
	mu     sync.RWMutex
	jobs   map[string]Job
	entry  map[string]cron.EntryID
	paused map[string]bool
}

func NewService(runner Runner, logger func(Job, RunResult), lockers ...Locker) *Service {
	var locker Locker
	if len(lockers) > 0 {
		locker = lockers[0]
	}
	if logger == nil {
		logger = func(Job, RunResult) {}
	}
	return &Service{
		cron:   cron.New(cron.WithLocation(time.UTC), cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger))),
		runner: runner,
		logger: logger,
		locker: locker,
		jobs:   make(map[string]Job),
		entry:  make(map[string]cron.EntryID),
		paused: make(map[string]bool),
	}
}
func (s *Service) Add(job Job) error {
	if err := normalizeJob(&job); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.Name]; exists {
		return ErrJobAlreadyExists
	}
	entryID, err := s.cron.AddFunc(job.Schedule, func() {
		now := time.Now().UTC()
		s.Trigger(job, now, now)
	})
	if err != nil {
		return err
	}
	s.jobs[job.Name] = job
	s.entry[job.Name] = entryID
	s.paused[job.Name] = job.Paused
	return nil
}

func (s *Service) Register(job Job) error { return s.Add(job) }

func (s *Service) Update(job Job) error {
	if err := normalizeJob(&job); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oldEntry, exists := s.entry[job.Name]
	if !exists {
		return ErrJobNotFound
	}
	newEntry, err := s.cron.AddFunc(job.Schedule, func() {
		now := time.Now().UTC()
		s.Trigger(job, now, now)
	})
	if err != nil {
		return err
	}
	s.cron.Remove(oldEntry)
	s.jobs[job.Name] = job
	s.entry[job.Name] = newEntry
	s.paused[job.Name] = job.Paused
	return nil
}

func (s *Service) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entryID, exists := s.entry[name]
	if !exists {
		return ErrJobNotFound
	}
	s.cron.Remove(entryID)
	delete(s.jobs, name)
	delete(s.entry, name)
	delete(s.paused, name)
	return nil
}

func (s *Service) Delete(name string) error { return s.Remove(name) }

func (s *Service) Job(name string) (Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, exists := s.jobs[name]
	return job, exists
}

// Trigger dispatches one occurrence. The explicit scheduledAt/observedAt
// pair is also the seam used by a future durable registry to recover a missed
// occurrence after restart.
func (s *Service) Trigger(job Job, scheduledAt, observedAt time.Time) {
	if observedAt.After(scheduledAt) && job.MisfirePolicy == MisfireSkip {
		requestID, idempotencyKey := RequestIdentity(job, scheduledAt)
		s.logger(job, RunResult{Code: "SCHEDULER_MISFIRED", RequestID: requestID, IdempotencyKey: idempotencyKey})
		return
	}
	s.run(job, observedAt)
}

func (s *Service) run(job Job, now time.Time) {
	s.mu.RLock()
	paused := s.paused[job.Name]
	s.mu.RUnlock()
	if paused {
		return
	}
	ctx := context.Background()
	if s.locker != nil {
		// Keep the claim until its TTL expires. Releasing it after a successful
		// dispatch would let another replica execute the same occurrence again.
		_, acquired, err := s.locker.Acquire(ctx, OccurrenceKey(job, now), 26*time.Hour)
		if err != nil || !acquired {
			if err != nil {
				requestID, idempotencyKey := RequestIdentity(job, now)
				s.logger(job, RunResult{Err: fmt.Errorf("scheduler coordination failed: %w", err), Code: "SCHEDULER_COORDINATION_UNAVAILABLE", RequestID: requestID, IdempotencyKey: idempotencyKey})
			}
			return
		}
	}
	result := RunResult{}
	maxAttempts := job.Retry.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 1
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if runner, ok := s.runner.(interface {
			RunAt(context.Context, Job, time.Time) RunResult
		}); ok {
			result = runner.RunAt(ctx, job, now)
		} else {
			result = s.runner.Run(ctx, job)
		}
		result.Attempts = attempt
		if result.RequestID == "" || result.IdempotencyKey == "" {
			result.RequestID, result.IdempotencyKey = RequestIdentity(job, now)
		}
		if result.Err == nil || !retryable(result) || attempt == maxAttempts {
			break
		}
		backoff := time.Duration(job.Retry.BackoffSeconds) * time.Second
		if backoff > 0 {
			select {
			case <-time.After(backoff * time.Duration(1<<(attempt-1))):
			case <-ctx.Done():
				result.Err = ctx.Err()
				break
			}
		}
	}
	s.logger(job, result)
}

func retryable(result RunResult) bool {
	return result.Status == 0 || result.Status == http.StatusTooManyRequests || result.Status >= 500
}

func (s *Service) Pause(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[name]; !exists {
		return ErrJobNotFound
	}
	s.paused[name] = true
	job := s.jobs[name]
	job.Paused = true
	s.jobs[name] = job
	return nil
}

func (s *Service) Resume(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[name]; !exists {
		return ErrJobNotFound
	}
	s.paused[name] = false
	job := s.jobs[name]
	job.Paused = false
	s.jobs[name] = job
	return nil
}
func (s *Service) Start()                                   { s.cron.Start() }
func (s *Service) Stop(ctx context.Context) context.Context { return s.cron.Stop() }
