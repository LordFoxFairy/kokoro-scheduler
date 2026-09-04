package postgresadapter

import (
	"context"
	"errors"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *txStore) RecoverExhaustedDispatches(ctx context.Context, now time.Time) error {
	_, err := s.tx.Exec(ctx, `
		WITH exhausted AS (
			UPDATE scheduler_dispatch_outbox
			   SET status = 'failed', next_attempt_at = $1,
			       claim_owner = NULL, claim_expires_at = NULL,
			       last_error_code = $2,
			       last_error_message = 'worker claim expired after final dispatch attempt',
			       completed_at = $1, updated_at = $1
			 WHERE status = 'dispatching'
			   AND claim_expires_at <= $1
			   AND attempt_count >= max_attempts
			 RETURNING tenant_id, occurrence_id, attempt_count
		)
		UPDATE scheduler_occurrence AS occurrence
		   SET status = 'failed', outcome_code = $2,
		       attempt_count = exhausted.attempt_count,
		       last_error_code = $2,
		       last_error_message = 'worker claim expired after final dispatch attempt',
		       completed_at = $1, updated_at = $1
		  FROM exhausted
		 WHERE occurrence.tenant_id = exhausted.tenant_id
		   AND occurrence.id = exhausted.occurrence_id`,
		now, domain.CodeDispatchRecoveryExhausted,
	)
	return err
}

func (s *txStore) ClaimDispatches(ctx context.Context, now time.Time, workerID string, claimExpiresAt time.Time, limit int) ([]domain.DispatchWork, error) {
	query := `
        WITH candidates AS (
            SELECT id
              FROM scheduler_dispatch_outbox
             WHERE attempt_count < max_attempts
               AND (
                    (status IN ('pending', 'retrying') AND next_attempt_at <= $1)
                    OR (status = 'dispatching' AND claim_expires_at <= $1)
               )
             ORDER BY next_attempt_at, id
             FOR UPDATE SKIP LOCKED
             LIMIT $2
        )
        UPDATE scheduler_dispatch_outbox
           SET status = 'dispatching', claim_owner = $3, claim_expires_at = $4,
               updated_at = $1
         WHERE id IN (SELECT id FROM candidates)
         RETURNING ` + dispatchColumns
	rows, err := s.tx.Query(ctx, query, now, limit, workerID, claimExpiresAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	workItems := make([]domain.DispatchWork, 0, limit)
	for rows.Next() {
		work, scanErr := scanDispatch(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		workItems = append(workItems, work)
	}
	return workItems, rows.Err()
}

func (s *txStore) BeginDispatch(ctx context.Context, work domain.DispatchWork, workerID string, now time.Time) (domain.DispatchWork, error) {
	query := `
        UPDATE scheduler_dispatch_outbox
           SET attempt_count = attempt_count + 1,
               first_attempt_at = COALESCE(first_attempt_at, $4),
               updated_at = $4
         WHERE tenant_id = $1 AND id = $2::uuid AND claim_owner = $3
           AND status = 'dispatching' AND claim_expires_at > $4
           AND attempt_count < max_attempts
         RETURNING ` + dispatchColumns
	started, err := scanDispatch(s.tx.QueryRow(ctx, query, work.TenantID, work.ID, workerID, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DispatchWork{}, domain.ErrClaimLost
	}
	if err != nil {
		return domain.DispatchWork{}, err
	}
	result, err := s.tx.Exec(ctx, `
        UPDATE scheduler_occurrence
           SET status = 'dispatching', attempt_count = $3, updated_at = $4
         WHERE tenant_id = $1 AND id = $2::uuid`,
		started.TenantID, started.OccurrenceID, started.AttemptCount, now,
	)
	if err != nil {
		return domain.DispatchWork{}, err
	}
	if result.RowsAffected() != 1 {
		return domain.DispatchWork{}, domain.ErrClaimLost
	}
	return started, nil
}

func (s *txStore) DeferDispatch(ctx context.Context, work domain.DispatchWork, workerID string, nextAttemptAt time.Time, code, message string, now time.Time) error {
	return s.transitionDispatch(ctx, work, workerID, domain.OutboxRetrying, domain.OccurrenceRetrying, domain.DispatchResult{
		Code: code, Err: messageError(message),
	}, nextAttemptAt, time.Time{}, now)
}

func (s *txStore) CompleteDispatch(ctx context.Context, work domain.DispatchWork, workerID string, result domain.DispatchResult, now time.Time) error {
	return s.transitionDispatch(ctx, work, workerID, domain.OutboxSucceeded, domain.OccurrenceSucceeded, result, now, now, now)
}

func (s *txStore) RetryDispatch(ctx context.Context, work domain.DispatchWork, workerID string, result domain.DispatchResult, nextAttemptAt, now time.Time) error {
	return s.transitionDispatch(ctx, work, workerID, domain.OutboxRetrying, domain.OccurrenceRetrying, result, nextAttemptAt, time.Time{}, now)
}

func (s *txStore) FailDispatch(ctx context.Context, work domain.DispatchWork, workerID string, result domain.DispatchResult, now time.Time) error {
	return s.transitionDispatch(ctx, work, workerID, domain.OutboxFailed, domain.OccurrenceFailed, result, now, now, now)
}

func (s *txStore) transitionDispatch(
	ctx context.Context,
	work domain.DispatchWork,
	workerID string,
	outboxStatus string,
	occurrenceStatus string,
	result domain.DispatchResult,
	nextAttemptAt time.Time,
	completedAt time.Time,
	now time.Time,
) error {
	var completed any
	if !completedAt.IsZero() {
		completed = completedAt
	}
	message := ""
	if result.Err != nil {
		message = result.Err.Error()
		if len(message) > 1024 {
			message = message[:1024]
		}
	}
	updated, err := s.tx.Exec(ctx, `
        UPDATE scheduler_dispatch_outbox
           SET status = $4, next_attempt_at = $5, claim_owner = NULL,
               claim_expires_at = NULL, last_http_status = $6,
               last_error_code = $7, last_error_message = $8,
               completed_at = $9, updated_at = $10
         WHERE tenant_id = $1 AND id = $2::uuid AND claim_owner = $3
           AND status = 'dispatching'`,
		work.TenantID, work.ID, workerID, outboxStatus, nextAttemptAt,
		nullableStatus(result.Status), nullableString(result.Code), nullableString(message), completed, now,
	)
	if err != nil {
		return err
	}
	if updated.RowsAffected() != 1 {
		return domain.ErrClaimLost
	}
	updated, err = s.tx.Exec(ctx, `
        UPDATE scheduler_occurrence
           SET status = $3, outcome_code = $4, attempt_count = $5,
               last_http_status = $6, last_error_code = $4,
               last_error_message = $7, completed_at = $8, updated_at = $9
         WHERE tenant_id = $1 AND id = $2::uuid`,
		work.TenantID, work.OccurrenceID, occurrenceStatus, nullableString(result.Code),
		work.AttemptCount, nullableStatus(result.Status), nullableString(message), completed, now,
	)
	if err != nil {
		return err
	}
	if updated.RowsAffected() != 1 {
		return domain.ErrClaimLost
	}
	return nil
}

type textError string

func (e textError) Error() string { return string(e) }

func messageError(message string) error {
	if message == "" {
		return nil
	}
	return textError(message)
}
