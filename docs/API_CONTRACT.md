# kokoro-scheduler API contract

字段级唯一事实源是 [`../contract/openapi/v1/openapi.yaml`](../contract/openapi/v1/openapi.yaml)。本文解释调用语义，不复制成第二套 machine schema。

## 1. 可见性与认证

- owner：`kokoro-scheduler`；
- inbound visibility：`internal-owner`；
- outbound dispatch visibility：`event-protocol`；
- base prefix：`/internal/scheduler/v1`。

所有 mutation 要求：

```text
Authorization: Bearer <service token>
X-Kokoro-Tenant-Id: <trusted tenant>
X-Request-Id: <stable caller request id>
Idempotency-Key: <stable mutation identity>
```

Transport 不从 JSON body 推导 tenant。service token 为空时 command routes fail closed。每个 OpenAPI operation 声明 owner、visibility、stability、idempotency 和 permission metadata。

## 2. Control surface

| Method | Path | 语义 |
|---|---|---|
| GET | `/healthz` | 进程存活 |
| GET | `/readyz` | Runtime + PostgreSQL；配置 Redis 时再检查 Redis |
| POST | `/internal/scheduler/v1/schedules/{name}` | 创建 Schedule |
| PUT | `/internal/scheduler/v1/schedules/{name}` | 完整替换 Schedule |
| DELETE | `/internal/scheduler/v1/schedules/{name}` | 删除 Schedule 定义；既有 outbox snapshot 继续收敛 |
| POST | `/internal/scheduler/v1/schedules/{name}/pause` | 暂停新 due materialization |
| POST | `/internal/scheduler/v1/schedules/{name}/resume` | 从 resume 当前 instant 重新计算下一次 due |

DELETE/pause/resume 接受空 body 或严格 `{}`。Schedule body 为严格 JSON object：未知字段、重复 key、null、trailing JSON、错误 content type 和超过 1 MiB 均拒绝。`name` 可省略；若提供必须与 path 一致。

POST/PUT 的主要字段：本地 `schedule` rule、`timezone`、target `url`/`method`/opaque object `body`、retry、misfire、catch-up、overlap 和 `paused`。默认值及范围由 canonical OpenAPI 和 Domain 同时约束。

## 3. Durable idempotency

identity 是 `(tenant_id, HTTP method:path, Idempotency-Key)`；request digest 包含 command scope 与规范化 payload。

- 首次请求在同一事务中修改 Schedule 并写 receipt；
- 相同 identity + digest 返回首次保存的 status/body/request ID，即使进程已重启；
- 相同 identity + 不同 digest 返回 `409 idempotency_conflict`；
- 不同 tenant 的相同 path/key 相互隔离。

调用方在 response 丢失时必须重发原 method、path、tenant、key 和语义相同的 body。

## 4. 时间与 recurrence

API instant 为 RFC3339 UTC；Schedule rule 与 IANA timezone 分列。`next_due_at` 和 dispatch 的 `X-Kokoro-Scheduler-Occurrence` 都是 UTC。DST、DOM/DOW 和 unsupported grammar 的精确语义见 [`TECHNICAL_DESIGN.md`](./TECHNICAL_DESIGN.md)。

## 5. Outbound dispatch

OpenAPI `webhooks.scheduleOccurrenceDispatch` 定义 Scheduler 调目标 owner 的 POST/PUT。opaque JSON body 不被 Scheduler 解释。headers：

- `X-Kokoro-Tenant-Id`；
- `X-Kokoro-Scheduler-Schedule`；
- `X-Kokoro-Scheduler-Occurrence`（RFC3339 UTC）；
- `X-Request-Id`；
- `Idempotency-Key`；
- W3C `traceparent`；
- 可选独立 outbound Bearer。

2xx 为成功。408/425/429/5xx 与网络/timeout 可重试；其他 HTTP 为永久失败。3xx 不跟随。每次 attempt 使用同一 occurrence identity，目标 owner 必须 durable deduplicate。

## 6. Error envelope

成功：`{"data": ..., "meta":{"request_id":"..."}}`。错误：`{"error":{"code":"...","message":"..."},"meta":{"request_id":"..."}}`。不返回 SQL、stack、token、下游 body 或 provider 原文。

稳定错误包括 auth/request/tenant/idempotency/route/media/body validation，以及 `schedule_already_exists`、`schedule_not_found`、`scheduler_command_failed`。具体 status 与 schema 以 OpenAPI 为准。

## 7. Contract gate

```bash
./scripts/contract-check
```

该 gate 解析 canonical document、检查 `$ref`/operation metadata/关键 schema，执行每个 control route 的 runtime parity，并把 dispatch header 与 concrete HTTP adapter 对齐。`breaking-policy.json` 固定 v1 protected surface；`manifest.json` 固定 artifact SHA-256 provenance。
