# kokoro-scheduler API Contract v1

## 1. Boundary

`kokoro-scheduler` is a Go infrastructure service. Its v1 runtime input is
the deployment-owned `SCHEDULER_JOBS_JSON` document; it does not expose a
business CRUD API and it does not read Billing, Credit, Agent, or any other
business database. A business repository owns the command endpoint and its
business response/error code.

The scheduler may use Redis for a distributed occurrence lease. PostgreSQL is
the platform persistence baseline for business repositories; this scheduler
remains configuration-driven in v1 and therefore has no PostgreSQL schema.
Redis is coordination only and is never the source of billing or execution
truth.

Because v1 has no public resource CRUD endpoint, cursor pagination, OAuth
tokens, SSE stream, or user-facing response envelope are not scheduler
surfaces. The target business command owns those concerns; scheduler-side
receipts are callback/log records with the fields defined below.

## 2. ScheduleJob configuration resource

The JSON array contains `ScheduleJob` resources:

| Field | Type | Required | Contract |
|---|---|---:|---|
| `name` | string | yes | Stable `[a-z0-9][a-z0-9._-]{0,63}` identifier; unique in one deployment |
| `schedule` | string | yes | UTC standard cron expression or `@every <duration>` |
| `url` | string | yes | Internal command endpoint supplied by deployment |
| `method` | string | no | `POST` or `PUT`; default `POST` |
| `body` | object | no | JSON command payload; default `{}` |
| `retry.max_attempts` | integer | no | 1–10; default `1` |
| `retry.backoff_seconds` | integer | no | 0–3600; exponential multiplier per attempt; default `0` |
| `misfire_policy` | string | no | `skip` or `fire_once`; default `skip` |
| `paused` | boolean | no | Initial runtime state; default `false` |

Unknown fields, duplicate names, invalid URLs/methods, and invalid cron
expressions fail startup before any job is registered.

## 3. State machines

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
`kokoro:scheduler:run:<name>:<occurrence>` with a 26-hour TTL. The lease
prevents duplicate dispatch across scheduler instances; it is not a durable
execution receipt. The command endpoint must persist its own idempotency receipt
in PostgreSQL when business truth is required.

## 4. Dispatch contract

Every HTTP dispatch is JSON and includes:

```http
Content-Type: application/json
X-Kokoro-Scheduler-Job: <name>
X-Request-Id: sched_<name>_<UTC timestamp>
Idempotency-Key: schedule:<name>:<UTC timestamp>
```

`X-Request-Id` identifies the delivery attempt. `Idempotency-Key` identifies
the scheduled occurrence and is reused by a caller that replays that
occurrence. The scheduler does not manufacture an end-user identity; the
target service validates the trusted service context and its own authorization
boundary.

Only 2xx is success. Network errors, HTTP 429, and HTTP 5xx are retryable when
the configured attempt budget remains. Other 4xx responses fail immediately.
Backoff is `backoff_seconds * 2^(attempt-1)` and is bounded by the process
context.

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
| `SCHEDULER_MISFIRED` | Recovery trigger was discarded by `misfire_policy=skip` |

## 5. Verification

```bash
gofmt -l .
go test ./...
go vet ./...
go build ./cmd/scheduler
```

The contract tests cover strict configuration parsing, retry policy,
pause/resume, request identity, HTTP headers, occurrence lease behavior, and
success/failure classification.
