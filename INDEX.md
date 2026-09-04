# kokoro-scheduler 代码与边界索引

## Owner 边界

`kokoro-scheduler` 是通用内部调度事实 owner，拥有：

1. tenant-scoped `Schedule` 定义、状态、版本和下一次 UTC due instant；
2. `Occurrence` 的计划时间、观察时间、misfire/overlap outcome 和执行状态；
3. internal command 的 durable idempotency receipt；
4. dispatch outbox、attempt、重试时间、claim 和终态；
5. 可选 Redis DB 7 coordination lease。

本仓不拥有 BFF `ScheduledTask`、IAM 授权事实、目标 command 的业务结果或其他 owner 的数据库。目标服务必须按 Scheduler 发送的稳定 `Idempotency-Key` 收敛 at-least-once 投递。

## 规范入口

| 路径 | 职责 |
|---|---|
| [`README.md`](./README.md) | 五分钟启动、配置和验证摘要 |
| [`docs/INDEX.md`](./docs/INDEX.md) | 文档阅读顺序 |
| [`docs/CURRENT.md`](./docs/CURRENT.md) | 当前实现、证据边界和已知风险 |
| [`contract/openapi/v1/openapi.yaml`](./contract/openapi/v1/openapi.yaml) | canonical HTTP/dispatch machine contract |
| [`contract/README.md`](./contract/README.md) | owner、版本、lint/parity/breaking/provenance 流程 |
| [`database/schema.sql`](./database/schema.sql) | 唯一 canonical PostgreSQL schema |

## 代码地图

| 路径 | Owner / 内容 | 依赖边界 |
|---|---|---|
| `cmd/scheduler/` | 生产组合根、探针、signal 与 graceful shutdown | 装配全部 concrete adapter |
| `cmd/db-apply-schema/` | 仅向空 database namespace 安装 canonical schema | PostgreSQL + embedded schema |
| `database/` | 当前 schema 及其 Go embed | 不保存 migration |
| `internal/domain/` | Schedule、Occurrence、Receipt、Dispatch 状态与纯规则 | 仅 Go 标准库 |
| `internal/application/` | command 事务、due planning、misfire、overlap、retry、recovery | `domain`、`ports` |
| `internal/ports/` | Store/TxStore、Clock、Recurrence、Wakeup、Lease、Target | 技术中立 |
| `internal/adapters/postgres/` | repository、事务 claim、outbox 与 schema bootstrap | pgx |
| `internal/adapters/recurrence/` | 五字段 cron/`@every` 与 IANA timezone 计算 | Go time/tzdata |
| `internal/adapters/gocron/` | 固定周期进程内唤醒；不保存业务事实 | gocron/v2 |
| `internal/adapters/httpclient/` | 目标 dispatch、timeout、DNS pin 与错误分类 | net/http |
| `internal/adapters/redis/` | 可选 DB 7 token-fenced lease | go-redis |
| `internal/transport/http/` | 受信 internal control API 与响应映射 | application |
| `internal/config/` | 环境配置和边界校验 | domain 配置规则 |
| `test/` | architecture、schema、contract、integration、smoke、doubles | 仅测试使用 |

依赖方向：

```text
transport -> application -> domain
                  |       -> ports <- adapters
cmd/scheduler -> application + transport + concrete adapters
```

Domain/Application 不 import timer、PostgreSQL、Redis 或 HTTP 实现。`github.com/go-co-op/gocron/v2 v2.22.0` 只允许出现在 `internal/adapters/gocron` 的生产 import 中。

## 修改路由

| 变更 | 首要位置 | 必须同步 |
|---|---|---|
| HTTP path/header/body/response | `contract/openapi/v1/openapi.yaml` | transport、contract tests、manifest digest、API 文档 |
| Schedule/Occurrence 状态或不变量 | `internal/domain/` | schema CHECK、application、contract/data model tests |
| command/due/retry/recovery | `internal/application/` | PostgreSQL adapter、integration/reliability tests |
| SQL、索引或 claim | `database/schema.sql`、`internal/adapters/postgres/` | schema/integration gate、DATA_MODEL |
| recurrence/DST 语义 | `internal/adapters/recurrence/` | contract、unit tests、TECHNICAL_DESIGN |
| 唤醒/Redis/target HTTP | 对应 `internal/adapters/` | config、integration、SECURITY/RUNBOOK |
| 启动配置/探针 | `cmd/scheduler/`、`internal/config/` | README、smoke、CI |

## 验证入口

```bash
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
go build ./...
./scripts/contract-check
go test ./test/architecture ./test/schema
```

真实 PostgreSQL/Redis/source-process gate 见 [`docs/ACCEPTANCE.md`](./docs/ACCEPTANCE.md)。
