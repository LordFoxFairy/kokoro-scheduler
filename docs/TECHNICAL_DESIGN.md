# kokoro-scheduler 技术设计

## 1. Owner 与原则

`kokoro-scheduler` 拥有通用 Schedule、Occurrence、command receipt、dispatch outbox、retry 与 claim。它不拥有 BFF `ScheduledTask`、IAM 权限事实或目标 command 的业务结果。

架构锁定为：

```text
trusted caller -> HTTP transport -> Application command transaction -> PostgreSQL
                                                            |
gocron/v2 fixed wakeup -> Application cycle -> due planner -> occurrence + outbox
                                            -> dispatcher -> target HTTP
                                                   |
                                            optional Redis DB 7 lease
```

PostgreSQL 是可恢复事实源。gocron/v2 是可替换 `Wakeup` adapter，不读取 schedule rule，也不保存任何执行历史。Redis 删除或不可用不会删除事实；配置 Redis 时协调失败会 defer durable outbox，而不是绕过 lease。

## 2. 分层与 ports

```text
cmd/scheduler/                 composition root
internal/domain/               pure model/state/policy
internal/application/          transaction use cases and recovery
internal/ports/                Store/TxStore, Clock, Recurrence, Wakeup, Lease, Target
internal/adapters/postgres/    durable store and claims
internal/adapters/recurrence/  recurrence calculation
internal/adapters/gocron/      process wakeup only
internal/adapters/redis/       optional coordination lease
internal/adapters/httpclient/  outbound dispatch
internal/transport/http/       inbound control boundary
```

依赖固定为 `transport -> application -> domain`、`application -> ports`、`adapters -> ports/domain`。Domain 仅依赖 Go 标准库；Application 不 import concrete adapter。

## 3. Command 事务与 tenant

Transport 从受信 `X-Kokoro-Tenant-Id` 取得 tenant，验证 service Bearer、request ID、idempotency key、path 和严格 JSON。Application 再验证 tenant、command identity、Schedule 及 recurrence。

每个 mutation 在一个 PostgreSQL transaction 内执行：

1. 对 `(tenant, scope, idempotency key)` 获取 transaction advisory lock；
2. 查 durable receipt；同 request digest 返回原 receipt，不同 digest 返回 conflict；
3. create/replace/delete/pause/resume tenant-scoped Schedule；
4. 写 receipt 后提交。

因此请求在进程重启后仍可精确 replay。POST 已存在和目标不存在也是可 replay 的持久化 command result。PUT 是完整替换，不保留旧字段兼容路径。

## 4. Due planning 与原子 outbox

每个 wakeup cycle 先运行 planner，再运行 dispatcher。启动时 Application 立即触发一次 cycle，不等待第一个 timer tick。

Planner：

1. 在短事务中按 `(next_due_at, id)` 选择 active due Schedule；
2. 使用 `FOR UPDATE SKIP LOCKED` 写 worker/expiry claim；
3. 按 Schedule 的 recurrence 与 misfire policy 生成 plan；
4. 在单一事务中检查 open occurrence、插入 Occurrence、插入 dispatch outbox、推进 `next_due_at` 并释放 claim。

`UNIQUE (tenant_id, schedule_id, scheduled_at)` 和 `UNIQUE (tenant_id, occurrence_id)` 是重复防线。若并发 update/delete 使 Schedule version/claim 失效，整个 materialization transaction 回滚。

## 5. Recurrence、timezone 与 DST

Schedule 持久化原始本地 rule 与独立 IANA timezone；API 和 occurrence 只传 RFC3339 UTC instant。

支持：

- 五字段 `minute hour day-of-month month day-of-week`；
- `*`、`,`、闭区间 `-`、step `/`；`N/step` 从 N 延续到字段最大值；
- `JAN`–`DEC`、`SUN`–`SAT`，Sunday 0/7；
- `@yearly`/`@annually`/`@monthly`/`@weekly`/`@daily`/`@midnight`/`@hourly`；
- `@every DURATION`，1 秒至 366 天、毫秒精度。

DOM/DOW：两者都受限时 OR；一方为 `*` 或等价 `*/1` 时由另一方筛选。保存前检查字段范围和 selected month 中是否存在可用日期。cron 以 UTC minute 递增并映射到 IANA location 判断，因此 DST gap 自然不产生 instant，DST fold 会产生两个不同 UTC instant。搜索 horizon 为 10 年。

`@every` 不按 wall clock 对齐；以已持久化 due instant 为 anchor，downtime 后用整数倍推进。

## 6. Misfire 与 overlap

- `skip`：记录一个 `skipped/SCHEDULER_MISFIRE_SKIPPED` occurrence，然后推进至 now 之后；
- `fire_once`：为最早 due instant 建立一个 dispatch，随后推进至 now 之后；
- `catch_up_bounded`：按时间顺序最多建立 `catch_up_limit` 个 plan；仍有 backlog 时再写一个 `SCHEDULER_MISFIRE_BOUND_EXCEEDED` skipped occurrence，并推进至 now 之后。

`overlap_policy=forbid` 会查询同 tenant/schedule 的 pending/dispatching/retrying occurrence。已有 open occurrence 时，新 plan 变为 `skipped/SCHEDULER_OVERLAP_BLOCKED`。这是 durable、可查询 SQL 的 outcome，不依赖 timer 的进程内 still-running 行为。`allow` 为每个唯一 due instant 建立独立 outbox。

## 7. Dispatch、retry 与 recovery

Dispatcher 在 PostgreSQL transaction 中：

1. 将 expired final-attempt claim 转为 outbox/occurrence failed；
2. 用 `FOR UPDATE SKIP LOCKED` claim ready 或 expired outbox；
3. 如配置 Redis，取得 tenant+occurrence 派生的短期 lease；
4. 在 transaction 内将 attempt 加一并标记 occurrence dispatching；
5. 使用 cancellable context 和 overall timeout 调 target；
6. 在 transaction 内持久化 success、retry 或 permanent failure。

可重试：HTTP 408/425/429/5xx、DNS/连接/读取网络错误、dispatch timeout。永久：其他非 2xx、target policy/invalid payload、caller cancellation。延迟为 capped exponential full jitter，并同时受 `max_attempts`、`max_backoff_seconds` 和 `max_retry_window_seconds` 限制。

投递 identity 由 tenant、schedule id/name 和 UTC scheduled instant 派生；每次重试/崩溃恢复保持相同 request/idempotency/trace identity。语义是 durable at-least-once，不是 exactly-once。

## 8. Lifecycle

- startup：配置校验 -> PostgreSQL connect/ping -> 可选 Redis DB 7 ping -> adapter 装配 -> wakeup start -> immediate cycle -> HTTP serve；
- readiness：Runtime accepting 且 PostgreSQL ping 成功；配置 Redis 时还要求 Redis ping；
- shutdown：先关闭 readiness/HTTP，取消 Runtime context，停止 gocron wakeup，并在 10 秒 deadline 内等待 in-flight cycle；
- crash：PostgreSQL claim expiry 后由其他 cycle 重领；attempt 已耗尽但未提交结果时写 `SCHEDULER_DISPATCH_RECOVERY_EXHAUSTED`。
