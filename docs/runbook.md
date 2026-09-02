# kokoro-scheduler 运行手册

## 启动

```bash
export SCHEDULER_JOBS_JSON='[]'
go run ./cmd/scheduler
```

生产环境使用 `go build ./cmd/scheduler` 生成的二进制或 Dockerfile 入口。

## 配置

- `SCHEDULER_JOBS_JSON`：严格 JSON 数组；未设置、空字符串或仅空白值时以零任务启动。
- `SCHEDULER_REDIS_URL`：多实例时配置共享 Redis，用于 occurrence lease；单实例可省略。

Job 的完整字段和 retry/misfire 规则见 [API_CONTRACT.md](./API_CONTRACT.md)。

## 故障处理

- Redis lease 不可用时，服务记录 `SCHEDULER_COORDINATION_UNAVAILABLE`，不在失去唯一性
  保证时继续 dispatch。
- 目标服务 429/5xx 或网络错误按 retry policy 处理；目标 4xx 直接失败。
- 业务幂等 receipt、账务状态和最终执行事实在目标服务 PostgreSQL 中排查，Scheduler
  不读取业务数据库。
- 更新配置时先保存上一份 `SCHEDULER_JOBS_JSON`，验证通过后再滚动重启；回滚使用上一份
  配置，避免直接修改运行中的容器环境。
