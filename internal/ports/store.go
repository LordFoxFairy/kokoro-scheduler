package ports

import (
	"context"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

type Store interface {
	Ping(context.Context) error
	WithinTx(context.Context, func(TxStore) error) error
}

type TxStore interface {
	LockCommandIdentity(ctx context.Context, tenantID, scope, idempotencyKey string) error
	FindCommandReceipt(ctx context.Context, tenantID, scope, idempotencyKey string) (domain.CommandReceipt, bool, error)
	InsertCommandReceipt(ctx context.Context, receipt domain.CommandReceipt) error

	CreateSchedule(ctx context.Context, schedule domain.Schedule) (domain.Schedule, error)
	UpdateSchedule(ctx context.Context, schedule domain.Schedule) (domain.Schedule, error)
	DeleteSchedule(ctx context.Context, tenantID, name string) error
	SetScheduleStatus(ctx context.Context, tenantID, name string, status domain.ScheduleStatus, nextDueAt, now time.Time) (domain.Schedule, error)
	GetSchedule(ctx context.Context, tenantID, name string) (domain.Schedule, error)

	ClaimDueSchedules(ctx context.Context, now time.Time, workerID string, claimExpiresAt time.Time, limit int) ([]domain.Schedule, error)
	HasOpenOccurrence(ctx context.Context, tenantID, scheduleID string) (bool, error)
	InsertOccurrence(ctx context.Context, schedule domain.Schedule, plan domain.OccurrencePlan) (occurrenceID string, inserted bool, err error)
	InsertDispatchOutbox(ctx context.Context, schedule domain.Schedule, occurrenceID string, plan domain.OccurrencePlan) error
	AdvanceSchedule(ctx context.Context, schedule domain.Schedule, workerID string, nextDueAt, now time.Time) error
	ReleaseScheduleClaim(ctx context.Context, schedule domain.Schedule, workerID string, now time.Time) error

	RecoverExhaustedDispatches(ctx context.Context, now time.Time) error
	ClaimDispatches(ctx context.Context, now time.Time, workerID string, claimExpiresAt time.Time, limit int) ([]domain.DispatchWork, error)
	BeginDispatch(ctx context.Context, work domain.DispatchWork, workerID string, now time.Time) (domain.DispatchWork, error)
	DeferDispatch(ctx context.Context, work domain.DispatchWork, workerID string, nextAttemptAt time.Time, code, message string, now time.Time) error
	CompleteDispatch(ctx context.Context, work domain.DispatchWork, workerID string, result domain.DispatchResult, now time.Time) error
	RetryDispatch(ctx context.Context, work domain.DispatchWork, workerID string, result domain.DispatchResult, nextAttemptAt, now time.Time) error
	FailDispatch(ctx context.Context, work domain.DispatchWork, workerID string, result domain.DispatchResult, now time.Time) error
}
