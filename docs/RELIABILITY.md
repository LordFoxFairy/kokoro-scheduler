# kokoro-scheduler 可靠性设计

## 1. 交付语义

Scheduler 提供 **at-least-once command delivery**，不提供 exactly-once 业务执行。Redis lease 在 26 小时窗口内
抑制同一 occurrence 的多实例重复触发，但进程崩溃、网络结果不确定、lease 丢失或窗口到期仍可能重复投递。
目标事实 owner 必须使用稳定 `Idempotency-Key` 保存 durable receipt，并以自己的事务确定业务成功。

Scheduler 不持久化 job registry、mutation receipt 或 dispatch history。服务重启后，部署配置恢复静态 job，
BFF/部署编排重放动态注册；本仓不从业务数据库重建状态。

## 2. Occurrence 与 misfire

- 所有具体时间转 UTC；cron engine 以 UTC 运行。
- 标准 cron 触发时 `scheduled_at == observed_at == now`；显式恢复调用可分别提供计划和观察时间。
- `misfire_policy=skip` 丢弃 `observed_at > scheduled_at` 的 occurrence。
- `misfire_policy=fire_once` 允许该显式 occurrence 继续，但 v1 没有历史窗口扫描器。
- paused job、stale cron closure 和已被另一实例 claim 的 occurrence 均不调用目标。

## 3. Lease 与多实例

1. 未配置 `SCHEDULER_REDIS_URL` 时按单实例运行，不做跨实例去重。
2. 配置 Redis 时，启动先在 5 秒 deadline 内 PING；失败则进程退出。
3. dispatch 前以 2 秒 operation timeout 获取 token lease；错误 fail closed，未获取表示其他实例已拥有。
4. in-flight 时每 `TTL / 3` token-safe renew；renew 失败取消 dispatch，并归类
   `SCHEDULER_COORDINATION_UNAVAILABLE`。
5. 成功 dispatch 保留 claim 至 TTL 到期；失败/cancelled dispatch 尝试在独立 2 秒 context 中 release。

Redis lease 是并发协调，不是执行日志。即使 lease 正常，目标 endpoint 也必须幂等。

## 4. Retry 与 timeout

| 结果 | 是否由 Scheduler 重试 |
|---|---|
| HTTP 2xx | 否，成功 |
| HTTP 4xx（除 429） | 否，立即失败 |
| HTTP 429 | 是，预算允许时 |
| HTTP 5xx | 是，预算允许时 |
| 网络错误 / timeout | 是，预算允许时 |
| jitter source、context、lease 错误 | 否，归一失败并停止本次 occurrence |

失败 attempt `n` 的等待 ceiling 为：

```text
min(backoff_seconds * 2^(n-1), max_backoff_seconds, retry_window_remaining)
```

实际等待由操作系统加密随机源在 `[0, ceiling]` 取 full jitter。只有 attempt 数和总窗口都剩余时才开始下一次；
已开始的 attempt 仍受 30 秒 dispatch timeout 和 process cancellation 约束。Clock、Sleeper、RandomSource 均经
port 注入，单元测试可确定性验证边界。

当前 HTTP client 只有 30 秒 overall timeout；connect、TLS handshake、response header 尚未拆分预算，登记为
当前限制而非已完成能力。

## 5. Lifecycle、探针与恢复

- 启动：严格解析全部 jobs -> 初始化/探测 Redis（若配置）-> 注册 cron -> 启动 HTTP -> Start scheduler。
- `/healthz`：只证明进程 listener 可响应。
- `/readyz`：要求 scheduler 已 Start；配置 Redis 时在 2 秒内实时 PING。
- shutdown：先将 readiness 置 false、取消 scheduler context、停止 cron，再等待 in-flight；main 的 HTTP 和
  scheduler shutdown 共使用 10 秒 deadline。
- job 失败只结束本次 occurrence，不停止整个 scheduler。
- registry/receipt 重建依赖静态配置和外部 replay；rollback 不删除任何业务数据，因为本仓没有业务数据库。

## 6. 风险登记

| 风险 | 影响 | 当前控制 | 剩余状态 / owner |
|---|---|---|---|
| 进程重启丢失动态 registry 与 receipt | job 暂停触发或 mutation 无法 replay | 静态配置重载；BFF/部署 replay | 已知；部署/consumer 必须保存注册意图 |
| v1 不扫描历史窗口 | downtime 内 occurrence 可能跳过 | 显式 misfire policy；不虚构恢复 | 已知；调用方决定是否显式补发 |
| Redis lease 过期、清空或不可用 | 重复或停止 dispatch | renew、token fencing、fail closed、目标幂等 | 剩余重复风险由目标 owner receipt 收敛 |
| 目标 command 非幂等 | retry/不确定结果造成重复副作用 | 稳定 `Idempotency-Key` 契约 | 接入阻断项；目标 owner 负责 durable receipt |
| 多实例同步重试 | 下游流量尖峰 | capped exponential backoff + crypto full jitter | 已控制，仍需下游 capacity/rate limit |
| 动态 URL 指向非内部地址 | 数据/credential 边界扩大 | scheme/host/userinfo 校验、部署 egress policy | 进程内 allowlist 缺失；部署/security owner |
| shared token 泄漏 | 未授权 registry 变更或 target 调用 | secret 注入、分离 inbound/outbound token、日志 redaction | 需要轮换与 secret manager；部署 owner |
| 可观测性未落 metrics exporter | SLO 无法直接计算 | 稳定日志字段与 SLO 指标契约 | 未实现；部署 observability owner |

## 7. 可观测性与错误预算

dispatch 日志至少包含 `service`、`operation`、`request_id`、`trace_id`、`result`、`status`、`attempts`、
`code`、`duration_ms`，且不记录 token/body。指标名、30 天目标和 burn-rate 告警见 [`SLO.md`](./SLO.md)；
实际 exporter/dashboard/alert rule 未落地前，不将目标写成实测结果。

诊断、重放和回滚步骤见 [`RUNBOOK.md`](./RUNBOOK.md)，可执行场景见
[`ACCEPTANCE.md`](./ACCEPTANCE.md)。
