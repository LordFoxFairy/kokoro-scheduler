# kokoro-scheduler 运行手册

## 1. 启动前检查

1. 确认使用 `go.mod` 固定的 Go 版本；
2. 确认目标 URL 是受控内部 endpoint，目标 command 支持稳定 `Idempotency-Key`；
3. 单实例不配置 Redis；多实例复用已有共享 Redis，Kokoro 本地约定 logical DB 7；
4. 从 secret manager 注入 inbound/outbound token，不写入仓库、命令历史或日志；
5. 保存当前 `SCHEDULER_JOBS_JSON` 和动态注册意图，供重启 replay 与回滚。

本仓没有 PostgreSQL 或业务 Schema，不运行数据库安装/迁移。

## 2. 本地启动

```bash
export SCHEDULER_JOBS_JSON='[]'
export SCHEDULER_INTERNAL_SERVICE_TOKEN=TOKEN
# 仅当目标契约要求 Scheduler Bearer 时设置：
export SCHEDULER_TARGET_SERVICE_TOKEN=TOKEN
# 多实例/Redis 联调时复用现有服务：
# export SCHEDULER_REDIS_URL=redis://127.0.0.1:56380/7

go run ./cmd/scheduler
```

默认 listener 为 `:8080`，可用 `SCHEDULER_HTTP_ADDR` 修改。容器 healthcheck 调用
`/kokoro-scheduler healthcheck`，其 URL 由 `SCHEDULER_HEALTHCHECK_URL` 控制，默认
`http://127.0.0.1:8080/readyz`。

## 3. 配置速查

| 变量 | 说明 | 失败行为 |
|---|---|---|
| `SCHEDULER_JOBS_JSON` | 严格 JSON array；unset/空白为 `[]` | 任一 job 非法则启动退出 |
| `SCHEDULER_REDIS_URL` | 可选 occurrence coordination | URL/PING 失败则启动退出；运行期失败 fail closed |
| `SCHEDULER_HTTP_ADDR` | listener，默认 `:8080` | bind 失败触发 shutdown |
| `SCHEDULER_INTERNAL_SERVICE_TOKEN` | inbound job command Bearer | 为空时所有 command 401，探针仍可用 |
| `SCHEDULER_TARGET_SERVICE_TOKEN` | outbound target Bearer | 为空时不发送 Authorization |
| `SCHEDULER_HEALTHCHECK_URL` | healthcheck 子命令的 readiness URL | 2 秒内非 200 则 healthcheck 失败 |

完整字段和 retry 边界见 [`API_CONTRACT.md`](./API_CONTRACT.md)。

## 4. 探针与快速诊断

```bash
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

- health 200、ready 503：scheduler 未 Start，或已配置 Redis 的实时 2 秒 PING 失败；
- 两者都不可达：检查进程、listener bind、容器状态和最近启动日志；
- health/ready 都 200 只证明 Scheduler 自身可用，不证明任何目标业务 command 健康。

启动日志按顺序检查：配置解析、Redis URL/PING、job register、HTTP bind、`scheduler started`。禁止在工单或
日志中粘贴 token、完整 Redis URL 或 job body。

## 5. 常见故障

### 配置拒绝

症状：进程在 `scheduler startup failed` 或 `register scheduler job failed` 后退出。

处置：

1. 将 `SCHEDULER_JOBS_JSON` 作为单个 JSON array 校验；
2. 检查重复/未知/null 字段、重复 name、cron、URL、method 和 retry 范围；
3. 确认 `max_backoff_seconds >= backoff_seconds`；
4. 使用空数组启动隔离 runtime，再逐条恢复受审 job；
5. 不通过放宽 parser 或跳过错误启动来恢复。

### Internal command 返回 401/400/409

- `service_auth_failed`：检查 caller 与 Scheduler 的 inbound token 是否同步；
- `request_id_required` / `request_id_invalid`：修复 canonical `X-Request-Id`；
- `idempotency_key_required` / `invalid_idempotency_key`：修复 mutation identity；
- `idempotency_conflict`：同一 method/path/key 被不同 payload 使用；停止重试并调查 caller；
- `job_already_exists`：POST 不可作为 update，使用新的 key 和 PUT；
- `job_not_found`：核对重启后 replay 是否完成。

### Redis coordination unavailable

1. 确认所有副本使用同一 Redis 与正确 logical DB；
2. 检查网络、ACL/TLS、credential、latency 和 Redis health；
3. 保持 fail closed，不临时绕过 lease 继续多副本 dispatch；
4. 需要恢复时可缩为一个副本并按变更流程移除 Redis 配置；
5. 恢复多副本前确认目标 owner 的 durable idempotency receipt 正常。

### Target dispatch 失败

- 429、5xx、网络错误：查看 `attempts`、`code`、`duration_ms` 和 retry window；
- 其他 4xx：Scheduler 不重试，交给目标 owner 修正认证/validation/业务拒绝；
- `SCHEDULER_TARGET_TIMEOUT`：目标调用超过 30 秒 overall timeout；
- `SCHEDULER_RETRY_JITTER_UNAVAILABLE`：随机源失败，本次 occurrence 停止重试；
- 结果不确定：使用稳定 `Idempotency-Key` 到目标 owner 的 receipt 查询，不从 Scheduler 日志推断业务成功。

## 6. 重启与注册 replay

Registry 和 inbound receipt 都是进程内数据。每次重启后：

1. 静态 `SCHEDULER_JOBS_JSON` 在启动阶段重新注册；
2. BFF/部署编排按保存的业务意图重新发送仍有效的 register/update/delete/pause/resume mutation；
3. replay 使用原 mutation idempotency identity；跨进程不能依赖 Scheduler 旧 receipt；
4. 通过目标 owner 的状态和后续 occurrence 验证，不创建 Scheduler 业务数据库补偿。

`misfire_policy=fire_once` 不会自动扫描 downtime 历史；是否补发必须由拥有调度意图的 caller 显式决定。

## 7. 发布与回滚

### 发布

1. 运行 [`ACCEPTANCE.md`](./ACCEPTANCE.md) 的全部门禁；
2. 保存旧 image digest、环境配置与动态注册意图；
3. 先部署一个实例，等待 `/readyz` 200；
4. replay 动态 registry，使用 mock/低风险 command 验证 headers 和 receipt；
5. 多副本 rollout 时确认共享 lease，并观察 coordination/dispatch 错误；
6. 只有目标 owner 的 receipt 能证明业务执行结果。

### 回滚

1. 停止继续 rollout；
2. 恢复上一不可变 image digest 和上一份配置；
3. 等待 readiness 后 replay 上一版本有效 registry；
4. 检查目标 owner receipt 以收敛 rollout 窗口内的重复/不确定 dispatch；
5. Scheduler 无业务数据库，因此不执行数据 downgrade、跨仓 SQL 或业务记录删除。

## 8. Shutdown

SIGTERM/SIGINT 会停止 readiness、取消 scheduler context、停止 cron 并等待 in-flight，main 的 shutdown deadline
为 10 秒。超过 deadline 时保留日志与目标 request/idempotency identity，按目标 owner receipt 调查；不要通过
手工删除 Redis claim 来假定业务未执行。
