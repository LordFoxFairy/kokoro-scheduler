# kokoro-scheduler 风险清单

| 风险 | 影响 | 控制措施 | 剩余状态 |
|---|---|---|---|
| 进程重启丢失历史触发窗口 | occurrence 可能跳过 | `misfire_policy` 显式定义；v1 不追溯进程外历史窗口；后续接入 PG registry/recovery | 已知 |
| Redis lease 过期 | 多实例可能重复 dispatch | dispatch 后保留 26 小时 claim、业务端 PostgreSQL 幂等 receipt、监控 coordination failure | 已控制 |
| 目标 command 非幂等 | 重试造成重复业务副作用 | 强制使用 `Idempotency-Key`，目标仓库保存 receipt | 接入前置 |
| 多实例同步重试 | 瞬时故障后形成下游流量尖峰 | capped exponential backoff、每次独立 full jitter、最大总重试窗口 | 已控制 |
| 配置 URL 指向外部地址 | 数据和凭据边界扩大 | 部署校验内部地址 allowlist，配置不携带用户 token | 已控制 |
| Scheduler 误持有业务逻辑 | 跨仓边界漂移 | 仅允许通用 HTTP dispatch；Billing/Credit/Capability 等逻辑由目标仓库拥有 | 已控制 |
