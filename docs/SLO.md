# kokoro-scheduler SLI / SLO

本文件定义目标和指标契约，不代表当前环境已实测达标。当前仓库尚无 metrics exporter/dashboard/alert rule；只有 structured logs 与 probes。统计窗口建议为滚动 30 天，指标不得用 tenant、target URL、request ID 或 payload 作 label。

## 服务目标

| Surface | SLI | 目标 |
|---|---|---|
| Internal control API | 排除调用方 4xx 后非 5xx 比例 | 99.9% |
| Internal control API | server latency | p95 < 100 ms，p99 < 250 ms |
| Due planning | active due Schedule 在 `next_due_at` 后 5 秒内产生 durable occurrence/outbox 或 explicit skipped outcome 的比例 | 99.9% |
| Dispatch start | ready outbox 在 `next_attempt_at` 后 5 秒内进入 attempt 的比例 | 99.9% |
| Durable recovery | expiry claim 在一个 claim TTL + 5 秒内被重领或终结的比例 | 99.9% |
| Graceful shutdown | 终止信号后 10 秒内 drain 的实例比例 | 99.9% |

`/healthz`、`/readyz` 不进入 API availability 分母。目标业务 4xx 不算 Scheduler availability 错误，但计入 permanent dispatch outcome。PostgreSQL error、worker loop error、超过 planning/dispatch delay、network timeout和持久化失败计入相应 SLI。

99.9% 在 30 天约有 43 分 49 秒错误预算。建议同时评估 1 小时 fast burn 与 6 小时 slow burn；预算耗尽时暂停非修复发布。

## 必需指标

- `scheduler_http_requests_total{route,status_class}`；
- `scheduler_http_duration_seconds{route}`；
- `scheduler_due_schedule_lag_seconds`；
- `scheduler_occurrences_total{status,outcome_code}`；
- `scheduler_outbox_ready_lag_seconds`；
- `scheduler_dispatch_attempts_total{result,code}`；
- `scheduler_dispatch_duration_seconds{result}`；
- `scheduler_claim_operations_total{kind,result}`；
- `scheduler_coordination_operations_total{operation,result}`；
- `scheduler_recovery_total{kind,result}`；
- `scheduler_shutdown_duration_seconds{result}`。

结构化日志至少保留 `service`、`operation`、`result`、`duration_ms` 和需要排障的 request/trace identity；不记录 token/payload。

## 建议告警

| 告警 | 条件 | Runbook |
|---|---|---|
| API fast burn | 1h burn rate > 14.4 | 停止 rollout，检查 PG/进程错误 |
| API slow burn | 6h burn rate > 6 | 当班调查并记录事件 |
| Due backlog | due lag p99 > 5s 持续 10m | 检查 wakeup、PG lock/CPU、batch size |
| Outbox backlog | ready lag p99 > 5s 持续 10m | 检查 target/PG/Redis 与 retry volume |
| Recovery backlog | expired claim 数持续两个 TTL 非零 | 检查 worker crash 与 recovery transaction |
| Readiness loss | deployment >20% 实例 5m not-ready | 检查 PostgreSQL；配置 Redis 时检查 DB 7 |
| Shutdown timeout | 任一实例 >10s | 检查 hanging HTTP/cycle 和 cancellation |

在 exporter、dashboard 与 alert rule 实现并通过规则测试前，这些名称和阈值只是 deployment owner 的待实现 contract。
