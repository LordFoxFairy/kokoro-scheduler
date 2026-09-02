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
默认 HTTP server 监听 `:8080`；通过 `SCHEDULER_HTTP_ADDR` 修改监听地址。`/healthz` 和
`/readyz` 是不需要认证的进程探针。

## 配置

- `SCHEDULER_JOBS_JSON`：严格 JSON 数组；未设置、空字符串或仅空白值时以零任务启动。
- `SCHEDULER_REDIS_URL`：多实例时配置共享 Redis，用于 occurrence lease；单实例可省略。
- `SCHEDULER_INTERNAL_SERVICE_TOKEN`：BFF 调用 scheduler internal command 的共享 service
  token。生产环境必须注入；未配置时 scheduler 仍可启动和报告健康，但所有 job command
  返回 `401 service_auth_failed`。
- `SCHEDULER_TARGET_SERVICE_TOKEN`：可选的 Scheduler 调用目标服务凭据。非空时出站 dispatch
  携带 `Authorization: Bearer <token>`；留空时不发送该 header，以保持现有 fixture 兼容。该
  token 与 internal command token 独立，不写入日志；轮换时重新部署 scheduler 和目标服务。
- `SCHEDULER_HTTP_ADDR`：HTTP server bind address，默认 `:8080`。

Job 的完整字段、internal command 路由、严格 JSON、request ID、幂等和 retry/misfire
规则见 [API_CONTRACT.md](./API_CONTRACT.md)。

## 故障处理

- Redis lease 不可用时，服务记录 `SCHEDULER_COORDINATION_UNAVAILABLE`，不在失去唯一性
  保证时继续 dispatch。
- 目标服务 429/5xx 或网络错误按 retry policy 处理；目标 4xx 直接失败。
- 业务幂等 receipt、账务状态和最终执行事实在目标服务 PostgreSQL 中排查，Scheduler
  不读取业务数据库。
- internal job registry 是进程内存，不在 PostgreSQL 或 Redis 中持久化。scheduler 重启后，
  由 BFF/deployment orchestration 重放 register/update 请求；回滚时先保存上一份
  `SCHEDULER_JOBS_JSON` 和注册请求记录，验证通过后再滚动重启。
