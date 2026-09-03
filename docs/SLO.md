# kokoro-scheduler SLI / SLO

本文件定义生产目标和告警契约，不代表当前环境已经达到的观测结果。统计窗口统一为滚动 30 天，
planned maintenance 不从分母中静默剔除；所有指标按 deployment、region 和结果维度聚合，禁止携带
tenant、token 或 command body。

## 服务目标

| Surface | SLI | 目标 |
|---|---|---|
| Internal command API | 非调用方错误请求中返回非 5xx 的比例 | 99.9% |
| Internal command API | server-side latency | p95 < 100 ms，p99 < 250 ms |
| Occurrence dispatch | 应触发且未 pause/misfire 的 occurrence 在调度时间后 5 秒内开始首次 attempt 的比例 | 99.9% |
| Redis coordination | 配置 Redis 时 lease acquire/renew 操作在 2 秒内成功的比例 | 99.95% |
| Graceful shutdown | 收到终止信号后 10 秒内完成 drain 的实例比例 | 99.9% |

`/healthz` 和 `/readyz` 仅作为探针，不计入 availability 分母。目标服务返回的业务 4xx 不计为
Scheduler availability 错误；网络错误、timeout、lease failure、Scheduler 5xx 和超过 dispatch-start
目标的 occurrence 计入相应 SLI。

99.9% availability 在 30 天窗口对应约 43 分 49 秒错误预算。发布必须同时观察 1 小时快速燃烧率和
6 小时持续燃烧率；预算耗尽时暂停非修复性发布。

## 必需指标与日志

- `scheduler_http_requests_total{route,status_class}` 与 `scheduler_http_duration_seconds{route}`；
- `scheduler_occurrences_total{result}`、`scheduler_dispatch_start_delay_seconds`；
- `scheduler_dispatch_duration_seconds{result}`、`scheduler_dispatch_attempts{result}`；
- `scheduler_lease_operations_total{operation,result}` 与 `scheduler_lease_duration_seconds{operation}`；
- `scheduler_shutdown_duration_seconds{result}`；
- JSON 日志字段：`service`、`operation`、`request_id`、`trace_id`、`result`、`duration_ms`。

## 告警与处置

| 告警 | 条件 | 处置 |
|---|---|---|
| Availability fast burn | 1 小时错误预算燃烧率 > 14.4 | 立即值班，停止发布，按 runbook 检查实例和依赖 |
| Availability slow burn | 6 小时错误预算燃烧率 > 6 | 当班处理并建立事件记录 |
| Dispatch delay | 10 分钟内 p99 > 5 秒 | 检查 cron backlog、CPU、下游 timeout 和 drain 状态 |
| Coordination failure | 5 分钟 lease acquire/renew 错误率 > 1% | 检查 Redis；保持 fail-closed，不绕过 claim |
| Readiness loss | 同 deployment 超过 20% 实例连续 5 分钟 not-ready | 停止 rollout，检查 Redis 与 scheduler lifecycle |
| Shutdown timeout | 任一实例超过 10 秒 | 检查 hanging dispatch/lease renewal，并阻止继续 rollout |

处置步骤、诊断命令和回滚边界见 [runbook](./runbook.md)。PrometheusRule/dashboard 由部署 owner 根据这些
稳定指标名实现并执行告警规则测试；实现缺失时不得把本文件视为已具备监控能力。
