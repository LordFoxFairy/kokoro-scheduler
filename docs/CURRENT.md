# kokoro-scheduler 当前状态

更新日期：2026-09-04。本文只描述当前仓库实现；历史报告、设计愿景和未来工作不构成完成证据。

## 当前定位

Scheduler 是 Go 编写的通用内部调度服务，唯一生产入口为 `cmd/scheduler/main.go`。它拥有
Schedule、Occurrence、Lease、Retry、Dispatch，不拥有任何目标服务的业务任务、业务状态、tenant 权限或
数据库事实。

| Surface | 当前实现 |
|---|---|
| Schedule | 从严格的 `SCHEDULER_JOBS_JSON` 或 internal command API 建立进程内 registry；使用 UTC cron |
| Occurrence | `scheduled_at` / `observed_at` 进入领域时转 UTC，并生成稳定 occurrence 与 idempotency identity |
| Lease | 单实例可不配置；多实例可用 Redis `SET NX` token lease，默认 TTL 26 小时并持续 renew |
| Retry | 仅网络错误、429、5xx 可重试；capped exponential backoff + crypto full jitter + 总窗口 |
| Dispatch | `POST` / `PUT` JSON command，30 秒总超时，携带 occurrence、request、idempotency、trace headers；target URL 校验拒绝明显特殊地址，dispatch 前单次解析并 pin 安全 DNS 地址，禁跟随 redirect，response body 上限 1 MiB |
| Control API | register、update、delete、pause、resume；Bearer service token、严格 JSON、进程内幂等 replay |
| Lifecycle | `/healthz`、`/readyz`、Redis readiness PING、SIGINT/SIGTERM 停止注册并等待 in-flight |
| Contract | `contract/openapi/v1/openapi.yaml` 是唯一 HTTP wire source，visibility 为 `internal-owner` |

## 持久化与事实边界

- 本仓没有 PostgreSQL、业务数据库、`database/schema.sql` 或 migration。
- Job registry 与 inbound idempotency receipt 都是进程内数据，重启后由 BFF 或部署编排重放仍有效的注册请求。
- Redis 只保存短生命周期 occurrence claim，不保存 Schedule、业务执行结果或目标服务 receipt。
- 目标 owner 必须以 dispatch 的 `Idempotency-Key` 在自己的持久化边界收敛重复投递。

## 当前已知限制

1. registry 和 inbound mutation receipt 不耐进程重启；当前恢复契约是外部 replay，不是本仓持久化。
2. v1 不扫描进程外历史窗口；`fire_once` 只对调用方显式提供的 missed occurrence 生效。
3. `SCHEDULER_JOBS_JSON` 和受信 command 仍可配置可路由的 global `http` / `https` host；进程内策略不替代
   部署的 hostname/CIDR allowlist、egress network policy 和目标服务认证。
4. inbound authorization 当前是一个共享 Bearer service token，没有 caller identity 或细粒度 IAM permission。
5. `docs/SLO.md` 定义指标名与目标；本仓当前只有结构化 dispatch 日志，没有内置 metrics exporter、dashboard
   或 PrometheusRule，因此 SLO 不代表已实测达标。
6. Redis 集成测试依赖显式 `SCHEDULER_REDIS_TEST_URL`；未提供真实 Redis 时该用例会跳过，不能冒充集成通过。

## 本轮治理范围

- 标准文档入口、contract provenance 和 OpenAPI operation governance metadata；
- 强化 outbound target URL、DNS resolution、redirect 与 response body 边界，不改变 Go module、数据存储或部署依赖；
- 不引入 Scheduler 业务数据库，也不复制其他仓的 `ScheduledTask` 或业务事实。

## 验证证据

2026-09-03 在 Go `1.26.8 darwin/arm64`、当前治理分支上执行；最终 commit 仍须以同一组命令重验，
不继承历史报告中的 PASS：

| Gate | 命令 | 结果 |
|---|---|---|
| Format | `gofmt -w . && test -z "$(gofmt -l .)"` | PASS |
| Vet | `go vet ./...` | PASS |
| Unit/architecture/transport | `go test ./...` | PASS；未注入 URL 的 Redis case 明确 SKIP |
| Build | `go build ./...` | PASS |
| Contract behavior | `go test ./internal/architecture ./internal/transport/http` | PASS |
| Contract governance/provenance | `docs/ACCEPTANCE.md` 中的 Root 定向检查 | PASS；OpenAPI SHA-256 与 README 一致 |
| Module integrity | `go mod verify` | PASS |
| Race | `go test -race ./...` | PASS |
| Redis integration | `SCHEDULER_REDIS_TEST_URL=redis://127.0.0.1:56380/7 go test -count=1 -v ./internal/adapters/redis` | PASS；复用既有本地 Redis DB 7 |

最终提交报告记录 branch、commit 和命令退出码；若运行环境没有共享 Redis，必须将集成结果报告为 SKIP，
不得用内存替身替代。
