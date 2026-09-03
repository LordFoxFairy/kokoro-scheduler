package ports

import (
	"context"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

// Clock is injected so scheduling and retry decisions are deterministic in tests.
type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// Sleeper makes retry delays context-aware and replaceable in deterministic tests.
type Sleeper interface {
	Wait(ctx context.Context, duration time.Duration) error
}

// RandomSource supplies an unbiased value in [0, maxExclusive). Production
// adapters must not use a shared deterministic seed across scheduler instances.
type RandomSource interface {
	Int63n(maxExclusive int64) (int64, error)
}

// ScheduleEngine owns only timer registration. It never owns scheduler jobs.
type ScheduleEngine interface {
	Add(spec string, run func()) (EntryID, error)
	Remove(id EntryID)
	Start()
	Stop(ctx context.Context) error
}

type EntryID int64

// LeaseStore is the cross-instance occurrence claim port. Successful
// occurrences retain their claim for the configured TTL; failed occurrences
// release their claim so a later retry or recovery loop can make progress.
type LeaseStore interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (Lease, bool, error)
}

type Lease interface {
	Renew(ctx context.Context, ttl time.Duration) error
	Release(ctx context.Context) error
}

// TargetClient dispatches a generic command to the owner service. It does not
// know Billing, Agent, or any other business model.
type TargetClient interface {
	Dispatch(ctx context.Context, job domain.Job, occurrence domain.Occurrence) domain.RunResult
}

type RunObserver interface {
	Observe(job domain.Job, result domain.RunResult)
}

type RunObserverFunc func(domain.Job, domain.RunResult)

func (f RunObserverFunc) Observe(job domain.Job, result domain.RunResult) { f(job, result) }

type NoopObserver struct{}

func (NoopObserver) Observe(domain.Job, domain.RunResult) {}
