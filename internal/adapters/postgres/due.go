package postgresadapter

import (
	"context"
	"errors"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *txStore) ClaimDueSchedules(ctx context.Context, now time.Time, workerID string, claimExpiresAt time.Time, limit int) ([]domain.Schedule, error) {
	query := `
        WITH candidates AS (
            SELECT id
              FROM scheduler_schedule
             WHERE status = 'active'
               AND next_due_at <= $1
               AND (claim_expires_at IS NULL OR claim_expires_at <= $1)
             ORDER BY next_due_at, id
             FOR UPDATE SKIP LOCKED
             LIMIT $2
        )
        UPDATE scheduler_schedule
           SET claim_owner = $3, claim_expires_at = $4, updated_at = $1
         WHERE id IN (SELECT id FROM candidates)
         RETURNING ` + scheduleColumns
	rows, err := s.tx.Query(ctx, query, now, limit, workerID, claimExpiresAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	schedules := make([]domain.Schedule, 0, limit)
	for rows.Next() {
		schedule, scanErr := scanSchedule(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

func (s *txStore) HasOpenOccurrence(ctx context.Context, tenantID, scheduleID string) (bool, error) {
	var exists bool
	err := s.tx.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1
              FROM scheduler_occurrence
             WHERE tenant_id = $1 AND schedule_id = $2::uuid
               AND status IN ('pending', 'dispatching', 'retrying')
        )`, tenantID, scheduleID).Scan(&exists)
	return exists, err
}

func (s *txStore) InsertOccurrence(ctx context.Context, schedule domain.Schedule, plan domain.OccurrencePlan) (string, bool, error) {
	var completedAt any
	if plan.Status == domain.OccurrenceSucceeded || plan.Status == domain.OccurrenceFailed || plan.Status == domain.OccurrenceSkipped {
		completedAt = plan.ObservedAt
	}
	var occurrenceID string
	err := s.tx.QueryRow(ctx, `
        INSERT INTO scheduler_occurrence (
            tenant_id, schedule_id, schedule_name, scheduled_at, observed_at,
            status, outcome_code, missed_count, completed_at, created_at, updated_at
        ) VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $5, $5)
        ON CONFLICT (tenant_id, schedule_id, scheduled_at) DO NOTHING
        RETURNING id::text`,
		schedule.TenantID, schedule.ID, schedule.Name, plan.ScheduledAt, plan.ObservedAt,
		plan.Status, nullableString(plan.OutcomeCode), plan.MissedCount, completedAt,
	).Scan(&occurrenceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return occurrenceID, err == nil, err
}

func (s *txStore) InsertDispatchOutbox(ctx context.Context, schedule domain.Schedule, occurrenceID string, plan domain.OccurrencePlan) error {
	_, err := s.tx.Exec(ctx, `
        INSERT INTO scheduler_dispatch_outbox (
            tenant_id, occurrence_id, schedule_id, schedule_name, scheduled_at,
            target_url, target_method, payload, status, max_attempts,
            retry_backoff_seconds, retry_max_backoff_seconds, retry_window_seconds,
            next_attempt_at, created_at, updated_at
        ) VALUES (
            $1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, 'pending', $9,
            $10, $11, $12, $13, $13, $13
        ) ON CONFLICT (tenant_id, occurrence_id) DO NOTHING`,
		schedule.TenantID, occurrenceID, schedule.ID, schedule.Name, plan.ScheduledAt,
		schedule.TargetURL, schedule.Method, schedule.Payload, schedule.Retry.MaxAttempts,
		schedule.Retry.BackoffSeconds, schedule.Retry.MaxBackoffSeconds,
		schedule.Retry.MaxRetryWindowSeconds, plan.ObservedAt,
	)
	return err
}

func (s *txStore) AdvanceSchedule(ctx context.Context, schedule domain.Schedule, workerID string, nextDueAt, now time.Time) error {
	result, err := s.tx.Exec(ctx, `
        UPDATE scheduler_schedule
           SET next_due_at = $4, claim_owner = NULL, claim_expires_at = NULL,
               version = version + 1, updated_at = $5
         WHERE tenant_id = $1 AND id = $2::uuid AND version = $3 AND claim_owner = $6`,
		schedule.TenantID, schedule.ID, schedule.Version, nextDueAt, now, workerID,
	)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return domain.ErrClaimLost
	}
	return nil
}

func (s *txStore) ReleaseScheduleClaim(ctx context.Context, schedule domain.Schedule, workerID string, now time.Time) error {
	result, err := s.tx.Exec(ctx, `
        UPDATE scheduler_schedule
           SET claim_owner = NULL, claim_expires_at = NULL, updated_at = $4
         WHERE tenant_id = $1 AND id = $2::uuid AND claim_owner = $3`,
		schedule.TenantID, schedule.ID, workerID, now,
	)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return domain.ErrClaimLost
	}
	return nil
}
