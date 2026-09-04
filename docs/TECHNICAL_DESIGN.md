# kokoro-scheduler 技术设计

## 1. 定位与 owner

`kokoro-scheduler` 是 Kokoro 的通用内部定时调度服务，唯一生产入口为 `cmd/scheduler/main.go`。它只拥有：

1. **Schedule**：配置校验、进程内 job registry、UTC cron 注册、pause/resume；
2. **Occurrence**：计划/观察时间、稳定 identity 和 misfire 判断；
3. **Lease**：可选 Redis 跨实例 occurrence claim、renew 和 token-safe release；
4. **Retry**：可重试分类、attempt 上限、指数 ceiling、full jitter、总窗口；
5. **Dispatch**：向目标 owner 的 internal HTTP command 发送 opaque JSON，并归一技术结果。

Scheduler 不拥有 `ScheduledTask`、用户/tenant 授权、Billing/Payment/Credit/Agent 等业务模型、目标 command
状态、业务幂等 receipt 或业务事件。本仓没有业务数据库，也不读取任何其他 owner 的数据库。

## 2. 分层与依赖方向

```text
kokoro-scheduler/
├── cmd/scheduler/                 # 唯一组合根、signals、HTTP server
├── internal/
│   ├── domain/                    # Job、RetryPolicy、Occurrence、RunResult、不变量
│   ├── application/               # registry、misfire、lease、retry、dispatch、lifecycle
│   ├── ports/                     # Clock、Sleeper、RandomSource、Engine、Lease、Target、Observer
│   ├── adapters/
│   │   ├── cron/                  # robfig/cron/v3
│   │   ├── httpclient/            # outbound net/http
│   │   ├── redis/                 # go-redis token lease
│   │   └── system/                # context timer、crypto random
│   ├── transport/http/            # probes 与 internal job command
│   └── config/                    # environment loading
├── test/doubles/                  # 仅测试替身
├── contract/openapi/v1/           # Scheduler 自有机器 HTTP contract
└── docs/                          # 当前设计、运维与治理入口
```

```text
transport -> application -> domain
                    \----> ports <---- adapters
cmd/scheduler -> transport + application + concrete adapters
```

- Domain 不依赖 `net/http`、Redis、数据库或其他 Kokoro 业务包。
- Application 只通过 ports 使用 timer、clock、random、lease 和 target client。
- Adapters 实现技术细节，不决定业务授权或目标 command 语义。
- Transport 负责 strict JSON、service auth、request metadata、response/error mapping。
- `cmd/scheduler` 负责 concrete wiring，不承载 Schedule/Retry 规则。

架构测试检查目录、旧根实现删除、关键依赖禁入和 OpenAPI 路由存在性。

## 3. 核心模型与状态

`Job` 由 `name`、`schedule`、`url`、`method`、`body`、`retry`、`misfire_policy`、`paused` 构成。
字段级范围和 Redis record 见 [`DATA_MODEL.md`](./DATA_MODEL.md)。

Job registry 的运行状态：

```text
configured -> active <-> paused -> removed
```

Occurrence 是瞬时用例，不是 durable state machine：

```text
observed -> validate -> misfire/pause/stale check -> optional lease
         -> target attempt -> success
                           \-> retry wait -> target attempt
                           \-> terminal failure/cancel
```

所有具体时间进入领域层后统一 `.UTC()`。标准 cron 由 `robfig/cron/v3` 在 UTC location 计算；
`@every` 使用库提供的 duration schedule。一个 cron entry 使用 `SkipIfStillRunning` 防止单实例同一 entry 重叠。

## 4. 启动与装配流程

```text
config.Load
  -> strict LoadJobs(SCHEDULER_JOBS_JSON)
  -> optional Redis URL parse + 5s PING
  -> create cron/http/redis/system adapters
  -> application.NewScheduler
  -> register every configured job (all-or-exit)
  -> start HTTP listener
  -> scheduler.Start / ready=true
  -> wait for signal or listener error
```

- 未设置、空字符串或纯空白 `SCHEDULER_JOBS_JSON` 等价于 `[]`。
- 任一配置或 cron entry 注册失败会在 scheduler 启动前终止进程。
- Redis 配置后即成为 readiness/dispatch 协调依赖；启动不可达时进程退出。
- HTTP listener 和 cron 共用一个进程，但 probes 与 command API 语义分离。

## 5. Registry mutation 流程

受信服务通过以下 `internal-owner` surface 操作通用 job：

```text
POST   /internal/scheduler/v1/jobs/{name}
PUT    /internal/scheduler/v1/jobs/{name}
DELETE /internal/scheduler/v1/jobs/{name}
POST   /internal/scheduler/v1/jobs/{name}/pause
POST   /internal/scheduler/v1/jobs/{name}/resume
```

处理顺序：service token -> request ID -> route/method -> idempotency key -> body size/strict JSON -> domain
normalize/validate -> application mutation -> stable envelope -> 进程内 receipt。相同 method/path/key 与 canonical
payload replay 原 status/body；冲突 payload 返回 409。

Registry 与 receipt 都只存在当前进程。BFF 拥有业务 `ScheduledTask`，部署或 BFF 必须在 Scheduler 重启后重放
仍有效的通用注册。Scheduler 不为此创建 PostgreSQL 表或复制 BFF DTO。

## 6. Occurrence dispatch 流程

1. cron closure 读取 UTC now，并以它作为当前 `scheduled_at` / `observed_at` 调用 application；恢复器也可显式
   提供两个时间。
2. Job 再次 normalize/validate；构造稳定 request、idempotency 和 trace identity。
3. `misfire_policy=skip`、paused、stale trigger 在调用目标前结束。
4. 配置 Redis 时，以 job + occurrence key 获取 26 小时 token lease；获取错误 fail closed，未获取表示另一实例
   已 claim。
5. lease 存续期间每 `TTL / 3` renew；lost lease 取消 run context。
6. TargetClient 发送 JSON `POST`/`PUT`，可选附加独立 target service token。
7. 2xx 成功；429、5xx、网络错误在 attempt 与 window 双预算内 full-jitter retry；其他 4xx 终止。
8. 成功 claim 留到 TTL 到期；失败/cancelled claim 尝试 token-safe release。
9. Observer 写归一后的 result；不读取或持久化目标业务 body。

该流程降低重复概率但保持 at-least-once。目标 owner 必须以 occurrence `Idempotency-Key` 保存 durable receipt。

## 7. HTTP 与安全边界

### Inbound

- command surface 使用 `SCHEDULER_INTERNAL_SERVICE_TOKEN` Bearer；未配置时 fail closed；
- mutation 要求 `X-Request-Id` 和 `Idempotency-Key`；
- body 上限 1 MiB，严格拒绝未知/重复/null/trailing 字段；
- `/healthz`、`/readyz` 无认证并只返回最小状态 envelope。

### Outbound

- URL 必须是有 host、无 userinfo/fragment 的 HTTP(S)；method 只允许 POST/PUT；
- `SCHEDULER_TARGET_SERVICE_TOKEN` 非空时发送独立 Bearer；
- 固定发送 job、occurrence、request、idempotency 和 `traceparent`；
- current client 使用 30 秒 overall timeout 与 context cancellation。

目标 allowlist、egress policy、TLS 强制和 secret distribution 由部署边界负责；当前进程没有 host/CIDR allowlist。
完整风险与控制见 [`SECURITY.md`](./SECURITY.md)。

## 8. 配置 contract

| 环境变量 | 必需性 | 当前语义 |
|---|---|---|
| `SCHEDULER_JOBS_JSON` | 可选 | 严格 JSON array；空值为 `[]` |
| `SCHEDULER_REDIS_URL` | 多实例协调时必需 | occurrence lease；本地共享 Redis 使用 logical DB 7 |
| `SCHEDULER_HTTP_ADDR` | 可选 | bind address，默认 `:8080` |
| `SCHEDULER_INTERNAL_SERVICE_TOKEN` | command surface 必需 | 为空时 probes 可用、所有 command 401 |
| `SCHEDULER_TARGET_SERVICE_TOKEN` | 依目标契约 | 非空时附加到所有 outbound dispatch；与 inbound token 分离 |
| `SCHEDULER_HEALTHCHECK_URL` | 容器 healthcheck 可选 | healthcheck 子命令使用，默认 `http://127.0.0.1:8080/readyz` |

Retry 范围：`max_attempts=1..10`、`backoff_seconds=1..3600`、
`max_backoff_seconds=1..3600`、`max_retry_window_seconds=1..86400`，且 max backoff 不小于 base。
默认值分别为 `1/1/3600/3600`；显式零值不是省略值，会在 strict boundary 失败。

## 9. 存储与一致性

| 数据 | 位置 | 权威性 | 恢复 |
|---|---|---|---|
| Job registry | process memory | 当前实例运行配置 | 静态配置 + consumer replay |
| Inbound idempotency receipt | process memory，最多 10,000 | 当前实例 replay cache | 不跨重启 |
| Occurrence claim | optional Redis | 26 小时协调窗口 | TTL；失败时 token-safe release |
| Dispatch result | structured log/callback | 技术观测，不是业务事实 | 日志平台策略 |
| 业务 command/result | target owner store | 唯一业务事实 | 目标 owner runbook |

本仓没有 PostgreSQL schema。跨实例 lease 不建立业务一致性；目标服务的数据库事务与 receipt 才决定业务结果。

## 10. Shutdown、部署与观测

SIGINT/SIGTERM 或 HTTP listener failure 触发：HTTP shutdown -> `ready=false` -> cancel scheduler context ->
cron stop -> 等待 in-flight，main 使用 10 秒 shutdown deadline。context-aware retry sleep 和 target request 会响应取消；
超时后进程记录 shutdown error。

容器以 distroless nonroot 运行，内置 healthcheck 调用同一二进制的 `healthcheck` 子命令。单副本无需 Redis；
多副本必须共享 Redis 或由部署提供等价 singleton，不能在无协调时假定唯一触发。

当前结构化 dispatch 日志提供 service/operation/request/trace/result/status/attempts/code/duration。稳定指标契约和
目标见 [`SLO.md`](./SLO.md)；metrics exporter 与告警由部署 owner 尚待实现，不能用目标代替实测证据。

## 11. Contract 与验证

机器 HTTP 事实源：[`../contract/openapi/v1/openapi.yaml`](../contract/openapi/v1/openapi.yaml)。
版本、生成与 provenance：[`../contract/README.md`](../contract/README.md)。

```bash
gofmt -w .
go vet ./...
go test ./...
go build ./...
go test ./internal/architecture ./internal/transport/http
```

更完整的场景矩阵见 [`ACCEPTANCE.md`](./ACCEPTANCE.md)，故障处置见 [`RUNBOOK.md`](./RUNBOOK.md)。
