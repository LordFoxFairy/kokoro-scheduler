# kokoro-scheduler 可靠性

## 1. 交付语义

Scheduler 对 target 提供 **durable at-least-once dispatch**：Occurrence 与 outbox 先原子提交，随后才发 HTTP。成功响应与永久失败写回 outbox/occurrence 同一事务。网络结果不确定或 worker 崩溃时可能重投，但 tenant+schedule+scheduled UTC instant 派生的 `Idempotency-Key` 保持稳定。

目标 owner 必须持久化 request digest/receipt；Scheduler 的 receipt 只覆盖 control mutation，不证明目标业务 command 已成功。

## 2. 并发与 claim

- due Schedule 和 ready/expired outbox 分别使用 PostgreSQL `FOR UPDATE SKIP LOCKED`；
- claim 包含 worker owner 与 expiry，Application 只允许持有者推进状态；
- Schedule materialization 以 version+claim 条件完成，竞争更新会回滚整个 occurrence/outbox transaction；
- occurrence/outbox UNIQUE 约束是并发和重复 wakeup 的最终去重边界；
- optional Redis lease 是第二层协调，不替代 PostgreSQL claim/UNIQUE。

同一进程可收到重叠 wakeup；正确性由 PostgreSQL claim 和业务唯一键保证，不由 timer callback 的进程内状态保证。

## 3. Restart 与 crash recovery

启动后 Runtime 立即执行 cycle：

1. 重新扫描 `next_due_at <= now` 的 active Schedule；
2. 按持久化 misfire policy 物化 downtime；
3. 重领 expiry 已过且 attempt 未耗尽的 outbox；
4. 将 expiry 已过且 final attempt 已开始的 outbox/occurrence 标为 `SCHEDULER_DISPATCH_RECOVERY_EXHAUSTED`。

control receipt 在重建 Service/进程后仍 replay。恢复不要求 BFF 重发 Schedule 注册。

若 worker 在目标接收后、写 success 前崩溃，claim expiry 后会用相同 identity 重投；这是目标幂等边界必须处理的已知 at-least-once 窗口。

## 4. Misfire 与 overlap 可观测性

`skip`、`fire_once`、`catch_up_bounded` 的行为由 Schedule row 决定。skip、bound exceeded、overlap blocked 都写 Occurrence 终态和 outcome code。不存在 timer 级无记录跳过。

暂停期间不创建 occurrence；resume 从当前 instant 重新计算 future due。已创建 outbox 是 durable snapshot，Schedule pause/delete 不撤销已发生的 dispatch obligation。

## 5. Retry 与 timeout

| 结果 | 分类 |
|---|---|
| 2xx | succeeded |
| 408 / 425 / 429 / 5xx | retryable |
| DNS、connect、read、network error | retryable |
| per-attempt deadline exceeded | retryable timeout |
| caller/runtime cancellation | permanent cancellation |
| 其他非 2xx、redirect、target policy、invalid payload | permanent |

重试 delay 为 `uniform(0, min(base*2^(attempt-1), max_backoff, remaining_window))`。`max_attempts`、`max_retry_window_seconds` 和 next-attempt deadline 任一耗尽即终结。随机源失败会 durable fail，不进行无 jitter 重试。

配置强制 `claim_ttl > dispatch_timeout`，避免仍在合法 HTTP attempt 时被另一个 worker重领。HTTP client另有 dial/TLS/header/overall timeout、context cancellation 和 1 MiB response 上限。

## 6. Redis degradation

Redis 未配置：PostgreSQL claim/UNIQUE 提供正确性。

Redis 已配置：必须为 logical DB 7；startup/readiness PING 失败使实例 not-ready。运行期 acquire error 或 contention 会将 outbox durable defer 1 秒并记录 coordination code，不直接 dispatch、不清除事实。不得通过清 DB 7 作为恢复手段；key 自带 TTL，release 使用 token compare/delete。

## 7. Shutdown

SIGINT/SIGTERM 后：readiness 关闭，HTTP server shutdown，Runtime context cancellation，gocron shutdown，并等待 in-flight cycle，整体 deadline 10 秒。caller cancellation会写 permanent cancellation（若 transaction context仍可提交）；进程在提交前退出时由 claim expiry recovery 收敛。

## 8. 当前观测与缺口

当前实现输出 structured background/dispatch logs，包含 operation、tenant、schedule、result、attempt、code、request/trace identity 和 duration；不记录 token 或 payload。`/healthz` 与 `/readyz` 可检查 lifecycle/dependency。

尚未实现 metrics exporter、backlog dashboard、PrometheusRule、retention worker 和 operator-facing Occurrence query API。这些缺口在 [`CURRENT.md`](./CURRENT.md) 与 [`SLO.md`](./SLO.md) 保持显式，不用日志推断 exactly-once 或业务成功。
