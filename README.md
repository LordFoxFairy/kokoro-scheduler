# kokoro-scheduler

技术方案：[docs/technical-architecture.md](docs/technical-architecture.md)；文档索引：[docs/INDEX.md](docs/INDEX.md)。

通用定时任务子仓库，只负责通用调度配置与 HTTP 触发，不包含 Billing、Payment、Credit 业务逻辑。多实例模式可选连接共享 Redis，仅用于 occurrence claim；本仓 v1 不需要 PostgreSQL，因为任务注册来自部署配置，业务执行事实由目标业务仓库写入 PostgreSQL。

调度核心直接复用成熟的 [`robfig/cron/v3`](https://github.com/robfig/cron)，支持标准 cron 表达式和 `@every`。
任务由 `SCHEDULER_JOBS_JSON` 注入：

未设置、空字符串或仅包含空白字符的值均按空任务列表处理；也可以显式配置 `[]`。

```json
[{"name":"billing.reconcile","schedule":"@every 1h","url":"http://service.internal/commands/reconcile","method":"POST","body":{"tenantId":"TENANT"}}]
```

每个任务由 `name/schedule/url/method/body/retry/misfire_policy/paused` 组成；配置使用严格 JSON 解码，未知字段和非法 cron 表达式会在启动时失败。
HTTP 调用默认 30 秒超时，携带 `X-Request-Id` 和按 occurrence 稳定的 `Idempotency-Key`。网络错误、429 和 5xx 按 retry policy 重试；目标业务服务负责持久化幂等 receipt 和业务状态。单实例不需要 Redis；多实例请配置 `SCHEDULER_REDIS_URL`，用于同一 occurrence 的跨实例 claim。

完整契约见：[API_CONTRACT.md](docs/API_CONTRACT.md)。

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
SCHEDULER_REDIS_TEST_URL=redis://127.0.0.1:PORT/15 go test -run TestRedisLocker ./...
```

`Dockerfile` 从实际生产入口 `./cmd/scheduler` 构建，并以 `/kokoro-scheduler` 作为容器
`ENTRYPOINT`：

```bash
docker build -t kokoro-scheduler:local .
docker run --rm \
  -e SCHEDULER_JOBS_JSON='[]' \
  kokoro-scheduler:local
```

发布到 GHCR 的 workflow 仅响应 `v*.*.*` tag；普通 branch push 和 pull request 只运行检查，
不会发布镜像。
