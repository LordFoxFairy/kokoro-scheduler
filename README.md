# kokoro-scheduler

`kokoro-scheduler` 是通用内部调度 owner。它持久化 Schedule、Occurrence、command receipt、dispatch outbox 与重试结果；不拥有 BFF `ScheduledTask`、Billing、Agent 或目标 command 的业务事实。

- 代码地图：[INDEX.md](./INDEX.md)
- 当前实现：[docs/CURRENT.md](./docs/CURRENT.md)
- 技术设计：[docs/TECHNICAL_DESIGN.md](./docs/TECHNICAL_DESIGN.md)
- 机器契约：[contract/openapi/v1/openapi.yaml](./contract/openapi/v1/openapi.yaml)
- 验收：[docs/ACCEPTANCE.md](./docs/ACCEPTANCE.md)

## 架构摘要

```text
trusted service -> HTTP transport -> application commands -> PostgreSQL
                                             |
gocron/v2 wakeup -> planner -> occurrence + dispatch outbox (one transaction)
                                -> dispatcher -> target HTTP
                                     |
                              optional Redis DB 7 lease
```

PostgreSQL 是唯一事实源。`github.com/go-co-op/gocron/v2 v2.22.0` 只按固定间隔唤醒进程内 scanner，不保存 schedule、执行历史或 receipt。Redis 可选且只用于短期协调；删除 Redis 数据不会删除 Scheduler 事实。进程启动后立即扫描 PostgreSQL，因此重启不依赖调用方重放注册意图。

生产分层：

```text
cmd/scheduler/                    组合根、探针、graceful shutdown
cmd/db-apply-schema/              空数据库 schema 安装命令
internal/domain/                  Schedule、Occurrence、状态与不变量
internal/application/             command、planning、dispatch、recovery 用例
internal/ports/                   Store、Clock、Recurrence、Wakeup、Lease、Target
internal/adapters/postgres/       durable repository 与事务 claim
internal/adapters/recurrence/     明确定义的 recurrence calculator
internal/adapters/gocron/         仅进程唤醒
internal/adapters/redis/          可选 token-fenced lease
internal/adapters/httpclient/     timeout、DNS pin、重试分类
internal/transport/http/          tenant-scoped internal control API
test/{architecture,contract,schema,integration,smoke,doubles}/
```

## 五分钟启动

要求：`go 1.26.8`、一个**空的 Scheduler 专用 PostgreSQL database**。本地多实例联调可复用共享 Redis logical DB 7；不要清理其他 logical DB。

```bash
git clone https://github.com/LordFoxFairy/kokoro-scheduler.git
cd kokoro-scheduler
go mod download

export SCHEDULER_DATABASE_URL='postgresql://USER:PASSWORD@HOST:PORT/EMPTY_SCHEDULER_DATABASE'
export SCHEDULER_INTERNAL_SERVICE_TOKEN='TOKEN'
# 可选，且 URL 必须显式选择 /7：
# export SCHEDULER_REDIS_URL='redis://HOST:PORT/7'

./scripts/db-apply-schema
go run ./cmd/scheduler
```

探针：

```bash
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

创建 schedule：

```bash
curl -fsS -X POST \
  -H 'Authorization: Bearer TOKEN' \
  -H 'X-Kokoro-Tenant-Id: tenant-a' \
  -H 'X-Request-Id: request-001' \
  -H 'Idempotency-Key: schedule-create-001' \
  -H 'Content-Type: application/json' \
  http://127.0.0.1:8080/internal/scheduler/v1/schedules/maintenance.reconcile \
  --data '{
    "schedule":"0 2 * * *",
    "timezone":"America/New_York",
    "url":"https://service.example/internal/reconcile",
    "method":"POST",
    "body":{"scope":"scheduled"},
    "misfire_policy":"fire_once",
    "overlap_policy":"forbid",
    "retry":{"max_attempts":4,"backoff_seconds":2,"max_backoff_seconds":30,"max_retry_window_seconds":300}
  }'
```

API 时间均为 RFC3339 UTC；周期规则单独保存 IANA timezone。默认 misfire 为 `fire_once`，默认 overlap 为 `forbid`，每次跳过都会持久化 outcome，不存在无记录的 still-running skip。

## Recurrence contract

支持：

- 五字段 cron：minute、hour、day-of-month、month、day-of-week；
- `*`、列表、闭区间、step、`JAN`–`DEC`、`SUN`–`SAT`，Sunday 可写 `0` 或 `7`；
- `@yearly`、`@annually`、`@monthly`、`@weekly`、`@daily`、`@midnight`、`@hourly`；
- `@every DURATION`，范围 1 秒至 366 天、毫秒精度。

DOM 与 DOW 都受限时使用标准 OR 语义；任一字段为无 step 的 `*` 时由另一字段筛选。不存在的日期在写入前拒绝。DST spring-forward 缺失的本地时刻不触发；fall-back 重复的本地时刻对应两个不同 UTC occurrence。完整语义和测试位置见 [docs/TECHNICAL_DESIGN.md](./docs/TECHNICAL_DESIGN.md)。

## 配置

| 变量 | 必需 | 默认/约束 |
|---|---:|---|
| `SCHEDULER_DATABASE_URL` | 是 | Scheduler 专用 PostgreSQL database |
| `SCHEDULER_REDIS_URL` | 否 | 配置时必须为 `redis://.../7` 或 `rediss://.../7` |
| `SCHEDULER_HTTP_ADDR` | 否 | `:8080` |
| `SCHEDULER_INTERNAL_SERVICE_TOKEN` | command 使用时是 | 空值使 command fail closed，探针仍可用 |
| `SCHEDULER_TARGET_SERVICE_TOKEN` | 否 | 非空时发送独立 outbound Bearer |
| `SCHEDULER_INTERNAL_TARGET_ALLOWLIST` | 否 | 精确 hostname 与 canonical internal CIDR JSON |
| `SCHEDULER_WAKEUP_INTERVAL` | 否 | `1s` |
| `SCHEDULER_CLAIM_TTL` | 否 | `2m`，必须大于 dispatch timeout |
| `SCHEDULER_DISPATCH_TIMEOUT` | 否 | `30s` |
| `SCHEDULER_BATCH_SIZE` | 否 | `100`，范围 1–1000 |
| `SCHEDULER_WORKER_ID` | 否 | hostname 加随机 suffix |

## 验证

真实 integration/smoke 使用独立测试 database；无 URL 时相关测试明确 SKIP。

```bash
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
go build ./...
./scripts/contract-check

env \
  SCHEDULER_DATABASE_TEST_URL='postgresql://USER:PASSWORD@HOST:PORT/kokoro_scheduler_test_RUN' \
  SCHEDULER_REDIS_TEST_URL='redis://HOST:PORT/7' \
  go test -count=1 ./test/integration ./test/smoke ./internal/adapters/redis

git diff --check
```

`./scripts/db-apply-schema` 只接受空 database namespace，只安装 [`database/schema.sql`](./database/schema.sql)，不是历史升级工具。
