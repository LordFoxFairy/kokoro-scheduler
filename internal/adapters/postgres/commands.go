package postgresadapter

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *txStore) LockCommandIdentity(ctx context.Context, tenantID, scope, idempotencyKey string) error {
	identity := tenantID + "\x1f" + scope + "\x1f" + idempotencyKey
	_, err := s.tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, identity)
	return err
}

func (s *txStore) FindCommandReceipt(ctx context.Context, tenantID, scope, idempotencyKey string) (domain.CommandReceipt, bool, error) {
	var receipt domain.CommandReceipt
	var resultBody []byte
	err := s.tx.QueryRow(ctx, `
        SELECT tenant_id, command_scope, idempotency_key, request_digest,
               request_id, result_code, result_body, created_at
          FROM scheduler_command_receipt
         WHERE tenant_id = $1 AND command_scope = $2 AND idempotency_key = $3`,
		tenantID, scope, idempotencyKey,
	).Scan(
		&receipt.TenantID, &receipt.CommandScope, &receipt.IdempotencyKey, &receipt.RequestDigest,
		&receipt.RequestID, &receipt.Result.Code, &resultBody, &receipt.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CommandReceipt{}, false, nil
	}
	if err != nil {
		return domain.CommandReceipt{}, false, err
	}
	if err := json.Unmarshal(resultBody, &receipt.Result); err != nil {
		return domain.CommandReceipt{}, false, err
	}
	receipt.CreatedAt = domain.NormalizeInstant(receipt.CreatedAt)
	return receipt, true, nil
}

func (s *txStore) InsertCommandReceipt(ctx context.Context, receipt domain.CommandReceipt) error {
	resultBody, err := json.Marshal(receipt.Result)
	if err != nil {
		return err
	}
	_, err = s.tx.Exec(ctx, `
        INSERT INTO scheduler_command_receipt (
            tenant_id, command_scope, idempotency_key, request_digest,
            request_id, result_code, result_body, created_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		receipt.TenantID, receipt.CommandScope, receipt.IdempotencyKey, receipt.RequestDigest,
		receipt.RequestID, receipt.Result.Code, resultBody, receipt.CreatedAt,
	)
	return err
}

func (s *txStore) CreateSchedule(ctx context.Context, schedule domain.Schedule) (domain.Schedule, error) {
	query := `
        INSERT INTO scheduler_schedule (
            tenant_id, name, schedule_rule, timezone, target_url, target_method,
            payload, status, misfire_policy, catch_up_limit, overlap_policy,
            max_attempts, retry_backoff_seconds, retry_max_backoff_seconds,
            retry_window_seconds, next_due_at, created_at, updated_at
        ) VALUES (
            $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
            $12, $13, $14, $15, $16, $17, $17
        ) ON CONFLICT ON CONSTRAINT uq_scheduler_schedule_tenant_name DO NOTHING
          RETURNING ` + scheduleColumns
	created, err := scanSchedule(s.tx.QueryRow(ctx, query,
		schedule.TenantID, schedule.Name, schedule.Rule, schedule.Timezone,
		schedule.TargetURL, schedule.Method, schedule.Payload, schedule.Status,
		schedule.MisfirePolicy, schedule.CatchUpLimit, schedule.OverlapPolicy,
		schedule.Retry.MaxAttempts, schedule.Retry.BackoffSeconds, schedule.Retry.MaxBackoffSeconds,
		schedule.Retry.MaxRetryWindowSeconds, schedule.NextDueAt, schedule.CreatedAt,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Schedule{}, domain.ErrScheduleAlreadyExists
	}
	return created, err
}

func (s *txStore) UpdateSchedule(ctx context.Context, schedule domain.Schedule) (domain.Schedule, error) {
	query := `
        UPDATE scheduler_schedule
           SET schedule_rule = $3, timezone = $4, target_url = $5, target_method = $6,
               payload = $7, status = $8, misfire_policy = $9, catch_up_limit = $10,
               overlap_policy = $11, max_attempts = $12, retry_backoff_seconds = $13,
               retry_max_backoff_seconds = $14, retry_window_seconds = $15,
               next_due_at = $16, claim_owner = NULL, claim_expires_at = NULL,
               version = version + 1, updated_at = $17
         WHERE tenant_id = $1 AND name = $2
         RETURNING ` + scheduleColumns
	updated, err := scanSchedule(s.tx.QueryRow(ctx, query,
		schedule.TenantID, schedule.Name, schedule.Rule, schedule.Timezone,
		schedule.TargetURL, schedule.Method, schedule.Payload, schedule.Status,
		schedule.MisfirePolicy, schedule.CatchUpLimit, schedule.OverlapPolicy,
		schedule.Retry.MaxAttempts, schedule.Retry.BackoffSeconds, schedule.Retry.MaxBackoffSeconds,
		schedule.Retry.MaxRetryWindowSeconds, schedule.NextDueAt, schedule.UpdatedAt,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Schedule{}, domain.ErrScheduleNotFound
	}
	return updated, err
}

func (s *txStore) DeleteSchedule(ctx context.Context, tenantID, name string) error {
	var id string
	err := s.tx.QueryRow(ctx, `
        DELETE FROM scheduler_schedule
         WHERE tenant_id = $1 AND name = $2
         RETURNING id::text`, tenantID, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrScheduleNotFound
	}
	return err
}

func (s *txStore) SetScheduleStatus(ctx context.Context, tenantID, name string, status domain.ScheduleStatus, nextDueAt, now time.Time) (domain.Schedule, error) {
	var nextDue any
	if !nextDueAt.IsZero() {
		nextDue = nextDueAt
	}
	query := `
        UPDATE scheduler_schedule
           SET status = $3,
               next_due_at = COALESCE($4::timestamptz, next_due_at),
               claim_owner = NULL, claim_expires_at = NULL,
               version = version + 1, updated_at = $5
         WHERE tenant_id = $1 AND name = $2
         RETURNING ` + scheduleColumns
	updated, err := scanSchedule(s.tx.QueryRow(ctx, query, tenantID, name, status, nextDue, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Schedule{}, domain.ErrScheduleNotFound
	}
	return updated, err
}

func (s *txStore) GetSchedule(ctx context.Context, tenantID, name string) (domain.Schedule, error) {
	query := `SELECT ` + scheduleColumns + `
          FROM scheduler_schedule
         WHERE tenant_id = $1 AND name = $2`
	schedule, err := scanSchedule(s.tx.QueryRow(ctx, query, tenantID, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Schedule{}, domain.ErrScheduleNotFound
	}
	return schedule, err
}
