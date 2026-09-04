# kokoro-scheduler 安全设计

## 1. Trust boundary

```text
deployment configuration
        | SCHEDULER_JOBS_JSON / secrets
        v
+----------------------+     trusted service caller
|  kokoro-scheduler    | <--------------------------
+----------------------+
        | generic HTTP command + optional target service token
        v
target owner internal command endpoint

kokoro-scheduler <--- optional private Redis coordination (logical DB 7 locally)
```

Scheduler 不接收浏览器 session、OAuth user token 或 body 中声明的可信 actor。BFF/目标服务拥有用户认证、
tenant、permission 和业务授权；Scheduler 只验证进入本仓 command surface 的 service credential。

## 2. Inbound HTTP controls

- `/internal/scheduler/v1/jobs/**` 只接受
  `Authorization: Bearer <SCHEDULER_INTERNAL_SERVICE_TOKEN>`；token 使用 constant-time compare。
- token 未配置时进程仍可提供探针，但所有 job command fail closed 为 `401 service_auth_failed`。
- 每次 mutation 必须有 1–128 字符 `X-Request-Id` 和 1–256 字符 `Idempotency-Key`。
- job name 只允许 `[a-z0-9][a-z0-9._-]{0,63}`；路径和 method 使用显式 allowlist。
- request body 最大 1 MiB；JSON parser 拒绝未知/重复字段、`null`、错误类型、trailing value 和错误
  `Content-Type`。
- `/healthz`、`/readyz` 无认证，只返回 service/status/request_id；readiness 的 Redis 错误不回传依赖细节。
- 错误 envelope 不暴露 stack、Redis URL、token 或目标 response body。

OpenAPI 中 command operation 的 `x-kokoro-permission: service-token` 精确表示当前授权机制，不宣称已实现
IAM fine-grained permission。探针使用 `x-kokoro-permission: none`。

## 3. Outbound dispatch controls

- URL validation 只允许有 host 的 `http` / `https`，拒绝 embedded credentials、fragment、异常端口、localhost、loopback、未指定、私有、链路本地、组播和特殊/保留 IP。
- hostname target 在每次 dispatch 前只解析一次；所有答案先经过地址策略，随后将选中的地址固定到本次请求 URL，同时保留原 Host/HTTPS server name，避免 validation 与实际连接之间的 DNS TOCTOU。默认不启用 allowlist；注入的 `(host, address)` allowlist 只能进一步收窄已允许的全局地址。
- HTTP client 禁止自动跟随 redirect；目标 response body 读取上限为 1 MiB。
- method 只允许 `POST` / `PUT`，body 必须是 JSON object。
- `SCHEDULER_TARGET_SERVICE_TOKEN` 与 inbound token 分离；非空时只写入 `Authorization` header，不写日志。
- 每次 dispatch 携带稳定 occurrence idempotency identity、request ID 和 W3C `traceparent`；目标 owner 仍须
  校验 service identity、tenant scope 和自己的业务 permission。
- context cancellation 与 30 秒 overall timeout 限制单次请求时长；目标 4xx 不重试，429/5xx/网络错误受
  attempt 与时间窗口双重约束。

当前进程不提供可配置的 hostname/CIDR allowlist；默认地址策略拒绝特殊网段，生产部署仍必须通过受审配置、DNS/egress
network policy 和目标服务认证把 dispatch 限制在内部 endpoint。任何允许修改 ScheduleJob 的 caller 都等价于
拥有创建出站请求的能力，必须保持在受信服务边界内。

## 4. Secret 与日志

| Secret / 敏感数据 | 来源 | 处理规则 |
|---|---|---|
| `SCHEDULER_INTERNAL_SERVICE_TOKEN` | 部署 secret | 不提交、不回显、不写日志；轮换时同步 caller 与 Scheduler |
| `SCHEDULER_TARGET_SERVICE_TOKEN` | 部署 secret | 与 inbound token 独立；轮换时同步 Scheduler 与目标服务 |
| `SCHEDULER_REDIS_URL` | 部署 secret/config | 可能含 credential，禁止写日志；TLS/ACL 由部署环境配置 |
| job body | deployment/trusted caller | 作为 opaque JSON 投递；不进入 dispatch 结构化日志 |

结构化日志允许 `service`、`operation`、job name、request_id、trace_id、status、attempts、result、code 和
duration；不得记录 token、Authorization、Redis URL 或 command body。目标错误当前只记录本仓归一后的
HTTP status/Go error，不读取或记录目标 response body。

## 5. Redis coordination security

- lease value 是每次 acquire 生成的随机 token；renew/release 都验证 token，避免旧 owner 删除新 lease。
- Redis 只承载 occurrence claim；泄漏或清空 Redis 可能造成重复 dispatch，但不会直接泄漏业务数据库事实。
- 多实例必须共享同一受控 Redis/等价 singleton；Redis 不可用或 lease 丢失时 fail closed，不绕过协调继续调用。
- 本地固定使用 logical DB 7；生产隔离还需网络 ACL、认证和必要时 TLS，logical DB 不是安全边界。

## 6. 当前风险与后续安全门禁

1. shared bearer token 无 caller-level audit/permission；升级认证机制必须先改 owner contract 和 consumer。
2. 进程内安全策略不替代部署 egress policy；暴露 command surface 前仍必须验证网络出口和目标服务认证。
3. registry/receipt 是内存结构；1 MiB request/response body limit 与 10,000 receipt 上限限制资源占用，但高频可信 caller 仍需
   上游 rate limit 和实例资源限制。
4. 安全变更必须覆盖负向认证、严格 JSON、header 边界、secret redaction 与目标 mock 测试，并同步
   `API_CONTRACT.md` 和 OpenAPI。
