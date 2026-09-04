package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/ports"
)

const (
	DefaultClaimTTL  = 2 * time.Minute
	DefaultBatchSize = 100
)

type Planner struct {
	store      ports.Store
	clock      ports.Clock
	recurrence ports.Recurrence
	workerID   string
	claimTTL   time.Duration
	batchSize  int
}

func NewPlanner(store ports.Store, clock ports.Clock, recurrence ports.Recurrence, workerID string, claimTTL time.Duration, batchSize int) (*Planner, error) {
	if store == nil || clock == nil || recurrence == nil || workerID == "" {
		return nil, ErrInvalidDependencies
	}
	if claimTTL <= 0 {
		claimTTL = DefaultClaimTTL
	}
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	return &Planner{store: store, clock: clock, recurrence: recurrence, workerID: workerID, claimTTL: claimTTL, batchSize: batchSize}, nil
}

func (p *Planner) Run(ctx context.Context) error {
	now := domain.NormalizeInstant(p.clock.Now())
	var schedules []domain.Schedule
	if err := p.store.WithinTx(ctx, func(tx ports.TxStore) error {
		var err error
		schedules, err = tx.ClaimDueSchedules(ctx, now, p.workerID, now.Add(p.claimTTL), p.batchSize)
		return err
	}); err != nil {
		return fmt.Errorf("claim due schedules: %w", err)
	}
	var runErr error
	for _, schedule := range schedules {
		if err := p.materialize(ctx, schedule, now); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("materialize schedule %s/%s: %w", schedule.TenantID, schedule.Name, err))
		}
	}
	return runErr
}

func (p *Planner) materialize(ctx context.Context, schedule domain.Schedule, now time.Time) error {
	plans, nextDueAt, err := planSchedule(schedule, now, p.recurrence)
	if err != nil {
		releaseErr := p.store.WithinTx(ctx, func(tx ports.TxStore) error {
			return tx.ReleaseScheduleClaim(ctx, schedule, p.workerID, now)
		})
		return errors.Join(err, releaseErr)
	}
	return p.store.WithinTx(ctx, func(tx ports.TxStore) error {
		openOccurrence := false
		if schedule.OverlapPolicy == domain.OverlapForbid {
			var err error
			openOccurrence, err = tx.HasOpenOccurrence(ctx, schedule.TenantID, schedule.ID)
			if err != nil {
				return err
			}
		}
		for _, sourcePlan := range plans {
			plan := sourcePlan
			if plan.Dispatch && openOccurrence {
				plan.Dispatch = false
				plan.Status = domain.OccurrenceSkipped
				plan.OutcomeCode = domain.CodeOverlapBlocked
			}
			occurrenceID, inserted, err := tx.InsertOccurrence(ctx, schedule, plan)
			if err != nil {
				return err
			}
			if !inserted || !plan.Dispatch {
				continue
			}
			if err := tx.InsertDispatchOutbox(ctx, schedule, occurrenceID, plan); err != nil {
				return err
			}
			if schedule.OverlapPolicy == domain.OverlapForbid {
				openOccurrence = true
			}
		}
		return tx.AdvanceSchedule(ctx, schedule, p.workerID, nextDueAt, now)
	})
}

func planSchedule(schedule domain.Schedule, now time.Time, recurrence ports.Recurrence) ([]domain.OccurrencePlan, time.Time, error) {
	now = domain.NormalizeInstant(now)
	due := domain.NormalizeInstant(schedule.NextDueAt)
	if due.IsZero() || due.After(now) {
		return nil, due, nil
	}
	if due.Equal(now) {
		next, err := recurrence.Next(schedule.Rule, schedule.Timezone, due)
		return []domain.OccurrencePlan{dispatchPlan(due, now)}, next, err
	}

	switch schedule.MisfirePolicy {
	case domain.MisfireSkip:
		next, err := recurrence.NextAfter(schedule.Rule, schedule.Timezone, due, now)
		return []domain.OccurrencePlan{{
			ScheduledAt: due, ObservedAt: now, Status: domain.OccurrenceSkipped,
			OutcomeCode: domain.CodeMisfireSkipped, MissedCount: 1,
		}}, next, err
	case domain.MisfireFireOnce:
		next, err := recurrence.NextAfter(schedule.Rule, schedule.Timezone, due, now)
		return []domain.OccurrencePlan{dispatchPlan(due, now)}, next, err
	case domain.MisfireCatchUpBounded:
		return boundedCatchUp(schedule, due, now, recurrence)
	default:
		return nil, time.Time{}, fmt.Errorf("unsupported misfire policy %q", schedule.MisfirePolicy)
	}
}

func boundedCatchUp(schedule domain.Schedule, due, now time.Time, recurrence ports.Recurrence) ([]domain.OccurrencePlan, time.Time, error) {
	plans := make([]domain.OccurrencePlan, 0, schedule.CatchUpLimit+1)
	candidate := due
	for len(plans) < schedule.CatchUpLimit && !candidate.After(now) {
		plans = append(plans, dispatchPlan(candidate, now))
		next, err := recurrence.Next(schedule.Rule, schedule.Timezone, candidate)
		if err != nil {
			return nil, time.Time{}, err
		}
		candidate = next
	}
	if !candidate.After(now) {
		plans = append(plans, domain.OccurrencePlan{
			ScheduledAt: candidate, ObservedAt: now, Status: domain.OccurrenceSkipped,
			OutcomeCode: domain.CodeMisfireBoundExceeded, MissedCount: 1,
		})
		next, err := recurrence.NextAfter(schedule.Rule, schedule.Timezone, candidate, now)
		return plans, next, err
	}
	return plans, candidate, nil
}

func dispatchPlan(scheduledAt, observedAt time.Time) domain.OccurrencePlan {
	return domain.OccurrencePlan{
		ScheduledAt: domain.NormalizeInstant(scheduledAt),
		ObservedAt:  domain.NormalizeInstant(observedAt),
		Status:      domain.OccurrencePending,
		MissedCount: 1,
		Dispatch:    true,
	}
}
