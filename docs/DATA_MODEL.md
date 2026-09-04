# kokoro-scheduler 数据模型

## 1. 数据边界

本仓没有关系数据库、业务 Schema、ORM Entity、migration 或跨仓外键。Scheduler 的数据只分为：

1. 进程内通用调度模型；
2. 可选 Redis occurrence lease；
3. 结构化日志中的瞬时 dispatch 结果。

`ScheduledTask`、tenant、账务状态、Agent Run、目标 command receipt 和业务事件属于各自事实 owner，Scheduler
只传递 opaque JSON body，不解析或持久化这些事实。

## 2. 进程内模型

### ScheduleJob

| 字段 | Go 类型 | 不变量 / 默认值 | 生命周期 |
|---|---|---|---|
| `name` | `string` | `[a-z0-9][a-z0-9._-]{0,63}`，registry 内唯一 | 进程内 |
| `schedule` | `string` | 有效标准 cron 或 `@every`；UTC 解释 | 进程内 |
| `url` | `string` | 必须有 host，scheme 为 `http`/`https`，无 userinfo/fragment | 进程内 |
| `method` | `Method` | `POST` 或 `PUT`；默认 `POST` | 进程内 |
| `body` | `json.RawMessage` | JSON object；规范化为 canonical compact JSON；默认 `{}` | 进程内 |
| `retry` | `RetryPolicy` | 见下表 | 进程内 |
| `misfire_policy` | `MisfirePolicy` | `skip` 或 `fire_once`；默认 `skip` | 进程内 |
| `paused` | `bool` | 默认 `false`；只阻止后续 occurrence | 进程内 |

### RetryPolicy

| 字段 | 范围 | 默认值 | 语义 |
|---|---:|---:|---|
| `max_attempts` | 1–10 | 1 | 包含首次调用的总 attempt 数 |
| `backoff_seconds` | 1–3600 | 1 | 第一次重试的指数 ceiling 基数 |
| `max_backoff_seconds` | 1–3600，且不小于 base | 3600 | 单次 ceiling 上限 |
| `max_retry_window_seconds` | 1–86400 | 3600 | 从首次 target attempt 前开始的总窗口 |

### Occurrence

`Occurrence` 包含 `job_name`、`scheduled_at` 和 `observed_at`。两个时间进入领域对象时立即调用
`.UTC()`；具体 occurrence identity 使用 `scheduled_at` 的 `YYYYMMDDTHHMMSSZ` 表示。它不是数据库
row，也不承诺在重启后可查询。

### RunResult

`RunResult` 是一次 dispatch 编排的瞬时结果，包含 HTTP status、error、稳定错误 code、attempt 数、
request/idempotency/trace identity 和 duration。它只交给 observer 写结构化日志，不是 durable receipt。

### Inbound mutation receipt

internal command handler 以 `method:path:idempotency_key` 为 scope，保存：

- canonical request fingerprint；
- 原始 HTTP status；
- 原始 JSON response bytes。

相同 scope 和 payload 精确 replay；payload 不同返回 `409 idempotency_conflict`。map 最多保留 10,000 条，
达到上限时淘汰一条任意旧记录；进程退出时全部消失。因此它只提供单进程重试收敛，不是业务或 durable receipt。

## 3. Redis lease record

| 属性 | 当前格式 |
|---|---|
| key | `kokoro:scheduler:run:<job-name>:<occurrence-key>` |
| value | 操作系统加密随机源生成的 128-bit token，以 32 位 hex 表示 |
| acquire | `SET key token NX PX <ttl>` 的等价 go-redis 操作 |
| 默认 TTL | 26 小时 |
| operation timeout | 2 秒 |
| renew | 每 `TTL / 3` 检查并仅由 token owner 延长 TTL |
| release | Lua compare-token-and-delete；仅失败/cancelled occurrence 主动释放 |
| success retention | 成功后保留 claim 至 TTL 到期，抑制同一 occurrence 再投递 |

标准 cron 的 `<occurrence-key>` 使用 UTC 分钟 `YYYYMMDDHHMM`；`@every <duration>` 使用
`UnixNano / duration` bucket。对外 header/idempotency identity 仍使用完整 UTC 秒格式，两者职责不同。

Redis logical DB 7 是 Kokoro 本地共享 Redis 的 Scheduler 分区约定。Redis 不保存 job registry、retry history、
目标响应或业务状态，也不能替代目标 owner 的幂等 receipt。

## 4. 状态与关系

```text
ScheduleJob: configured -> active <-> paused -> removed
Occurrence:  pending -> claimed -> running -> succeeded
                                  \-> retrying -> running
                                  \-> failed/cancelled
             pending -> skipped (paused/misfire/already-claimed/stale)
```

- 一个 registry name 对应一个当前 `Job` 和一个 cron entry；update 先建立新 entry，再移除旧 entry。
- pause/resume 修改 registry 中当前 job，不取消已 in-flight 的 occurrence。
- stale cron closure 若与 registry 当前 job 不同，会以 `SCHEDULER_STALE_TRIGGER` 结束。
- lease 只关联 job + occurrence identity，不关联 tenant 或业务资源。

## 5. Retention 与删除

- registry、cron entry 和 mutation receipt：进程生命周期；delete 只移除指定 job/entry，不删除业务数据。
- Redis 成功 claim：默认 26 小时 TTL；失败 claim 尝试立即 token-safe release。
- 日志 retention、访问控制和删除由部署日志平台拥有。
- 目标业务数据 retention 由目标事实 owner 的 `DATA_MODEL.md` 定义，本仓不复制。
