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

唯一生产入口：`cmd/scheduler/main.go`。
调度核心：`service.go` + `github.com/robfig/cron/v3`；HTTP/Redis 属于 `dispatch.go` adapters。
