package postgresadapter

import (
	"encoding/json"
	"time"

	"github.com/LordFoxFairy/kokoro-scheduler/internal/domain"
)

const scheduleColumns = `
    id::text, tenant_id, name, schedule_rule, timezone,
    target_url, target_method, payload, status, misfire_policy,
    catch_up_limit, overlap_policy, max_attempts, retry_backoff_seconds,
    retry_max_backoff_seconds, retry_window_seconds, next_due_at, version,
    created_at, updated_at, claim_owner, claim_expires_at`

const dispatchColumns = `
    id::text, tenant_id, occurrence_id::text, schedule_id::text, schedule_name,
    scheduled_at, target_url, target_method, payload, status, attempt_count,
    max_attempts, retry_backoff_seconds, retry_max_backoff_seconds,
    retry_window_seconds, next_attempt_at, first_attempt_at, created_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSchedule(row rowScanner) (domain.Schedule, error) {
	var schedule domain.Schedule
	var method, status, misfire, overlap string
	var payload []byte
	var claimOwner *string
	var claimExpiresAt *time.Time
	err := row.Scan(
		&schedule.ID, &schedule.TenantID, &schedule.Name, &schedule.Rule, &schedule.Timezone,
		&schedule.TargetURL, &method, &payload, &status, &misfire,
		&schedule.CatchUpLimit, &overlap, &schedule.Retry.MaxAttempts, &schedule.Retry.BackoffSeconds,
		&schedule.Retry.MaxBackoffSeconds, &schedule.Retry.MaxRetryWindowSeconds, &schedule.NextDueAt, &schedule.Version,
		&schedule.CreatedAt, &schedule.UpdatedAt, &claimOwner, &claimExpiresAt,
	)
	if err != nil {
		return domain.Schedule{}, err
	}
	schedule.Method = domain.Method(method)
	schedule.Payload = append(json.RawMessage(nil), payload...)
	schedule.Status = domain.ScheduleStatus(status)
	schedule.MisfirePolicy = domain.MisfirePolicy(misfire)
	schedule.OverlapPolicy = domain.OverlapPolicy(overlap)
	if claimOwner != nil {
		schedule.ClaimOwner = *claimOwner
	}
	if claimExpiresAt != nil {
		schedule.ClaimExpiresAt = domain.NormalizeInstant(*claimExpiresAt)
	}
	schedule.NextDueAt = domain.NormalizeInstant(schedule.NextDueAt)
	schedule.CreatedAt = domain.NormalizeInstant(schedule.CreatedAt)
	schedule.UpdatedAt = domain.NormalizeInstant(schedule.UpdatedAt)
	return schedule, nil
}

func scanDispatch(row rowScanner) (domain.DispatchWork, error) {
	var work domain.DispatchWork
	var method string
	var payload []byte
	var firstAttemptAt *time.Time
	err := row.Scan(
		&work.ID, &work.TenantID, &work.OccurrenceID, &work.ScheduleID, &work.ScheduleName,
		&work.ScheduledAt, &work.TargetURL, &method, &payload, &work.Status, &work.AttemptCount,
		&work.Retry.MaxAttempts, &work.Retry.BackoffSeconds, &work.Retry.MaxBackoffSeconds,
		&work.Retry.MaxRetryWindowSeconds, &work.NextAttemptAt, &firstAttemptAt, &work.CreatedAt,
	)
	if err != nil {
		return domain.DispatchWork{}, err
	}
	work.Method = domain.Method(method)
	work.Payload = append(json.RawMessage(nil), payload...)
	work.ScheduledAt = domain.NormalizeInstant(work.ScheduledAt)
	work.NextAttemptAt = domain.NormalizeInstant(work.NextAttemptAt)
	work.CreatedAt = domain.NormalizeInstant(work.CreatedAt)
	if firstAttemptAt != nil {
		work.FirstAttemptAt = domain.NormalizeInstant(*firstAttemptAt)
	}
	return work, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableStatus(status int) any {
	if status == 0 {
		return nil
	}
	return status
}
