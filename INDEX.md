# kokoro-scheduler 代码与边界索引

## Owner 边界

`kokoro-scheduler` 只拥有通用调度能力：

1. **Schedule**：校验通用 `ScheduleJob`，维护进程内 registry，并注册 UTC cron；
2. **Occurrence**：规范化计划时间与观察时间，生成稳定 occurrence、request、idempotency 和 trace identity；
3. **Lease**：在多实例模式下使用可选 Redis lease 协调同一 occurrence；
4. **Retry**：执行次数、指数退避上限、full jitter 和总重试窗口；
5. **Dispatch**：向事实 owner 的内部 HTTP command endpoint 投递 JSON command 并分类结果。

本仓不拥有 `ScheduledTask`、Billing、Payment、Credit、Agent、tenant 权限或目标 command 的业务事实，
不创建业务数据库、跨仓 Schema、业务 receipt 或用户 API。目标 owner 自己持久化业务状态和幂等结果。

## 入口

| 路径 | 职责 |
|---|---|
| [`README.md`](./README.md) | 五分钟启动、配置和验证摘要 |
| [`cmd/scheduler/main.go`](./cmd/scheduler/main.go) | 唯一生产组合根与进程入口 |
| [`contract/openapi/v1/openapi.yaml`](./contract/openapi/v1/openapi.yaml) | Scheduler HTTP wire contract 唯一事实源 |
| [`contract/README.md`](./contract/README.md) | contract owner、可见性、版本、生成、破坏性变更和来源 |
| [`docs/INDEX.md`](./docs/INDEX.md) | 规范文档阅读顺序 |
| [`docs/CURRENT.md`](./docs/CURRENT.md) | 当前实现、缺口和验证入口 |

## 代码地图

| 路径 | Owner / 内容 | 允许依赖 |
|---|---|---|
| `internal/domain/` | `Job`、`RetryPolicy`、`Occurrence`、`RunResult` 与不变量 | Go 标准库、纯解析库；不依赖 transport/adapter |
| `internal/application/` | register/update/remove、pause/resume、lease/retry/dispatch 编排 | `domain`、`ports` |
| `internal/ports/` | Clock、Sleeper、RandomSource、ScheduleEngine、LeaseStore、TargetClient、Observer | `domain` |
| `internal/adapters/cron/` | `robfig/cron` timer adapter | `ports` |
| `internal/adapters/httpclient/` | 目标 command HTTP client | `domain` |
| `internal/adapters/redis/` | token-fenced occurrence lease | `ports`、go-redis |
| `internal/adapters/system/` | context-aware timer 和加密随机源 | `ports` 语义 |
| `internal/transport/http/` | health/readiness 与 internal job command API | `application`、`domain` |
| `internal/config/` | 环境配置装载 | `application`、`domain` |
| `test/doubles/` | 测试替身 | 仅测试引用 |

依赖方向固定为：

```text
transport -> application -> domain
                    \----> ports <---- adapters
cmd/scheduler -> transport + application + concrete adapters
```

`cmd/scheduler` 是唯一装配 concrete adapter 的位置。生产代码不得 import `test/doubles`，Domain 不得认识
HTTP、Redis、数据库或其他 Kokoro 业务包。

## 修改路由

| 变更 | 首要位置 | 必须同步 |
|---|---|---|
| HTTP path、header、schema、错误 envelope | `contract/openapi/v1/openapi.yaml` | transport contract tests、`docs/API_CONTRACT.md`、`contract/README.md` |
| Schedule/Retry/Occurrence 不变量 | `internal/domain/` | application tests、`docs/DATA_MODEL.md` |
| 调度、lease、retry、shutdown 行为 | `internal/application/` | adapter/fixture tests、`docs/RELIABILITY.md` |
| Redis 或目标 HTTP 技术行为 | `internal/adapters/` | integration tests、`docs/SECURITY.md`、runbook |
| 启动、环境变量、探针 | `cmd/scheduler/`、`internal/config/` | README、`docs/RUNBOOK.md`、smoke/CI |

## 验证

```bash
gofmt -w .
go vet ./...
go test ./...
go build ./...
go test ./internal/architecture ./internal/transport/http
```

Redis adapter 的真实集成测试需复用本地共享 Redis logical DB 7，并显式设置
`SCHEDULER_REDIS_TEST_URL`；未设置时该测试按设计跳过。
