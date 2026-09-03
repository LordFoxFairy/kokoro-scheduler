package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type Occurrence struct {
	JobName     string
	ScheduledAt time.Time
	ObservedAt  time.Time
}

func NewOccurrence(jobName string, scheduledAt, observedAt time.Time) Occurrence {
	return Occurrence{JobName: jobName, ScheduledAt: scheduledAt.UTC(), ObservedAt: observedAt.UTC()}
}

func (o Occurrence) Identity() string { return o.ScheduledAt.UTC().Format("20060102T150405Z") }

func OccurrenceKey(job Job, at time.Time) string {
	at = at.UTC()
	occurrence := at.Format("200601021504")
	if strings.HasPrefix(job.Schedule, "@every ") {
		if duration, err := time.ParseDuration(strings.TrimPrefix(job.Schedule, "@every ")); err == nil && duration > 0 {
			occurrence = fmt.Sprintf("%d", at.UnixNano()/duration.Nanoseconds())
		}
	}
	return fmt.Sprintf("kokoro:scheduler:run:%s:%s", job.Name, occurrence)
}

func RequestIdentity(job Job, at time.Time) (requestID, idempotencyKey string) {
	occurrence := at.UTC().Format("20060102T150405Z")
	return fmt.Sprintf("sched_%s_%s", job.Name, occurrence), fmt.Sprintf("schedule:%s:%s", job.Name, occurrence)
}

// TraceIdentity returns a stable W3C trace identifier for every
// retry of the same occurrence. The downstream client creates an attempt span
// identifier separately.
func TraceIdentity(job Job, at time.Time) string {
	_, idempotencyKey := RequestIdentity(job, at)
	digest := sha256.Sum256([]byte(idempotencyKey))
	return hex.EncodeToString(digest[:16])
}

type RunResult struct {
	Status         int
	Err            error
	Code           string
	Attempts       int
	RequestID      string
	IdempotencyKey string
	TraceID        string
	Duration       time.Duration
}

func (r RunResult) Succeeded() bool {
	return r.Err == nil && r.Status >= 200 && r.Status < 300
}

func Retryable(result RunResult) bool {
	return result.Status == 0 || result.Status == 429 || result.Status >= 500
}
