# kokoro-scheduler 验收矩阵

所有命令从仓库根目录执行。真实测试 database 名必须以 `kokoro_scheduler_test` 开头；复用 Redis 时只使用 DB 7，不执行共享实例清库。

## 1. 静态、模块与构建

```bash
gofmt -w .
test -z "$(gofmt -l .)"
go mod tidy
git diff --exit-code -- go.mod go.sum  # tidy 后确认已提交结果时使用
go mod verify
go vet ./...
go test ./...
go test -race ./...
go build ./...
git diff --check
```

固定依赖与 import：

```bash
test "$(go list -m -f '{{.Version}}' github.com/go-co-op/gocron/v2)" = 'v2.22.0'
go mod why -m github.com/go-co-op/gocron/v2
rg -n 'github.com/go-co-op/gocron/v2' --glob '*.go' cmd database internal
```

期望：旧 timer package 没有 production Go import；gocron 只在实际 `internal/adapters/gocron/wakeup.go` import。若 gocron 的 module graph仍解析旧 package，只允许 `go.mod` indirect/go.sum/module graph，不作为 Domain/Application direct dependency。

## 2. Architecture、schema 与 contract

```bash
go test -count=1 ./test/architecture ./test/schema
./scripts/contract-check

test ! -d database/migrations
! rg -ni '\bforeign[[:space:]]+key\b|\breferences[[:space:]]' database/schema.sql
! rg -n 'SkipIfStillRunning|type[[:space:]].*(InMemory|Fake|Fixture)|receipts map\[|schedules map\[|occurrences map\[' cmd database internal --glob '*.go' --glob '!**/*_test.go'
```

Contract gate 必须验证 OpenAPI parse/$ref、五个 governance extension、protected v1 signature、Schedule/Occurrence/dispatch schema、所有 control route runtime parity、concrete dispatch header parity 与 manifest SHA-256。

## 3. Fresh database schema

创建独立临时 database，不对共享业务库执行：

```bash
createdb 'kokoro_scheduler_test_SCHEMA_RUN'
export SCHEDULER_DATABASE_URL='postgresql://USER:PASSWORD@HOST:PORT/kokoro_scheduler_test_SCHEMA_RUN'
./scripts/db-apply-schema
psql "$SCHEDULER_DATABASE_URL" -Atc \
  "select count(*) from pg_catalog.pg_tables where schemaname=current_schema() and tablename like 'scheduler_%'"
# 期望 4；验收结束后：
dropdb 'kokoro_scheduler_test_SCHEMA_RUN'
```

同一 database 第二次执行 `./scripts/db-apply-schema` 必须以“requires an empty database schema”失败。

## 4. Real PostgreSQL/Redis/integration/smoke

```bash
export SCHEDULER_DATABASE_TEST_URL='postgresql://USER:PASSWORD@HOST:PORT/kokoro_scheduler_test_RUN'
export SCHEDULER_REDIS_TEST_URL='redis://HOST:PORT/7'

go test -count=1 -v ./test/integration
go test -count=1 -v ./internal/adapters/redis
go test -count=1 -v ./test/smoke
go test -race -count=1 ./...
```

必须覆盖：

| 行为 | Evidence |
|---|---|
| process restart | Schedule/receipt 从 PostgreSQL 恢复，原 response/request ID replay |
| duplicate trigger | 并发 planner 只有一个 occurrence/outbox |
| tenant isolation | 同名 Schedule 可跨 tenant；receipt/conflict不串 tenant |
| atomic planning | occurrence 与 outbox 同 transaction；Schedule version/claim 保护推进 |
| timezone/DST | spring gap、fall fold、IANA、UTC transport |
| recurrence | invalid/impossible date、leap day、list/range/step/name、Sunday 0/7、DOM-DOW |
| misfire/overlap | fire_once、skip、bounded catch-up、bound exceeded、overlap blocked |
| retry | 408/425/429/5xx/network/timeout retryable；其他 4xx/cancellation permanent |
| crash recovery | expired claim重领；final-attempt crash durable failed |
| Redis | DB 7 token-fenced acquire/renew/release，不承担事实 |

无测试 URL 时测试会显式 SKIP；SKIP 不计为真实 integration/smoke 通过。

## 5. Source process smoke

`test/smoke` 从当前源码构建二进制，使用独立 PostgreSQL schema 启动、调用 control API、SIGINT、重启并 replay receipt。应用容器构建不是该 gate 的替代。
