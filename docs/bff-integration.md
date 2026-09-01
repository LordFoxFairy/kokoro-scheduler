# BFF v1 接入说明

`kokoro-bff` 是业务统一入口，但不把 Scheduler 当作业务资源数据库，也不直连
PostgreSQL 或 Redis。BFF 的职责是接收用户请求并调用对应业务仓库；业务仓库把需要
周期执行的 command 以部署配置注册给 `kokoro-scheduler`。

## 边界

- BFF 不根据 Scheduler 的 job name 判断用户权限；IAM 产生的可信服务上下文和目标
  业务仓库自己的授权规则负责权限事实。
- Scheduler 只向内部 command endpoint 发起 `POST`/`PUT` JSON 请求，并携带
  `X-Request-Id`、`Idempotency-Key` 和 `X-Kokoro-Scheduler-Job`。
- Billing、Capability、Storage 等业务仓库在自己的 PostgreSQL 中保存幂等 receipt、
  状态和业务事件；Scheduler 的 Redis lease 只抑制多实例重复触发。
- BFF 的 SSE、统一响应 envelope、公开分页和用户可见错误码仍由 BFF/业务仓库契约
  负责，Scheduler 不暴露用户会话或 OAuth 资源。

## 接入检查

1. 目标 command 明确 owner、权限和 tenant scope。
2. 目标 command 使用 `Idempotency-Key` 做数据库幂等处理。
3. 目标 command 对重复、超时、429、5xx 具有可恢复语义。
4. `SCHEDULER_JOBS_JSON` 仅注入内部地址，不提交真实 token 或用户凭据。
5. 联调先使用 Mock command，再执行目标仓库和 Scheduler 的独立启动验收。
