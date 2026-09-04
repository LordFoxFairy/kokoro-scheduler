package ports

import (
	"context"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

type RandomSource interface {
	Int63n(maxExclusive int64) (int64, error)
}

type Recurrence interface {
	Validate(rule, timezone string) error
	Next(rule, timezone string, after time.Time) (time.Time, error)
	NextAfter(rule, timezone string, anchor, threshold time.Time) (time.Time, error)
}

type Wakeup interface {
	Start(run func(context.Context)) error
	Stop(context.Context) error
}

type LeaseStore interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (Lease, bool, error)
}

type Lease interface {
	Renew(ctx context.Context, ttl time.Duration) error
	Release(ctx context.Context) error
}

type TargetClient interface {
	Dispatch(ctx context.Context, work domain.DispatchWork) domain.DispatchResult
}

type DispatchObserver interface {
	Observe(work domain.DispatchWork, result domain.DispatchResult)
}

type DispatchObserverFunc func(domain.DispatchWork, domain.DispatchResult)

func (f DispatchObserverFunc) Observe(work domain.DispatchWork, result domain.DispatchResult) {
	f(work, result)
}

type NoopDispatchObserver struct{}

func (NoopDispatchObserver) Observe(domain.DispatchWork, domain.DispatchResult) {}

type ErrorObserver interface {
	ObserveError(operation string, err error)
}

type ErrorObserverFunc func(string, error)

func (f ErrorObserverFunc) ObserveError(operation string, err error) { f(operation, err) }

type NoopErrorObserver struct{}

func (NoopErrorObserver) ObserveError(string, error) {}
