# BFF v1 接入说明

`kokoro-bff` 是业务统一入口，但不把 Scheduler 当作业务资源数据库，也不直连
PostgreSQL 或 Redis。BFF 的职责是接收用户请求并拥有 `ScheduledTask` 定义、权限、
幂等回执和投影；它把转换后的通用 `ScheduleJob` 通过 Scheduler internal command
surface 注册给 `kokoro-scheduler`。

## 边界

- BFF 不根据 Scheduler 的 job name 判断用户权限；IAM 产生的可信服务上下文和目标
  业务仓库自己的授权规则负责权限事实。
- Scheduler command surface 是 `POST/PUT/DELETE /internal/scheduler/v1/jobs/{name}`，
  以及用于运行控制的 `/pause` 和 `/resume`；它只管理通用 job，不拥有 BFF 的
  `ScheduledTask` CRUD 或任何业务数据库。
- BFF 到 Scheduler 的 command 请求使用 `Authorization: Bearer
  <SCHEDULER_INTERNAL_SERVICE_TOKEN>`、`X-Request-Id` 和 `Idempotency-Key`。
  Scheduler 在进程内对相同 method/path/key 和相同规范化 JSON payload 重放原响应，
  对 payload 冲突返回 `409 idempotency_conflict`。
- Scheduler 只向内部 command endpoint 发起 `POST`/`PUT` JSON 请求，并携带
  `X-Request-Id`、`Idempotency-Key` 和 `X-Kokoro-Scheduler-Job`。
- Billing、Capability、Storage 等业务仓库在自己的 PostgreSQL 中保存幂等 receipt、
  状态和业务事件；Scheduler 的 Redis lease 只抑制多实例重复触发。
- BFF 的 SSE、统一响应 envelope、公开分页和用户可见错误码仍由 BFF/业务仓库契约
  负责，Scheduler 不暴露用户会话或 OAuth 资源。

## 接入检查

1. ScheduledTask 业务定义仍由 BFF 持有，Scheduler payload 只包含通用调度字段和
   业务 command URL/body。
2. 目标 command 明确 owner、权限和 tenant scope。
3. BFF 对注册/更新/删除请求复用同一个业务 mutation idempotency key，并把
   Scheduler 的 `meta.request_id` 纳入日志关联。
4. 目标 command 使用 dispatch 的 `Idempotency-Key` 做数据库幂等处理。
5. 目标 command 对重复、超时、429、5xx 具有可恢复语义。
6. `SCHEDULER_JOBS_JSON` 和 `SCHEDULER_INTERNAL_SERVICE_TOKEN` 仅由部署注入，不提交真实
   token 或用户凭据。
7. 联调先使用 Mock command，再执行目标仓库和 Scheduler 的独立启动验收；Scheduler
   重启后由 BFF/deployment replay 仍然生效的注册请求，因为 scheduler 不持久化 registry。
