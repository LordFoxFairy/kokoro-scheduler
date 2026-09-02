package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/robfig/cron/v3"
)

type Job struct {
	Name          string         `json:"name"`
	Schedule      string         `json:"schedule"`
	URL           string         `json:"url"`
	Method        string         `json:"method"`
	Body          map[string]any `json:"body"`
	Retry         RetryPolicy    `json:"retry"`
	MisfirePolicy string         `json:"misfire_policy"`
	Paused        bool           `json:"paused"`
}

type RetryPolicy struct {
	MaxAttempts    int `json:"max_attempts"`
	BackoffSeconds int `json:"backoff_seconds"`
}

const (
	MisfireSkip     = "skip"
	MisfireFireOnce = "fire_once"
)

var jobNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

var (
	ErrJobAlreadyExists = errors.New("scheduler job already exists")
	ErrJobNotFound      = errors.New("scheduler job not found")
)

func LoadJobs(raw string) ([]Job, error) {
	if strings.TrimSpace(raw) == "" {
		return []Job{}, nil
	}
	var jobs []Job
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&jobs); err != nil {
		return nil, fmt.Errorf("decode scheduler jobs: %w", err)
	}
	if jobs == nil {
		return nil, errors.New("decode scheduler jobs: expected a JSON array")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode scheduler jobs: trailing JSON")
		}
		return nil, fmt.Errorf("decode scheduler jobs: %w", err)
	}
	seenNames := make(map[string]struct{}, len(jobs))
	for i := range jobs {
		if err := normalizeJob(&jobs[i]); err != nil {
			return nil, fmt.Errorf("job %d: %w", i, err)
		}
		if _, exists := seenNames[jobs[i].Name]; exists {
			return nil, fmt.Errorf("duplicate job name %q", jobs[i].Name)
		}
		seenNames[jobs[i].Name] = struct{}{}
	}
	return jobs, nil
}

func normalizeJob(job *Job) error {
	if job.Name == "" || job.Schedule == "" || job.URL == "" {
		return errors.New("requires name, schedule and url")
	}
	if !jobNamePattern.MatchString(job.Name) {
		return fmt.Errorf("%q has invalid name", job.Name)
	}
	if _, err := cron.ParseStandard(job.Schedule); err != nil {
		return fmt.Errorf("%q has invalid schedule: %w", job.Name, err)
	}
	parsedURL, err := url.ParseRequestURI(job.URL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return fmt.Errorf("%q has invalid url", job.Name)
	}
	if job.Method == "" {
		job.Method = http.MethodPost
	}
	if job.Method != http.MethodPost && job.Method != http.MethodPut {
		return fmt.Errorf("%q has unsupported method %q", job.Name, job.Method)
	}
	if job.Body == nil {
		job.Body = map[string]any{}
	}
	if job.Retry.MaxAttempts == 0 {
		job.Retry.MaxAttempts = 1
	}
	if job.Retry.MaxAttempts < 1 || job.Retry.MaxAttempts > 10 {
		return fmt.Errorf("%q has invalid retry.max_attempts", job.Name)
	}
	if job.Retry.BackoffSeconds < 0 || job.Retry.BackoffSeconds > 3600 {
		return fmt.Errorf("%q has invalid retry.backoff_seconds", job.Name)
	}
	if job.MisfirePolicy == "" {
		job.MisfirePolicy = MisfireSkip
	}
	if job.MisfirePolicy != MisfireSkip && job.MisfirePolicy != MisfireFireOnce {
		return fmt.Errorf("%q has invalid misfire_policy %q", job.Name, job.MisfirePolicy)
	}
	return nil
}

type RunResult struct {
	Status         int
	Err            error
	Code           string
	Attempts       int
	RequestID      string
	IdempotencyKey string
}

type HTTPRunner struct{ client *http.Client }

func NewHTTPRunner(timeout time.Duration) *HTTPRunner {
	return &HTTPRunner{client: &http.Client{Timeout: timeout}}
}
func (r *HTTPRunner) Run(ctx context.Context, job Job) RunResult {
	return r.RunAt(ctx, job, time.Now().UTC())
}

func (r *HTTPRunner) RunAt(ctx context.Context, job Job, at time.Time) RunResult {
	requestID, idempotencyKey := RequestIdentity(job, at)
	body, err := json.Marshal(job.Body)
	if err != nil {
		return RunResult{Err: err, Code: "SCHEDULER_TARGET_UNAVAILABLE", RequestID: requestID, IdempotencyKey: idempotencyKey}
	}
	request, err := http.NewRequestWithContext(ctx, job.Method, job.URL, bytes.NewReader(body))
	if err != nil {
		return RunResult{Err: err, Code: "SCHEDULER_TARGET_UNAVAILABLE", RequestID: requestID, IdempotencyKey: idempotencyKey}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Kokoro-Scheduler-Job", job.Name)
	request.Header.Set("X-Request-Id", requestID)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response, err := r.client.Do(request)
	if err != nil {
		code := "SCHEDULER_TARGET_UNAVAILABLE"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "SCHEDULER_TARGET_TIMEOUT"
		}
		return RunResult{Err: err, Code: code, RequestID: requestID, IdempotencyKey: idempotencyKey}
	}
	defer response.Body.Close()
	result := RunResult{Status: response.StatusCode, RequestID: requestID, IdempotencyKey: idempotencyKey}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.Code = "SCHEDULER_TARGET_REJECTED"
		result.Err = fmt.Errorf("job returned HTTP %d", response.StatusCode)
	}
	return result
}

type Runner interface {
	Run(context.Context, Job) RunResult
}

// Locker is optional. A nil locker is the supported single-replica mode.
// In multi-replica mode it provides a per-occurrence distributed claim.
type Locker interface {
	Acquire(context.Context, string, time.Duration) (release func(), acquired bool, err error)
}

type RedisLocker struct{ client *redis.Client }

func NewRedisLocker(client *redis.Client) *RedisLocker { return &RedisLocker{client: client} }

func (l *RedisLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (func(), bool, error) {
	claimCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	token := fmt.Sprintf("%d", time.Now().UnixNano())
	ok, err := l.client.SetNX(claimCtx, key, token, ttl).Result()
	if err != nil || !ok {
		return func() {}, ok, err
	}
	release := func() {
		_, _ = l.client.Eval(context.Background(), "if redis.call('get', KEYS[1]) == ARGV[1] then return redis.call('del', KEYS[1]) else return 0 end", []string{key}, token).Result()
	}
	return release, true, nil
}

func OccurrenceKey(job Job, now time.Time) string {
	now = now.UTC()
	occurrence := now.Format("200601021504")
	if strings.HasPrefix(job.Schedule, "@every ") {
		if duration, err := time.ParseDuration(strings.TrimPrefix(job.Schedule, "@every ")); err == nil && duration > 0 {
			occurrence = fmt.Sprintf("%d", now.UnixNano()/duration.Nanoseconds())
		}
	}
	return fmt.Sprintf("kokoro:scheduler:run:%s:%s", job.Name, occurrence)
}

// RequestIdentity returns the retry-safe request metadata for one scheduler
// dispatch. Callers use the same occurrence timestamp when they need to
// replay a delivery without creating a second business operation.
func RequestIdentity(job Job, at time.Time) (requestID string, idempotencyKey string) {
	at = at.UTC()
	occurrence := at.Format("20060102T150405Z")
	return fmt.Sprintf("sched_%s_%s", job.Name, occurrence), fmt.Sprintf("schedule:%s:%s", job.Name, occurrence)
}

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
		release, acquired, err := s.locker.Acquire(ctx, OccurrenceKey(job, now), 26*time.Hour)
		if err != nil || !acquired {
			if err != nil {
				requestID, idempotencyKey := RequestIdentity(job, now)
				s.logger(job, RunResult{Err: fmt.Errorf("scheduler coordination failed: %w", err), Code: "SCHEDULER_COORDINATION_UNAVAILABLE", RequestID: requestID, IdempotencyKey: idempotencyKey})
			}
			return
		}
		defer release()
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
