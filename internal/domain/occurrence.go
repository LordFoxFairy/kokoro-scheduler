package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const (
	OccurrencePending     = "pending"
	OccurrenceDispatching = "dispatching"
	OccurrenceRetrying    = "retrying"
	OccurrenceSucceeded   = "succeeded"
	OccurrenceFailed      = "failed"
	OccurrenceSkipped     = "skipped"

	OutboxPending     = "pending"
	OutboxDispatching = "dispatching"
	OutboxRetrying    = "retrying"
	OutboxSucceeded   = "succeeded"
	OutboxFailed      = "failed"

	CodeTargetUnavailable         = "SCHEDULER_TARGET_UNAVAILABLE"
	CodeTargetTimeout             = "SCHEDULER_TARGET_TIMEOUT"
	CodeTargetRejected            = "SCHEDULER_TARGET_REJECTED"
	CodeTargetPermanent           = "SCHEDULER_TARGET_PERMANENT"
	CodeCancelled                 = "SCHEDULER_CANCELLED"
	CodeCoordinationUnavailable   = "SCHEDULER_COORDINATION_UNAVAILABLE"
	CodeCoordinationContended     = "SCHEDULER_COORDINATION_CONTENDED"
	CodeDispatchRecoveryExhausted = "SCHEDULER_DISPATCH_RECOVERY_EXHAUSTED"
	CodeMisfireSkipped            = "SCHEDULER_MISFIRE_SKIPPED"
	CodeMisfireBoundExceeded      = "SCHEDULER_MISFIRE_BOUND_EXCEEDED"
	CodeOverlapBlocked            = "SCHEDULER_OVERLAP_BLOCKED"
)

type OccurrencePlan struct {
	ScheduledAt time.Time
	ObservedAt  time.Time
	Status      string
	OutcomeCode string
	MissedCount int
	Dispatch    bool
}

type OccurrenceIdentityValue struct {
	ScheduledAt    time.Time
	RequestID      string
	IdempotencyKey string
	TraceID        string
}

func OccurrenceIdentity(schedule Schedule, scheduledAt time.Time) OccurrenceIdentityValue {
	scheduledAt = NormalizeInstant(scheduledAt)
	seed := fmt.Sprintf("%s\x00%s\x00%s\x00%s", schedule.TenantID, schedule.ID, schedule.Name, scheduledAt.Format(time.RFC3339Nano))
	digest := sha256.Sum256([]byte(seed))
	hexDigest := hex.EncodeToString(digest[:])
	return OccurrenceIdentityValue{
		ScheduledAt:    scheduledAt,
		RequestID:      "sched_" + hexDigest[:24],
		IdempotencyKey: "schedule:" + hexDigest,
		TraceID:        hexDigest[:32],
	}
}

func NormalizeInstant(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Truncate(time.Millisecond)
}

type DispatchWork struct {
	ID             string
	TenantID       string
	OccurrenceID   string
	ScheduleID     string
	ScheduleName   string
	ScheduledAt    time.Time
	TargetURL      string
	Method         Method
	Payload        json.RawMessage
	Status         string
	AttemptCount   int
	Retry          RetryPolicy
	NextAttemptAt  time.Time
	FirstAttemptAt time.Time
	CreatedAt      time.Time
}

func (w DispatchWork) ScheduleSnapshot() Schedule {
	return Schedule{
		ID:        w.ScheduleID,
		TenantID:  w.TenantID,
		Name:      w.ScheduleName,
		TargetURL: w.TargetURL,
		Method:    w.Method,
		Payload:   append(json.RawMessage(nil), w.Payload...),
		Retry:     w.Retry,
	}
}

type DispatchResult struct {
	Status         int
	Err            error
	Code           string
	RequestID      string
	IdempotencyKey string
	TraceID        string
	Duration       time.Duration
}

func (r DispatchResult) Succeeded() bool {
	return r.Err == nil && r.Status >= 200 && r.Status < 300
}

func RetryableDispatch(result DispatchResult) bool {
	if result.Code == CodeTargetRejected || result.Code == CodeTargetPermanent || result.Code == CodeCancelled {
		return false
	}
	if result.Status == 408 || result.Status == 425 || result.Status == 429 || result.Status >= 500 {
		return true
	}
	if result.Err != nil && (result.Code == CodeTargetUnavailable || result.Code == CodeTargetTimeout) {
		return true
	}
	return result.Status == 0 && result.Err != nil && result.Code == ""
}
