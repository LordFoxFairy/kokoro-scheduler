# kokoro-scheduler 安全边界

## 1. Trust boundary

Browser 不直接调用 Scheduler。internal caller 通过固定 service Bearer 和受信 `X-Kokoro-Tenant-Id` 调 control API；目标服务通过独立 outbound credential 验证 Scheduler。inbound/outbound token 不复用。

当前 Bearer 只证明共享 service identity，不提供 caller-specific IAM permission。部署层必须限制网络来源；细粒度授权是明确缺口。

## 2. Tenant isolation

- tenant 不从 body 推导；transport 与 Application 都验证受信 tenant context；
- command receipt identity、Schedule 查询/写入、Occurrence/outbox 状态变更都带 tenant；
- uniqueness 包含 tenant；
- Redis lease key使用 tenant+occurrence digest；
- target dispatch 明确携带 tenant header。

测试覆盖同名 Schedule、相似 idempotency key 的跨 tenant 隔离。任何新增 SQL 必须保持 tenant predicate，architecture/schema/integration gate 不代替代码评审。

## 3. Inbound validation

- constant-time Bearer comparison；
- name、request ID、idempotency key、tenant 长度/控制字符校验；
- mutation body 最大 1 MiB，严格 `application/json`；
- unknown field、duplicate JSON key、null、trailing JSON 拒绝；
- target URL 不允许 credentials/fragment/非法 port；
- recurrence/timezone/retry/misfire/overlap 在写 transaction 前验证；
- error envelope 不返回 SQL、stack、secret 或下游 response body。

## 4. PostgreSQL

`SCHEDULER_DATABASE_URL` 指向 Scheduler 专用 database。Repository 值全部使用 pgx 参数绑定；表/列名是静态源码。Schema 没有跨 owner foreign key，也没有 migration runner。`db:apply-schema` 在 serializable transaction 和 advisory lock 下检查 current namespace 必须为空，避免误把 current schema 当升级目标。

数据库 credential 由 secret manager 注入，不进入 `.env`、日志、contract 或 commit。生产角色应只拥有 Scheduler database 的连接/DML；schema bootstrap 使用单独受控角色更佳。

## 5. Outbound egress

默认 target policy 拒绝 loopback、private、link-local、multicast、unspecified 等特殊 IP。允许受控 internal target 时，`SCHEDULER_INTERNAL_TARGET_ALLOWLIST` 必须给出精确 hostname 与 canonical CIDR。每次 dispatch 解析一次并 pin 到已校验 IP，同时保留 Host/TLS ServerName，避免校验后重新 DNS 解析。

redirect 不跟随；proxy 在 pinned request transport 上关闭；只允许 HTTP(S) POST/PUT；response body 最多读取 1 MiB。部署仍需 egress network policy、内部 DNS 控制、目标认证和 allowlist review，应用检查不是网络隔离替代品。

## 6. Redis

Redis 可选且 URL 必须显式 `/7`。它只保存随机 token 与 TTL lease，不保存 payload、Schedule、receipt 或执行历史。release 使用 Lua compare/delete，错误时 fail/defer，不跨 logical DB 清理。Redis credential同样只通过 secret 注入。

## 7. Logs 与敏感数据

禁止记录 Bearer、database/Redis credential、Schedule payload、target response body。当前 dispatch 日志包含 tenant、schedule、attempt、status、稳定 request/trace identity 和 bounded error；部署日志访问应按 internal metadata 保护。metrics label 不使用 tenant、URL、request ID 或 payload，避免高基数和数据泄露。

## 8. Abuse controls 缺口

当前 API 没有 per-caller rate limit、Schedule quota、target-domain owner approval 或 payload字段级敏感信息检测。部署层需要限制 caller 和连接；后续 quota 必须由 Scheduler Application 使用 durable tenant facts实现，不可用进程内 map 作为唯一控制。
