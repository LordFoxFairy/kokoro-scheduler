package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
)

const (
	DefaultDispatchTimeout = 30 * time.Second
	coordinationRetryDelay = time.Second
)

type Dispatcher struct {
	store           ports.Store
	clock           ports.Clock
	random          ports.RandomSource
	target          ports.TargetClient
	leaseStore      ports.LeaseStore
	observer        ports.DispatchObserver
	workerID        string
	claimTTL        time.Duration
	dispatchTimeout time.Duration
	batchSize       int
}

type DispatcherDependencies struct {
	Store           ports.Store
	Clock           ports.Clock
	Random          ports.RandomSource
	Target          ports.TargetClient
	LeaseStore      ports.LeaseStore
	Observer        ports.DispatchObserver
	WorkerID        string
	ClaimTTL        time.Duration
	DispatchTimeout time.Duration
	BatchSize       int
}

func NewDispatcher(deps DispatcherDependencies) (*Dispatcher, error) {
	if deps.Store == nil || deps.Clock == nil || deps.Random == nil || deps.Target == nil || deps.WorkerID == "" {
		return nil, ErrInvalidDependencies
	}
	if deps.Observer == nil {
		deps.Observer = ports.NoopDispatchObserver{}
	}
	if deps.ClaimTTL <= 0 {
		deps.ClaimTTL = DefaultClaimTTL
	}
	if deps.DispatchTimeout <= 0 {
		deps.DispatchTimeout = DefaultDispatchTimeout
	}
	if deps.BatchSize <= 0 {
		deps.BatchSize = DefaultBatchSize
	}
	return &Dispatcher{
		store: deps.Store, clock: deps.Clock, random: deps.Random, target: deps.Target,
		leaseStore: deps.LeaseStore, observer: deps.Observer, workerID: deps.WorkerID,
		claimTTL: deps.ClaimTTL, dispatchTimeout: deps.DispatchTimeout, batchSize: deps.BatchSize,
	}, nil
}

func (d *Dispatcher) Run(ctx context.Context) error {
	now := domain.NormalizeInstant(d.clock.Now())
	var workItems []domain.DispatchWork
	if err := d.store.WithinTx(ctx, func(tx ports.TxStore) error {
		if err := tx.RecoverExhaustedDispatches(ctx, now); err != nil {
			return err
		}
		var err error
		workItems, err = tx.ClaimDispatches(ctx, now, d.workerID, now.Add(d.claimTTL), d.batchSize)
		return err
	}); err != nil {
		return fmt.Errorf("claim dispatch outbox: %w", err)
	}
	var runErr error
	for _, work := range workItems {
		if err := d.process(ctx, work); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("dispatch outbox %s: %w", work.ID, err))
		}
	}
	return runErr
}

func (d *Dispatcher) process(ctx context.Context, claimed domain.DispatchWork) error {
	lease, proceed, err := d.acquireLease(ctx, claimed)
	if err != nil || !proceed {
		code := domain.CodeCoordinationContended
		message := "another scheduler instance holds the optional coordination lease"
		if err != nil {
			code = domain.CodeCoordinationUnavailable
			message = boundedError(err)
		}
		now := domain.NormalizeInstant(d.clock.Now())
		return d.store.WithinTx(ctx, func(tx ports.TxStore) error {
			return tx.DeferDispatch(ctx, claimed, d.workerID, now.Add(coordinationRetryDelay), code, message, now)
		})
	}
	if lease != nil {
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = lease.Release(releaseCtx)
			cancel()
		}()
	}

	now := domain.NormalizeInstant(d.clock.Now())
	work := claimed
	if err := d.store.WithinTx(ctx, func(tx ports.TxStore) error {
		var beginErr error
		work, beginErr = tx.BeginDispatch(ctx, claimed, d.workerID, now)
		return beginErr
	}); err != nil {
		return err
	}

	startedAt := d.clock.Now()
	attemptCtx, cancel := context.WithTimeout(ctx, d.dispatchTimeout)
	result := d.target.Dispatch(attemptCtx, work)
	attemptErr := attemptCtx.Err()
	cancel()
	result = normalizeAttemptResult(result, attemptErr)
	result.Duration = d.clock.Now().Sub(startedAt)
	if result.Duration < 0 {
		result.Duration = 0
	}
	d.observer.Observe(work, result)
	return d.persistResult(ctx, work, result)
}

func normalizeAttemptResult(result domain.DispatchResult, attemptErr error) domain.DispatchResult {
	if attemptErr == nil {
		return result
	}
	if result.Err == nil {
		result.Err = attemptErr
	}
	if errors.Is(attemptErr, context.Canceled) {
		result.Code = domain.CodeCancelled
	} else if errors.Is(attemptErr, context.DeadlineExceeded) {
		result.Code = domain.CodeTargetTimeout
	}
	return result
}

func (d *Dispatcher) acquireLease(ctx context.Context, work domain.DispatchWork) (ports.Lease, bool, error) {
	if d.leaseStore == nil {
		return nil, true, nil
	}
	digest := sha256.Sum256([]byte(work.TenantID + "\x00" + work.OccurrenceID))
	key := "kokoro:scheduler:dispatch:" + hex.EncodeToString(digest[:])
	return d.leaseStore.Acquire(ctx, key, d.claimTTL)
}

func (d *Dispatcher) persistResult(ctx context.Context, work domain.DispatchWork, result domain.DispatchResult) error {
	now := domain.NormalizeInstant(d.clock.Now())
	if result.Succeeded() {
		return d.store.WithinTx(ctx, func(tx ports.TxStore) error {
			return tx.CompleteDispatch(ctx, work, d.workerID, result, now)
		})
	}
	if !domain.RetryableDispatch(result) || work.AttemptCount >= work.Retry.MaxAttempts {
		return d.fail(ctx, work, result, now)
	}
	firstAttemptAt := work.FirstAttemptAt
	if firstAttemptAt.IsZero() {
		firstAttemptAt = now
	}
	deadline := firstAttemptAt.Add(time.Duration(work.Retry.MaxRetryWindowSeconds) * time.Second)
	remaining := deadline.Sub(now)
	if remaining <= 0 {
		return d.fail(ctx, work, result, now)
	}
	ceiling := retryBackoff(work.Retry, work.AttemptCount)
	if ceiling > remaining {
		ceiling = remaining
	}
	delay, err := d.fullJitter(ceiling)
	if err != nil {
		result.Err = fmt.Errorf("scheduler retry jitter failed: %w", err)
		result.Code = "SCHEDULER_RETRY_JITTER_UNAVAILABLE"
		return d.fail(ctx, work, result, now)
	}
	nextAttemptAt := now.Add(delay)
	if !nextAttemptAt.Before(deadline) {
		return d.fail(ctx, work, result, now)
	}
	return d.store.WithinTx(ctx, func(tx ports.TxStore) error {
		return tx.RetryDispatch(ctx, work, d.workerID, result, nextAttemptAt, now)
	})
}

func (d *Dispatcher) fail(ctx context.Context, work domain.DispatchWork, result domain.DispatchResult, now time.Time) error {
	return d.store.WithinTx(ctx, func(tx ports.TxStore) error {
		return tx.FailDispatch(ctx, work, d.workerID, result, now)
	})
}

func retryBackoff(policy domain.RetryPolicy, failedAttempt int) time.Duration {
	base := time.Duration(policy.BackoffSeconds) * time.Second
	maximum := time.Duration(policy.MaxBackoffSeconds) * time.Second
	if base >= maximum {
		return maximum
	}
	multiplier := time.Duration(1) << max(failedAttempt-1, 0)
	if base > maximum/multiplier {
		return maximum
	}
	return min(base*multiplier, maximum)
}

func (d *Dispatcher) fullJitter(ceiling time.Duration) (time.Duration, error) {
	if ceiling <= 0 {
		return 0, nil
	}
	value, err := d.random.Int63n(int64(ceiling) + 1)
	if err != nil {
		return 0, err
	}
	if value < 0 || value > int64(ceiling) {
		return 0, errors.New("random source returned a value outside the requested range")
	}
	return time.Duration(value), nil
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 1024 {
		return message[:1024]
	}
	return message
}
