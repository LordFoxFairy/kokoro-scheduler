# kokoro-scheduler 验收清单

## 自动化验收

```bash
gofmt -l .
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/scheduler
```

## 场景矩阵

| 场景 | Fixture/检查 | 结果标准 |
|---|---|---|
| 合法配置 | `LoadJobs` | 完成注册并填充默认值 |
| 非法配置 | 未知字段、重复名称、非法 cron | 启动失败 |
| 成功 dispatch | HTTP Mock 返回 2xx | `succeeded`，含 request/idempotency metadata |
| 目标失败 | Mock 返回 4xx/5xx 或网络错误 | 正确分类，按策略仅重试可重试失败 |
| pause/resume | Service control fixture | pause 跳过后续 occurrence，resume 恢复 |
| misfire | `Trigger(scheduledAt, observedAt)` | `skip` 丢弃，`fire_once` 补发一次 |
| 多实例 | Redis locker fixture | 同一 occurrence 只获得一个 lease |
| 启停 | 空任务配置启动并发送 SIGTERM | 输出 started/stopped 且进程退出 |

真实 PostgreSQL/Redis 不属于本仓 v1 启动前置：Scheduler 使用部署配置和可选 Redis
lease；业务事实由目标仓库在 PostgreSQL 中保存。
