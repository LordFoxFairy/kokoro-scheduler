# kokoro-scheduler 技术方案

## 1. 定位

`kokoro-scheduler` 是 Kokoro 的独立通用定时任务服务。它只负责：

1. 读取部署注入的任务配置；
2. 使用成熟的 Go cron 库计算触发时间；
3. 通过受 service token 保护的 internal HTTP command surface 接收通用
   `ScheduleJob` 注册/更新/删除和 pause/resume；
4. 通过 HTTP 调用业务服务公开的内部 command；
5. 按通用 retry/backoff、pause/resume 和 misfire policy 控制触发，并记录本次 dispatch 的成功或失败；
6. 在进程停止时优雅退出。

业务状态、重试幂等和执行结果的最终事实由被调用的业务子仓库拥有。

## 2. owns / doesNotOwn

### owns

- `SCHEDULER_JOBS_JSON` 配置解析与启动校验；
- cron 表达式注册和 UTC 触发；
- 进程内通用 `ScheduleJob` registry，以及 BFF command 的重复注册/更新/删除语义；
- internal service token、严格 JSON、request ID 和 mutation idempotency 边界；
- 单实例任务运行控制；同一任务仍在执行时跳过下一次触发；
- HTTP method、URL、JSON body、超时和基础结果日志；
- scheduler 进程生命周期。

### doesNotOwn

- Billing、Payment、Credit、Session、Agent 业务逻辑；
- MySQL、MongoDB、Redis 业务数据连接和任何业务 schema；Redis 仅可作为显式启用的调度协调传输；
- 业务任务状态、业务重试、幂等 receipt、账务事实；调度 lease 只由可选 Redis coordination 实现；
- 任务后台管理 UI、持久化任务注册表、业务 `ScheduledTask` CRUD 或多租户授权。

## 3. 目录与唯一生产入口

```text
kokoro-scheduler/
├── cmd/scheduler/main.go   # 唯一生产入口
├── scheduler.go            # 配置、HTTP runner、cron service
├── internal_http.go        # BFF internal command and health/readiness adapter
├── scheduler_test.go       # scheduler unit/contract tests
├── internal_http_test.go   # internal HTTP contract tests
├── Dockerfile
└── docs/
```

服务通过 `go build ./cmd/scheduler` 构建，容器入口为 `/kokoro-scheduler`。

## 4. 依赖

| 依赖 | 结论 |
|---|---|
| MySQL | 不依赖 |
| MongoDB | 不依赖 |
| Redis | 单副本不依赖；多副本必须配置，仅用于跨实例 occurrence claim |
| 外部库 | `github.com/robfig/cron/v3` |
| HTTP | Go 标准库 `net/http` |

选择 `robfig/cron/v3` 而非重新实现时间轮或 interval loop。它提供标准 cron parser、`@every`、location 和 job wrapper；本仓库只封装通用 HTTP dispatch 与可选的 Redis occurrence claim。

## 5. 配置 contract

环境变量：

- `SCHEDULER_JOBS_JSON`：必需时为 JSON 数组；未设置、空字符串或仅空白值表示零任务；
- `SCHEDULER_REDIS_URL`：可选；单副本留空，多副本必须配置。配置后启动时会 Ping Redis，连接失败则进程退出；
- `SCHEDULER_HTTP_ADDR`：可选；HTTP server bind address，默认 `:8080`；
- `SCHEDULER_INTERNAL_SERVICE_TOKEN`：internal job command 的 service token。未设置时进程仍可启动，
  但 command route 全部拒绝认证；生产环境必须注入；
- `SCHEDULER_TARGET_SERVICE_TOKEN`：可选的 Scheduler → 目标服务凭据。非空时每次 HTTP dispatch
  携带 `Authorization: Bearer <token>`；留空时不发送该 header，以保持现有 fixture 兼容。该
  token 与 `SCHEDULER_INTERNAL_SERVICE_TOKEN` 独立，禁止写入日志；
- 每个 job 必须包含 `name`、`schedule`、`url`；
- `method` 只允许 `POST` 或 `PUT`，默认 `POST`；
- `body` 默认为 `{}`；
- `name` 必须是稳定的 `[a-z0-9][a-z0-9._-]{0,63}`，同一配置中不可重复；
- 未知字段、空字段、非法 cron 表达式在启动阶段失败；
- 调度时区固定为 UTC；
- HTTP 超时固定为 30 秒。
- 使用 Redis 时，以 `kokoro:scheduler:run:<job>:<occurrence>` 做 `SET NX` claim，TTL 为 26 小时；token 校验后释放锁。

示例：

```json
[
  {
    "name": "billing.reconcile",
    "schedule": "0 * * * *",
    "url": "http://service.internal/commands/reconcile",
    "method": "POST",
    "body": {"tenantId": "TENANT"}
  }
]
```

URL 和 body 由部署环境负责注入，不在 scheduler 中硬编码任何业务 endpoint。

## 6. 运行与可靠性

- 启动顺序：解析 JSON → 注册全部 cron job → 任一 job 非法则进程退出 → 启动 cron 和 HTTP server；
- 每个任务使用 `SkipIfStillRunning`，避免单实例同一任务重叠执行；
- 单副本不需要 Redis；多副本必须配置 `SCHEDULER_REDIS_URL`，各实例仍使用同一 UTC occurrence key 竞争 claim；
- Redis claim 只解决 scheduler 实例间的重复触发窗口，不是业务正确性存储；
- 任务调用采用 at-least-once 触发语义：进程重启或网络失败可能导致业务端再次收到 command；
- 业务 endpoint 必须使用自己的 Idempotency-Key/command receipt 保证重复调用无副作用；
- 如果配置了 `SCHEDULER_TARGET_SERVICE_TOKEN`，目标服务必须在自己的边界校验该 Bearer
  凭据；scheduler 不解析目标服务的业务身份或授权；
- 2xx 为成功，其余 HTTP 状态码和网络错误为失败；
- scheduler 只对网络错误、429 和 5xx 按 job retry policy 重试；不写业务状态、不把失败转换为成功；
- `Pause(name)` / `Resume(name)` 控制后续 occurrence；已进入 running 的 dispatch 不被强行取消；
- internal command registry 和 idempotency receipt 只在当前进程内存中存在；scheduler 重启后由 BFF 或部署编排重放注册，
  不把它们扩展为业务持久化；
- `misfire_policy=skip` 丢弃显式恢复触发中已经错过的 occurrence，`fire_once` 对显式恢复触发最多补发一次；配置驱动的 v1 不追溯进程外的历史窗口，持久化注册表接入后由恢复器提供 `scheduledAt/observedAt`；
- 任务失败只影响本次触发，不终止调度进程；
- SIGTERM/SIGINT 调用 cron Stop，等待已开始的 job 自然结束。

## 7. 部署模型

`docker-compose.app.yml` 和 Kubernetes 均部署一个独立的 `kokoro-scheduler` 服务。Billing API、Payment worker 等是其他 process role，不属于 scheduler。

默认配置为空数组；只有部署环境明确设置 `SCHEDULER_JOBS_JSON` 才会启用任务。生产部署必须：

1. 单副本可直接运行且不需要 Redis；多副本必须使用同一个 Redis coordination URL，或改用等价的外部 leader/singleton 机制；
2. 将任务目标限制为内部网络地址；
3. 由业务服务验证 service identity、tenant context 与 idempotency；
4. 监控启动失败、Redis coordination failure、任务失败和任务目标的健康状态；

### Redis 是否必需

不是业务必需依赖：单实例部署不需要 Redis。多实例部署如果没有 Kubernetes Lease、Consul、数据库锁等其他唯一协调机制，就必须配置 Redis；否则每个实例都会独立触发 cron，可能重复调用业务 command。

无论是否使用 Redis，被调用的 Billing command 都必须保持幂等。Redis 故障时启用了 coordination 的 scheduler 直接退出或跳过本次 claim，不在失去唯一性保证时继续执行。

Scheduler 的健康与业务 command 的健康分离；scheduler 不以访问数据库作为 readiness 条件。

## 8. 验证证据

```bash
go test ./...
go vet ./...
go build ./cmd/scheduler
```

测试覆盖：严格配置和 command JSON 解析、未知字段拒绝、HTTP method/body、service token、request ID/idempotency
headers、重复注册冲突与 replay、register/update/delete/pause/resume、retry policy、pause/resume、30 秒 client
timeout、job header、2xx/非 2xx 结果及 cron service 注册。
