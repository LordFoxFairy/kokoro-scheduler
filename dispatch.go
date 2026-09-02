package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type RunResult struct {
	Status         int
	Err            error
	Code           string
	Attempts       int
	RequestID      string
	IdempotencyKey string
}

const occurrenceHeader = "X-Kokoro-Scheduler-Occurrence"

type HTTPRunner struct {
	client             *http.Client
	targetServiceToken string
}

func NewHTTPRunner(timeout time.Duration, targetServiceTokens ...string) *HTTPRunner {
	targetServiceToken := ""
	if len(targetServiceTokens) > 0 {
		targetServiceToken = strings.TrimSpace(targetServiceTokens[0])
	}
	return &HTTPRunner{
		client:             &http.Client{Timeout: timeout},
		targetServiceToken: targetServiceToken,
	}
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
	request.Header.Set(occurrenceHeader, OccurrenceIdentity(at))
	request.Header.Set("X-Request-Id", requestID)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	if r.targetServiceToken != "" {
		request.Header.Set("Authorization", "Bearer "+r.targetServiceToken)
	}
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
// In multi-replica mode it provides a per-occurrence distributed claim. The
// release function is available to callers that need an explicit early
// release; Service retains its occurrence claim until the lease TTL expires.
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
	occurrence := OccurrenceIdentity(at)
	return fmt.Sprintf("sched_%s_%s", job.Name, occurrence), fmt.Sprintf("schedule:%s:%s", job.Name, occurrence)
}

// OccurrenceIdentity returns the canonical UTC timestamp used to identify one
// scheduled occurrence in outbound dispatch metadata.
func OccurrenceIdentity(at time.Time) string {
	return at.UTC().Format("20060102T150405Z")
}
