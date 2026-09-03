# kokoro-scheduler 运行手册

## 启动

```bash
export SCHEDULER_JOBS_JSON='[]'
export SCHEDULER_INTERNAL_SERVICE_TOKEN=TOKEN
# Optional; adds Authorization: Bearer TOKEN to outbound job dispatches.
export SCHEDULER_TARGET_SERVICE_TOKEN=TOKEN
go run ./cmd/scheduler
```

生产环境使用 `go build ./cmd/scheduler` 生成的二进制或 Dockerfile 入口。
默认 HTTP server 监听 `:8080`；通过 `SCHEDULER_HTTP_ADDR` 修改监听地址。`/healthz` 是不需要认证的
进程探针；`/readyz` 还检查 scheduler 已启动，并在配置 Redis 时执行 2 秒内完成的实时 PING。容器使用
内置 `kokoro-scheduler healthcheck` 探测 `/readyz`；自定义监听地址时通过
`SCHEDULER_HEALTHCHECK_URL` 设置容器内可访问的 URL。

## 配置

- `SCHEDULER_JOBS_JSON`：严格 JSON 数组；未设置、空字符串或仅空白值时以零任务启动。
- `SCHEDULER_REDIS_URL`：多实例时配置共享 Redis，用于 occurrence lease；单实例可省略。
- `SCHEDULER_INTERNAL_SERVICE_TOKEN`：BFF 调用 scheduler internal command 的共享 service
  token。生产环境必须注入；未配置时 scheduler 仍可启动和报告健康，但所有 job command
  返回 `401 service_auth_failed`。
- `SCHEDULER_TARGET_SERVICE_TOKEN`：可选的 Scheduler 调用目标服务凭据。非空时出站 dispatch
  携带 `Authorization: Bearer <token>`；仅当目标服务契约不要求服务认证时才允许留空。该
  token 与 internal command token 独立，不写入日志；轮换时重新部署 scheduler 和目标服务。
- `SCHEDULER_HTTP_ADDR`：HTTP server bind address，默认 `:8080`。
- `SCHEDULER_HEALTHCHECK_URL`：容器 healthcheck 使用的 readiness URL，默认
  `http://127.0.0.1:8080/readyz`。

Job 的完整字段、internal command 路由、严格 JSON、request ID、幂等和 retry/misfire
规则见 [API_CONTRACT.md](./API_CONTRACT.md)。

Retry 数值边界：`max_attempts=1..10`、`backoff_seconds=1..3600`、
`max_backoff_seconds=1..3600`、`max_retry_window_seconds=1..86400`，且最大 backoff 不得小于
初始 backoff。默认依次为 `1/1/3600/3600`；显式零值不会被当作省略值。

## 故障处理

- Redis lease 不可用时，服务记录 `SCHEDULER_COORDINATION_UNAVAILABLE`，不在失去唯一性
  保证时继续 dispatch。
- 目标服务 429/5xx 或网络错误按 capped exponential backoff + full jitter 处理；目标 4xx 直接失败。
- 重试只在 `max_attempts` 与 `max_retry_window_seconds` 均有余量时继续；每次等待不超过
  `max_backoff_seconds` 和剩余窗口。`SCHEDULER_RETRY_JITTER_UNAVAILABLE` 表示操作系统随机源异常，
  本次 occurrence 会停止重试并按失败路径释放 lease。
- 结构化日志包含 `service`、`operation`、`request_id`、`trace_id`、`result`、`attempts` 和
  `duration_ms`；禁止记录 service token、Redis URL 和 command body。
- 业务幂等 receipt、账务状态和最终执行事实在目标服务 PostgreSQL 中排查，Scheduler
  不读取业务数据库。
- internal job registry 是进程内存，不在 PostgreSQL 或 Redis 中持久化。scheduler 重启后，
  由 BFF/deployment orchestration 重放 register/update 请求；回滚时先保存上一份
  `SCHEDULER_JOBS_JSON` 和注册请求记录，验证通过后再滚动重启。
