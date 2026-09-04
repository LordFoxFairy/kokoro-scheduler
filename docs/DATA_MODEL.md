# kokoro-scheduler 数据模型

唯一 current schema 是 [`../database/schema.sql`](../database/schema.sql)。本仓不保存 migration，不使用 `FOREIGN KEY`/`REFERENCES`。所有关系由 Application transaction、tenant predicate、业务唯一键和 reconciliation 维护。

## 1. `scheduler_schedule`

Durable tenant-scoped Schedule definition。

关键字段：`tenant_id`、`name`、`schedule_rule`、`timezone`、target snapshot、misfire/catch-up/overlap/retry policy、`status`、`next_due_at`、claim、`version`、timestamps。

不变量：

- `UNIQUE (tenant_id, name)`；
- active/paused、三种 misfire、allow/forbid 与 retry 范围均有 CHECK；
- `next_due_at` 是 UTC instant；rule/timezone 保留本地调度语义；
- claim owner/expiry 同时为空或同时存在；
- due partial index 为 `(next_due_at, id) WHERE status='active'`。

## 2. `scheduler_occurrence`

每个计划 instant 的 durable lifecycle 和可观测 outcome。

```text
pending -> dispatching -> succeeded
                      -> retrying -> dispatching
                      -> failed
planned -> skipped (misfire / bound / overlap)
```

`UNIQUE (tenant_id, schedule_id, scheduled_at)` 防止相同 Schedule instant 重复物化。`scheduled_at`、`observed_at`、`completed_at` 使用 `TIMESTAMPTZ(3)`；终态必须有 `completed_at`。`schedule_name` 是创建时快照，Schedule 删除后历史仍可解释。

## 3. `scheduler_command_receipt`

Append-only internal command outcome。

`UNIQUE (tenant_id, command_scope, idempotency_key)` 是 mutation replay identity。保存 request SHA-256 digest、原 request ID、稳定 result code 与 result JSON。没有 update path；调用方重试读取首次结果。

## 4. `scheduler_dispatch_outbox`

Occurrence 的 durable target snapshot、attempt 与 retry state。

```text
pending -> dispatching -> succeeded
                      -> retrying -> dispatching
                      -> failed
```

`UNIQUE (tenant_id, occurrence_id)` 保证一个 dispatchable occurrence 只有一个 outbox。ready partial index 覆盖 pending/retrying 的 `(next_attempt_at, id)`；claim-expiry index支持 crash recovery。`attempt_count <= max_attempts`、terminal completion、claim/status consistency 均由 CHECK 保护。

## 5. 原子关系维护

- Schedule 与 receipt：同一 command transaction；
- Occurrence 与 outbox：同一 planner transaction；
- outbox 与 occurrence 状态：同一 dispatch result transaction；
- schedule/outbox claim：SQL lock + owner + expiry；
- 所有读写显式 tenant predicate；内部 UUID 引用不由数据库外键耦合。

## 6. 时间、NULL 与 JSON

- 所有 instant：`TIMESTAMPTZ(3)`，写入前 `.UTC().Truncate(time.Millisecond)`；
- `NULL completed_at` 表示未终结；`NULL last_http_status/error` 表示尚无对应结果；
- `payload` 只保存 opaque JSON object snapshot，不替代核心查询列；
- 同毫秒稳定顺序使用 UUID `id` 作为第二排序键。

## 7. Retention

当前 schema 未实现自动 TTL、partition 或 GC。删除 Schedule 不级联删除 occurrence/outbox/receipt；已建立的 dispatch snapshot继续收敛。部署 owner 在真实容量、审计和 replay 窗口形成后，需要给出按 tenant/time 的 archival 与删除事务，并先补 integration/operational gate。禁止直接清空共享数据库作为日常处置。
