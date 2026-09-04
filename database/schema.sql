CREATE TABLE IF NOT EXISTS scheduler_schedule (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id TEXT NOT NULL,
    name TEXT NOT NULL,
    schedule_rule TEXT NOT NULL,
    timezone TEXT NOT NULL,
    target_url TEXT NOT NULL,
    target_method TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'active',
    misfire_policy TEXT NOT NULL DEFAULT 'fire_once',
    catch_up_limit SMALLINT NOT NULL DEFAULT 1,
    overlap_policy TEXT NOT NULL DEFAULT 'forbid',
    max_attempts SMALLINT NOT NULL DEFAULT 1,
    retry_backoff_seconds INTEGER NOT NULL DEFAULT 1,
    retry_max_backoff_seconds INTEGER NOT NULL DEFAULT 3600,
    retry_window_seconds INTEGER NOT NULL DEFAULT 3600,
    next_due_at TIMESTAMPTZ(3) NOT NULL,
    claim_owner TEXT,
    claim_expires_at TIMESTAMPTZ(3),
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at TIMESTAMPTZ(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    CONSTRAINT uq_scheduler_schedule_tenant_name UNIQUE (tenant_id, name),
    CONSTRAINT ck_scheduler_schedule_tenant CHECK (tenant_id <> '' AND length(tenant_id) <= 128),
    CONSTRAINT ck_scheduler_schedule_name CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
    CONSTRAINT ck_scheduler_schedule_rule CHECK (schedule_rule <> '' AND length(schedule_rule) <= 256),
    CONSTRAINT ck_scheduler_schedule_timezone CHECK (timezone <> '' AND length(timezone) <= 128),
    CONSTRAINT ck_scheduler_schedule_target_method CHECK (target_method IN ('POST', 'PUT')),
    CONSTRAINT ck_scheduler_schedule_payload CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT ck_scheduler_schedule_status CHECK (status IN ('active', 'paused')),
    CONSTRAINT ck_scheduler_schedule_misfire CHECK (misfire_policy IN ('skip', 'fire_once', 'catch_up_bounded')),
    CONSTRAINT ck_scheduler_schedule_catch_up CHECK (
        (misfire_policy = 'catch_up_bounded' AND catch_up_limit BETWEEN 1 AND 100)
        OR (misfire_policy IN ('skip', 'fire_once') AND catch_up_limit = 1)
    ),
    CONSTRAINT ck_scheduler_schedule_overlap CHECK (overlap_policy IN ('allow', 'forbid')),
    CONSTRAINT ck_scheduler_schedule_attempts CHECK (max_attempts BETWEEN 1 AND 10),
    CONSTRAINT ck_scheduler_schedule_retry_backoff CHECK (
        retry_backoff_seconds BETWEEN 1 AND 3600
        AND retry_max_backoff_seconds BETWEEN retry_backoff_seconds AND 3600
        AND retry_window_seconds BETWEEN 1 AND 86400
    ),
    CONSTRAINT ck_scheduler_schedule_claim CHECK (
        (claim_owner IS NULL AND claim_expires_at IS NULL)
        OR (claim_owner IS NOT NULL AND claim_expires_at IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS ix_scheduler_schedule_due
    ON scheduler_schedule (next_due_at, id)
    WHERE status = 'active';

CREATE INDEX IF NOT EXISTS ix_scheduler_schedule_claim_expiry
    ON scheduler_schedule (claim_expires_at, id)
    WHERE claim_expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS scheduler_occurrence (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id TEXT NOT NULL,
    schedule_id UUID NOT NULL,
    schedule_name TEXT NOT NULL,
    scheduled_at TIMESTAMPTZ(3) NOT NULL,
    observed_at TIMESTAMPTZ(3) NOT NULL,
    status TEXT NOT NULL,
    outcome_code TEXT,
    missed_count INTEGER NOT NULL DEFAULT 1,
    attempt_count SMALLINT NOT NULL DEFAULT 0,
    last_http_status INTEGER,
    last_error_code TEXT,
    last_error_message TEXT,
    created_at TIMESTAMPTZ(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at TIMESTAMPTZ(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    completed_at TIMESTAMPTZ(3),
    CONSTRAINT uq_scheduler_occurrence_identity UNIQUE (tenant_id, schedule_id, scheduled_at),
    CONSTRAINT ck_scheduler_occurrence_tenant CHECK (tenant_id <> '' AND length(tenant_id) <= 128),
    CONSTRAINT ck_scheduler_occurrence_status CHECK (
        status IN ('pending', 'dispatching', 'retrying', 'succeeded', 'failed', 'skipped')
    ),
    CONSTRAINT ck_scheduler_occurrence_missed_count CHECK (missed_count >= 1),
    CONSTRAINT ck_scheduler_occurrence_attempt_count CHECK (attempt_count BETWEEN 0 AND 10),
    CONSTRAINT ck_scheduler_occurrence_http_status CHECK (
        last_http_status IS NULL OR last_http_status BETWEEN 100 AND 599
    ),
    CONSTRAINT ck_scheduler_occurrence_completed CHECK (
        (status IN ('succeeded', 'failed', 'skipped') AND completed_at IS NOT NULL)
        OR (status IN ('pending', 'dispatching', 'retrying') AND completed_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS ix_scheduler_occurrence_tenant_schedule
    ON scheduler_occurrence (tenant_id, schedule_id, scheduled_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS ix_scheduler_occurrence_status
    ON scheduler_occurrence (status, scheduled_at, id);

CREATE TABLE IF NOT EXISTS scheduler_command_receipt (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id TEXT NOT NULL,
    command_scope TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    request_id TEXT NOT NULL,
    result_code TEXT NOT NULL,
    result_body JSONB NOT NULL,
    created_at TIMESTAMPTZ(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    CONSTRAINT uq_scheduler_command_receipt_identity UNIQUE (tenant_id, command_scope, idempotency_key),
    CONSTRAINT ck_scheduler_command_receipt_tenant CHECK (tenant_id <> '' AND length(tenant_id) <= 128),
    CONSTRAINT ck_scheduler_command_receipt_scope CHECK (command_scope <> '' AND length(command_scope) <= 256),
    CONSTRAINT ck_scheduler_command_receipt_key CHECK (idempotency_key <> '' AND length(idempotency_key) <= 256),
    CONSTRAINT ck_scheduler_command_receipt_digest CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT ck_scheduler_command_receipt_request_id CHECK (request_id <> '' AND length(request_id) <= 128),
    CONSTRAINT ck_scheduler_command_receipt_result CHECK (jsonb_typeof(result_body) = 'object')
);

CREATE INDEX IF NOT EXISTS ix_scheduler_command_receipt_created
    ON scheduler_command_receipt (created_at, id);

CREATE TABLE IF NOT EXISTS scheduler_dispatch_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id TEXT NOT NULL,
    occurrence_id UUID NOT NULL,
    schedule_id UUID NOT NULL,
    schedule_name TEXT NOT NULL,
    scheduled_at TIMESTAMPTZ(3) NOT NULL,
    target_url TEXT NOT NULL,
    target_method TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempt_count SMALLINT NOT NULL DEFAULT 0,
    max_attempts SMALLINT NOT NULL,
    retry_backoff_seconds INTEGER NOT NULL,
    retry_max_backoff_seconds INTEGER NOT NULL,
    retry_window_seconds INTEGER NOT NULL,
    next_attempt_at TIMESTAMPTZ(3) NOT NULL,
    first_attempt_at TIMESTAMPTZ(3),
    claim_owner TEXT,
    claim_expires_at TIMESTAMPTZ(3),
    last_http_status INTEGER,
    last_error_code TEXT,
    last_error_message TEXT,
    created_at TIMESTAMPTZ(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at TIMESTAMPTZ(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    completed_at TIMESTAMPTZ(3),
    CONSTRAINT uq_scheduler_dispatch_outbox_occurrence UNIQUE (tenant_id, occurrence_id),
    CONSTRAINT ck_scheduler_dispatch_outbox_tenant CHECK (tenant_id <> '' AND length(tenant_id) <= 128),
    CONSTRAINT ck_scheduler_dispatch_outbox_target_method CHECK (target_method IN ('POST', 'PUT')),
    CONSTRAINT ck_scheduler_dispatch_outbox_payload CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT ck_scheduler_dispatch_outbox_status CHECK (
        status IN ('pending', 'dispatching', 'retrying', 'succeeded', 'failed')
    ),
    CONSTRAINT ck_scheduler_dispatch_outbox_attempts CHECK (
        max_attempts BETWEEN 1 AND 10 AND attempt_count BETWEEN 0 AND max_attempts
    ),
    CONSTRAINT ck_scheduler_dispatch_outbox_retry CHECK (
        retry_backoff_seconds BETWEEN 1 AND 3600
        AND retry_max_backoff_seconds BETWEEN retry_backoff_seconds AND 3600
        AND retry_window_seconds BETWEEN 1 AND 86400
    ),
    CONSTRAINT ck_scheduler_dispatch_outbox_http_status CHECK (
        last_http_status IS NULL OR last_http_status BETWEEN 100 AND 599
    ),
    CONSTRAINT ck_scheduler_dispatch_outbox_claim CHECK (
        (status = 'dispatching' AND claim_owner IS NOT NULL AND claim_expires_at IS NOT NULL)
        OR (status <> 'dispatching' AND claim_owner IS NULL AND claim_expires_at IS NULL)
    ),
    CONSTRAINT ck_scheduler_dispatch_outbox_completed CHECK (
        (status IN ('succeeded', 'failed') AND completed_at IS NOT NULL)
        OR (status IN ('pending', 'dispatching', 'retrying') AND completed_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS ix_scheduler_dispatch_outbox_ready
    ON scheduler_dispatch_outbox (next_attempt_at, id)
    WHERE status IN ('pending', 'retrying');

CREATE INDEX IF NOT EXISTS ix_scheduler_dispatch_outbox_claim_expiry
    ON scheduler_dispatch_outbox (claim_expires_at, id)
    WHERE status = 'dispatching';

COMMENT ON TABLE scheduler_schedule IS
    'Owner: kokoro-scheduler. Durable tenant-scoped schedule definitions and next UTC due instant.';
COMMENT ON TABLE scheduler_occurrence IS
    'Owner: kokoro-scheduler. Durable occurrence lifecycle and observable misfire/overlap outcomes.';
COMMENT ON TABLE scheduler_command_receipt IS
    'Owner: kokoro-scheduler. Append-only idempotent internal command outcomes.';
COMMENT ON TABLE scheduler_dispatch_outbox IS
    'Owner: kokoro-scheduler. Durable at-least-once dispatch work and retry state.';
