# kokoro-scheduler 运行手册

## 1. 启动前

1. 准备 Scheduler 专用 PostgreSQL database；不要指向其他 owner 或共享业务 database；
2. 对全新空 namespace 运行一次 `./scripts/db-apply-schema`；
3. 注入 inbound service token、可选独立 outbound token；
4. 如启用 Redis，复用实例但 URL 必须显式 logical DB 7；
5. 校验 target allowlist、egress policy 和目标端 durable idempotency receipt；
6. 确认 `claim_ttl > dispatch_timeout`。

`db:apply-schema` 不是 upgrade/migration runner；发现任意现有 table 会退出。不要为绕过检查删除或清空未知表。

## 2. 本地启动

```bash
export SCHEDULER_DATABASE_URL='postgresql://USER:PASSWORD@HOST:PORT/kokoro_scheduler'
export SCHEDULER_INTERNAL_SERVICE_TOKEN='TOKEN'
# optional:
# export SCHEDULER_TARGET_SERVICE_TOKEN='TOKEN'
# export SCHEDULER_REDIS_URL='redis://HOST:PORT/7'

./scripts/db-apply-schema
go run ./cmd/scheduler
```

```bash
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

ready 200 代表 Runtime accepting、PostgreSQL ping成功，以及已配置 Redis 的 PING 成功；不证明 target业务健康。

## 3. 配置速查

| 变量 | 默认/要求 | 失败行为 |
|---|---|---|
| `SCHEDULER_DATABASE_URL` | 必填，绝对 postgres/postgresql URL | startup退出 |
| `SCHEDULER_REDIS_URL` | 可选，必须 `/7` | parse/PING失败 startup退出；运行期 defer |
| `SCHEDULER_HTTP_ADDR` | `:8080` | bind失败 shutdown |
| `SCHEDULER_INTERNAL_SERVICE_TOKEN` | command route所需 | 空值 command 401，probe可用 |
| `SCHEDULER_TARGET_SERVICE_TOKEN` | 可选独立 outbound Bearer | 空值不发 Authorization |
| `SCHEDULER_INTERNAL_TARGET_ALLOWLIST` | 精确 host/CIDR JSON | 非法配置 startup退出 |
| `SCHEDULER_WAKEUP_INTERVAL` | `1s` | 非正值/超过24h拒绝 |
| `SCHEDULER_CLAIM_TTL` | `2m` | 必须大于 dispatch timeout |
| `SCHEDULER_DISPATCH_TIMEOUT` | `30s` | 非正值/超过24h拒绝 |
| `SCHEDULER_BATCH_SIZE` | `100`，1–1000 | 越界拒绝 |
| `SCHEDULER_WORKER_ID` | hostname+随机 suffix | 非法值拒绝 |
| `SCHEDULER_HEALTHCHECK_URL` | healthcheck 子命令使用 | 2秒非200退出1 |

## 4. Read-only 诊断

```sql
SELECT tenant_id, name, status, next_due_at, claim_owner, claim_expires_at
FROM scheduler_schedule
WHERE status = 'active'
ORDER BY next_due_at, id
LIMIT 100;

SELECT status, count(*), min(next_attempt_at) AS oldest_ready
FROM scheduler_dispatch_outbox
GROUP BY status
ORDER BY status;

SELECT tenant_id, schedule_name, scheduled_at, status, outcome_code,
       attempt_count, last_http_status, last_error_code
FROM scheduler_occurrence
ORDER BY created_at DESC, id DESC
LIMIT 100;
```

这些查询只用于 Scheduler 自有 database。日志中按 `request_id`/`trace_id` 关联，不粘贴 token、payload、完整 credential URL。

## 5. 常见故障

### `db:apply-schema` 拒绝非空 database

确认 URL/database/search_path。若属于已有 Scheduler deployment，停止并按版本化部署决策处理；current schema流程没有 in-place migration。若属于错误 database，修正 URL。不要 drop/truncate 非本任务资源。

### ready 503

1. 查 startup/background log；
2. 用受限 credential `SELECT 1` 检查 PostgreSQL；
3. 确认四张 table 已存在且 search_path正确；
4. 配置 Redis 时只 PING DB 7 并检查网络/ACL/TLS；
5. 修复依赖后 readiness自动恢复，不手工改业务状态。

### due backlog

检查 `next_due_at`、claim expiry、worker错误、数据库 lock/latency、wakeup interval和 batch size。expired claim 会自动重领。不要直接把 `next_due_at` 改到未来掩盖 backlog；misfire policy负责恢复并保存 outcome。

### outbox stuck/target失败

- pending/retrying：检查 `next_attempt_at`、attempt/window和 target connectivity；
- dispatching 且 claim expired：下一个 cycle重领，final attempt则转 recovery exhausted；
- 408/425/429/5xx/network：由 durable retry收敛；
- 其他4xx/policy rejected：永久失败，修正新 Schedule/target contract，不改写历史；
- 结果不确定：用稳定 `Idempotency-Key` 查询 target owner receipt。

### Redis unavailable

配置 Redis 时保持 not-ready/defer；修复 DB 7 网络/ACL。需要临时单实例无 Redis运行时，必须走部署变更并确认仅一个 active worker；不清 Redis、不把它当执行历史。

### idempotency conflict

同一 tenant+method:path+key 被不同 payload复用。停止自动重试，修复 caller identity；不要删除 receipt 来接受第二个含义。

## 6. Restart 与恢复

正常重启不需要 replay Schedule：

1. 新进程连接同一 PostgreSQL；
2. Runtime startup immediate cycle 扫 due/outbox；
3. command caller可用原 identity重试并收到持久化 receipt；
4. 检查 occurrence/outbox outcome确认恢复。

worker在 HTTP 后崩溃可能产生同 identity重复投递，目标 receipt是业务结果依据。

## 7. 发布与回滚

发布前运行 [`ACCEPTANCE.md`](./ACCEPTANCE.md) 全部门禁并记录 commit。先确认目标 database 已具有与该 commit 完全一致的 current schema，再滚动源码进程；source-process smoke 是必需证据，image smoke只是发布补充。

回滚只在上一 binary 与当前 schema/contract兼容时执行：停止 rollout、恢复不可变 artifact、观察 readiness/due/outbox。当前工程不提供 schema downgrade、旧 route compatibility或双读。若 schema不兼容，必须由 Scheduler owner给出新的 clean-slate部署决策，不对共享 PG/Redis清库。

## 8. Shutdown

SIGINT/SIGTERM 将关闭 readiness，停止 HTTP 接受，取消 cycle并等待最多10秒。deadline后保留 claim；不要手工删除。下一实例按 expiry recovery，目标 owner按 idempotency receipt收敛不确定 dispatch。
