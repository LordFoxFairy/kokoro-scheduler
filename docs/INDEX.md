# kokoro-scheduler 文档索引

| 文档 | 说明 |
|---|---|
| [技术方案](./technical-architecture.md) | 边界、运行模型、可靠性与部署约束 |
| [API Contract v1](./API_CONTRACT.md) | ScheduleJob 配置、状态机、dispatch headers、重试和幂等语义 |
| [BFF 接入](./bff-integration.md) | BFF v1 调用边界与 command 接入检查 |
| [运行手册](./runbook.md) | 启动、配置、故障处理与回滚 |
| [验收清单](./acceptance.md) | 自动化命令和 Mock 场景矩阵 |
| [风险清单](./risk-register.md) | misfire、lease、幂等及边界风险 |
| [README](../README.md) | 本地启动与配置摘要 |
| [SLI / SLO](./SLO.md) | 生产目标、错误预算、指标和告警阈值 |
| [OpenAPI](../contract/openapi/v1/openapi.yaml) | Scheduler 自有 HTTP wire contract 唯一事实源 |

唯一生产入口：`cmd/scheduler/main.go`。

生产代码按以下边界组织：

```text
cmd/scheduler/                    启动与依赖装配
internal/domain/                  ScheduleJob、Occurrence、RetryPolicy、领域不变量
internal/application/             注册、触发、lease、重试和 dispatch 编排
internal/ports/                   Clock、Sleeper、RandomSource、ScheduleEngine、LeaseStore、TargetClient
internal/adapters/cron/           robfig/cron 定时器实现
internal/adapters/httpclient/     HTTP 目标调用实现
internal/adapters/redis/          Redis occurrence lease 实现
internal/adapters/system/         context-aware retry timer 与加密随机源
internal/transport/http/          internal HTTP command surface
internal/config/                  环境变量和启动配置
test/doubles/                     仅测试替身，不进入生产依赖
```

`domain` 不依赖 Redis、HTTP SDK 或业务包；`application` 只依赖 ports；具体技术实现只能位于
`internal/adapters`；`cmd/scheduler` 是唯一组合根。
