# kokoro-scheduler API Contract v1

## 1. Boundary

`kokoro-scheduler` is a Go infrastructure service. Its v1 runtime inputs are
the deployment-owned `SCHEDULER_JOBS_JSON` document and the internal job
command surface below. The internal surface registers generic `ScheduleJob`
definitions only; it is not a business `ScheduledTask` CRUD API and it does
not read Billing, Credit, Agent, or any other business database. A business
repository owns the command endpoint and its business response/error code.

The scheduler may use Redis for a distributed occurrence lease. PostgreSQL is
the platform persistence baseline for business repositories; this scheduler
remains configuration-driven in v1 and therefore has no PostgreSQL schema.
Redis is coordination only and is never the source of billing or execution
truth.

If `SCHEDULER_JOBS_JSON` is unset, empty, or contains only whitespace, it is
treated as an empty job list (`[]`).

The scheduler has no public resource CRUD endpoint, cursor pagination, OAuth
user surface, SSE stream, or user-facing API. The target business command owns
those concerns; scheduler-side receipts are callback/log records with the
fields defined below.

## 2. Internal job command surface

The BFF-to-Scheduler registration adapter uses these routes:

```text
POST   /internal/scheduler/v1/jobs/{name}          register
PUT    /internal/scheduler/v1/jobs/{name}          replace/update
DELETE /internal/scheduler/v1/jobs/{name}          remove
POST   /internal/scheduler/v1/jobs/{name}/pause    pause future occurrences
POST   /internal/scheduler/v1/jobs/{name}/resume   resume future occurrences
```

`{name}` must match `[a-z0-9][a-z0-9._-]{0,63}`. The routes are internal
only and are not exposed through the public BFF user API.

### Authentication and request metadata

Every command request must include:

```http
Authorization: Bearer <SCHEDULER_INTERNAL_SERVICE_TOKEN>
X-Request-Id: <request-id>
Idempotency-Key: <mutation-key>
```

The service token is configured with `SCHEDULER_INTERNAL_SERVICE_TOKEN` and is
accepted only as a Bearer token in `Authorization`; it is compared in constant
time. Only the canonical credential and request-id headers are accepted.

Missing or invalid authentication returns `401 service_auth_failed`.
Missing request IDs return `400 request_id_required`; missing idempotency keys
return `400 idempotency_key_required`. A successful or failed command response
contains the request ID in `meta.request_id` and the `X-Request-Id` response
header.

### JSON and mutation semantics

`POST` and `PUT` require `Content-Type: application/json` and one JSON object.
Unknown or duplicate fields, `null` fields, malformed JSON, trailing JSON
values, invalid types, invalid URLs, invalid cron expressions, and a body name
that differs from `{name}` return `400 invalid_job`. The `name` field may be
omitted from the body because it is supplied by the path; when present it must
match.

`DELETE` accepts no body or `{}`. Pause and resume accept no body or `{}`.
Non-empty bodies on those operations must also be strict JSON objects. All
mutations require an idempotency key. The idempotency scope is method, path,
and key. Within one process, the same key and normalized JSON payload replay
the exact original response; the same key with a different payload returns
`409 idempotency_conflict`.

- `POST` registers only a new job. A different key for an existing job returns
  `409 job_already_exists`; it never silently updates the job.
- `PUT` updates only an existing job and returns `404 job_not_found` when the
  job is absent.
- `DELETE` removes an existing job and returns `404 job_not_found` when it is
  already absent under a new idempotency key. Replays use the original result.
- Pause/resume return `404 job_not_found` for an absent job and affect future
  occurrences only; an in-flight dispatch is not cancelled.

Successful responses use `200` and the following shape:

```json
{
  "data": { "job": { "name": "billing.reconcile" }, "status": "registered" },
  "meta": { "request_id": "req_scheduler_register_1" }
}
```

The delete response uses `data.name` and `data.status: "deleted"`; pause and
resume use `data.name` and `data.paused`.

`/healthz` and `/readyz` remain unauthenticated `GET` probes. Readiness does
not require a business database. It verifies that scheduling has started and,
when Redis coordination is configured, performs a deadline-bound Redis PING.
Both probes use the same `data/meta.request_id` envelope as other successful
responses; readiness failures use the standard error envelope and return 503.

The command registry and idempotency receipts are process-local memory. They
are intentionally not a database or a durable `ScheduledTask` store. BFF or
deployment orchestration must replay registrations after a scheduler restart.

## 3. ScheduleJob configuration resource

The JSON array contains `ScheduleJob` resources:

| Field | Type | Required | Contract |
|---|---|---:|---|
| `name` | string | yes | Stable `[a-z0-9][a-z0-9._-]{0,63}` identifier; unique in one deployment |
| `schedule` | string | yes | UTC standard cron expression or `@every <duration>` |
| `url` | string | yes | Internal command endpoint supplied by deployment |
| `method` | string | no | `POST` or `PUT`; default `POST` |
| `body` | object | no | JSON command payload; default `{}` |
| `retry.max_attempts` | integer | no | 1–10; default `1` |
| `retry.backoff_seconds` | integer | no | 1–3600; initial exponential-backoff ceiling; default `1` |
| `retry.max_backoff_seconds` | integer | no | 1–3600 and not less than `backoff_seconds`; per-delay ceiling; default `3600` |
| `retry.max_retry_window_seconds` | integer | no | 1–86400; maximum interval in which another attempt may begin; default `3600` |
| `misfire_policy` | string | no | `skip` or `fire_once`; default `skip` |
| `paused` | boolean | no | Initial runtime state; default `false` |

Unknown fields, duplicate names, invalid URLs/methods, and invalid cron
expressions fail startup before any job is registered.

## 4. State machines

### ScheduleJob

```text
configured -> active -> paused
                  \-> removed (deployment config change)
paused    -> active
```

`Pause(name)` and `Resume(name)` affect subsequent occurrences. An occurrence
already claimed remains under the current run and is not cancelled by pause.

### Execution receipt

```text
pending -> claimed -> running -> succeeded
                         \-> retrying -> running
                         \-> failed
pending --misfire=skip--> discarded
pending --misfire=fire_once--> claimed
```

Redis claim keys are
`kokoro:scheduler:run:<name>:<occurrence>` with a 26-hour TTL. The scheduler
keeps a successful claim until that TTL expires; releasing it immediately after
dispatch would allow another scheduler instance to execute the same occurrence
again. The lease prevents duplicate dispatch across scheduler instances during
the deduplication window; it is not a durable execution receipt. The command
endpoint must persist its own idempotency receipt in PostgreSQL when business
truth is required.

## 5. Dispatch contract

Every HTTP dispatch is JSON and includes:

```http
Content-Type: application/json
X-Kokoro-Scheduler-Job: <name>
X-Kokoro-Scheduler-Occurrence: <YYYYMMDDTHHMMSSZ>
X-Request-Id: sched_<name>_<UTC timestamp>
Idempotency-Key: schedule:<name>:<UTC timestamp>
```

When `SCHEDULER_TARGET_SERVICE_TOKEN` is non-empty, every outbound dispatch also
includes the following target-service authentication header:

```http
Authorization: Bearer <SCHEDULER_TARGET_SERVICE_TOKEN>
```

When the variable is empty or unset, the `Authorization` header is omitted for
targets whose contract does not require service authentication. This outbound
target credential is separate from `SCHEDULER_INTERNAL_SERVICE_TOKEN`, which authenticates BFF
requests entering the scheduler command surface. The configured target token
is reused across retries and is never included in scheduler logs.

`X-Kokoro-Scheduler-Occurrence` is the stable occurrence identity, formatted
as the scheduled occurrence timestamp in UTC (`YYYYMMDDTHHMMSSZ`). It is
derived from the occurrence time supplied to the HTTP runner and is stable for
retries of the same occurrence. `X-Request-Id` identifies the delivery attempt.
`Idempotency-Key` identifies the scheduled occurrence and is reused by a caller
that replays that occurrence. The scheduler does not manufacture an end-user
identity; the target service validates the trusted service context and its own
authorization boundary.

Every dispatch also carries a W3C `traceparent`. Its trace identifier is
stable across retries of one occurrence; the attempt span identifier is
derived separately. Structured JSON logs correlate `request_id`, `trace_id`,
result, attempt count, and total duration without recording tokens or bodies.

Only 2xx is success. Network errors, HTTP 429, and HTTP 5xx are retryable when
both the configured attempt budget and retry window remain. Other 4xx
responses fail immediately.

After failed attempt `n`, the scheduler computes the full-jitter upper bound
as:

```text
min(backoff_seconds * 2^(n-1), max_backoff_seconds, retry_window_remaining)
```

It then waits for a cryptographically random duration uniformly selected from
zero through that upper bound. A fresh value is selected after every
retryable failure so independently running scheduler instances do not retry in
lockstep. The retry window starts immediately before the first target attempt;
no later attempt starts at or after its deadline. An attempt already in
progress remains subject to the 30-second dispatch timeout and process
cancellation. Exhausting the attempt or time budget returns the last target
result.

The execution callback receives a `RunResult` containing `status`, `attempts`,
`request_id`, `idempotency_key`, and an optional error. Standard scheduler
error categories are:

| Code | Meaning |
|---|---|
| `SCHEDULER_CONFIG_INVALID` | Startup configuration or cron validation failed |
| `SCHEDULER_COORDINATION_UNAVAILABLE` | Redis lease operation failed |
| `SCHEDULER_TARGET_UNAVAILABLE` | Network-level target failure |
| `SCHEDULER_TARGET_TIMEOUT` | Target request exceeded the HTTP timeout |
| `SCHEDULER_TARGET_REJECTED` | Target returned a non-success HTTP response |
| `SCHEDULER_RETRY_JITTER_UNAVAILABLE` | The process random source failed before a retry delay could be selected |
| `SCHEDULER_MISFIRED` | Recovery trigger was discarded by `misfire_policy=skip` |

## 6. Verification

```bash
gofmt -l .
go test ./...
go vet ./...
go build ./cmd/scheduler
```

The contract tests cover strict configuration parsing, retry boundaries,
capped exponential backoff, full jitter, the total retry window, pause/resume,
request identity, normalized occurrence identity and HTTP headers, occurrence
lease behavior, and success/failure classification.
