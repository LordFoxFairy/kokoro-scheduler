# kokoro-scheduler

代码地图：[INDEX.md](INDEX.md)；当前状态：[docs/CURRENT.md](docs/CURRENT.md)；技术设计：
[docs/TECHNICAL_DESIGN.md](docs/TECHNICAL_DESIGN.md)；文档索引：[docs/INDEX.md](docs/INDEX.md)。

通用定时任务子仓库，只负责通用调度配置与 HTTP 触发，不包含 Billing、Payment、Credit 业务逻辑。多实例模式可选连接共享 Redis，仅用于 occurrence claim；本仓 v1 不需要 PostgreSQL，因为任务 registry 只在进程内存中维护，业务执行事实由目标业务仓库写入 PostgreSQL。

生产代码采用显式 Go 分层，组合根只有 `cmd/scheduler/main.go`：

```text
internal/domain/                   Job、Occurrence、RetryPolicy 和不变量
internal/application/              schedule、lease、retry、dispatch 编排
internal/ports/                    Clock、ScheduleEngine、LeaseStore、TargetClient
internal/adapters/cron/            cron 实现
internal/adapters/httpclient/      HTTP target client
internal/adapters/redis/            occurrence lease
internal/adapters/system/           context-aware timer、加密随机源
internal/transport/http/           internal command API
internal/config/                   环境配置
```

`domain` 不知道 HTTP、Redis 或业务仓；`application` 不直接使用具体 adapter；所有技术实现由
`cmd/scheduler` 装配。测试替身只位于 `test/doubles/`。

BFF 可通过受保护的 internal HTTP command surface 注册、更新、暂停、恢复和删除通用
`ScheduleJob`；该 surface 仍只操作 scheduler 内存中的通用任务，不创建或持久化业务
`ScheduledTask`。

机器可审查的唯一 HTTP 契约位于 `contract/openapi/v1/openapi.yaml`；`contract/README.md` 记录 owner、
version 与 provenance；`docs/API_CONTRACT.md` 只解释运行语义，不另建一份可编辑 wire source。

调度核心直接复用成熟的 [`robfig/cron/v3`](https://github.com/robfig/cron)，支持标准 cron 表达式和 `@every`。
任务由 `SCHEDULER_JOBS_JSON` 注入：

未设置、空字符串或仅包含空白字符的值均按空任务列表处理；也可以显式配置 `[]`。

```json
[
  {
    "name": "maintenance.reconcile",
    "schedule": "@every 1h",
    "url": "http://service.internal/internal/commands/reconcile",
    "method": "POST",
    "body": {"scope": "scheduled"}
  }
]
```

每个任务由 `name/schedule/url/method/body/retry/misfire_policy/paused` 组成；配置使用严格 JSON 解码，未知字段、越界 retry 值和非法 cron 表达式会在启动时失败。retry policy 使用带最大单次延迟和最大总窗口的 capped exponential backoff + full jitter；默认值为 `max_attempts=1`、`backoff_seconds=1`、`max_backoff_seconds=3600`、`max_retry_window_seconds=3600`。
HTTP 调用默认 30 秒超时，携带 `X-Kokoro-Scheduler-Job`、规范化 UTC occurrence 的
`X-Kokoro-Scheduler-Occurrence`、`X-Request-Id` 和按 occurrence 稳定的
`Idempotency-Key`。配置非空的 `SCHEDULER_TARGET_SERVICE_TOKEN` 后，scheduler 会为每次出站
dispatch 增加 `Authorization: Bearer <token>`；仅当目标服务契约不要求服务认证时才允许留空。
该 token 是 Scheduler → 目标服务凭据，与 BFF → Scheduler 使用的
`SCHEDULER_INTERNAL_SERVICE_TOKEN` 独立。网络错误、429 和 5xx 按 retry policy 重试，每次等待从
`[0, min(指数退避上限, max_backoff_seconds, 剩余总窗口)]` 独立随机取值，避免多实例同步重试；目标业务
服务负责持久化幂等 receipt 和业务状态。单实例不需要 Redis；多实例请配置
`SCHEDULER_REDIS_URL`，用于同一 occurrence 的跨实例 claim。

默认只允许解析到 global-unicast 的 target。需要访问本地 BFF 或受信内网时，部署可配置严格的
`SCHEDULER_INTERNAL_TARGET_ALLOWLIST` JSON 数组，例如
`[{"host":"service.internal","cidrs":["10.0.0.7/32"]}]`。每项必须是精确 DNS host 和 canonical
private/loopback/IPv6 ULA CIDR；配置存在时仅列出的 host/CIDR pair 可出站，未列出的私网、loopback 和
其他地址仍被拒绝。解析只执行一次并 pin 地址，仍禁止 redirect，response body 上限为 1 MiB，单次调用受
overall timeout 限制。

完整契约见：[API_CONTRACT.md](docs/API_CONTRACT.md)。

## Internal HTTP server

生产入口会同时启动 cron 和 HTTP server。默认监听 `:8080`，可通过
`SCHEDULER_HTTP_ADDR` 调整。`GET /healthz` 与 `GET /readyz` 不需要认证并使用统一
`data/meta.request_id` envelope；`/readyz` 还会检查调度器已启动，配置 Redis 时执行有超时的实时
PING。任务 command
必须使用 `Authorization: Bearer <SCHEDULER_INTERNAL_SERVICE_TOKEN>`、`X-Request-Id`
和 `Idempotency-Key`。完整路由、严格 JSON 规则和重复注册语义见
[API_CONTRACT.md](docs/API_CONTRACT.md)。

## 独立仓库使用

```bash
git clone https://github.com/LordFoxFairy/kokoro-scheduler.git
cd kokoro-scheduler
go mod download
```

本仓库不连接业务 PostgreSQL；业务执行事实由目标业务仓库持久化到其 PostgreSQL。
多实例调度只通过 `SCHEDULER_REDIS_URL` 使用 Redis occurrence lease，Redis 不承载业务事实。
本仓库不引入 MySQL 或 MongoDB。

## 本地验证

```bash
gofmt -l .
go test ./...
go test -race ./...
go vet ./...
go build -trimpath -ldflags='-s -w' -o /tmp/kokoro-scheduler ./cmd/scheduler
SCHEDULER_REDIS_TEST_URL=redis://127.0.0.1:56380/7 go test -run TestRedisLocker ./...
```

`Dockerfile` 从实际生产入口 `./cmd/scheduler` 构建，并以 `/kokoro-scheduler` 作为容器
`ENTRYPOINT`：

```bash
docker build -t kokoro-scheduler:local .
docker run --rm \
  -p 8080:8080 \
  -e SCHEDULER_JOBS_JSON='[]' \
  -e SCHEDULER_INTERNAL_SERVICE_TOKEN=TOKEN \
  kokoro-scheduler:local
```

发布到 GHCR 的 workflow 仅响应 `v*.*.*` tag；普通 branch push 和 pull request 只运行检查，
不会发布镜像。
