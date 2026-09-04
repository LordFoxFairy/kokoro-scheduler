# kokoro-scheduler 文档索引

## 阅读顺序

| 顺序 | 文档 | 说明 |
|---:|---|---|
| 1 | [CURRENT](./CURRENT.md) | 当前实现、明确缺口与证据边界 |
| 2 | [TECHNICAL_DESIGN](./TECHNICAL_DESIGN.md) | owner、依赖方向、运行流程、状态机与部署模型 |
| 3 | [API_CONTRACT](./API_CONTRACT.md) | 人类可读的 internal API、dispatch 与 consumer 语义 |
| 4 | [机器 OpenAPI](../contract/openapi/v1/openapi.yaml) | HTTP 字段、operation 和 response 的唯一 wire source |
| 5 | [Contract README](../contract/README.md) | owner、visibility、version、generation、breaking、provenance |
| 6 | [DATA_MODEL](./DATA_MODEL.md) | 内存模型、Redis lease、时间与 retention；明确无业务数据库 |
| 7 | [SECURITY](./SECURITY.md) | trust boundary、认证、输入、secret 与出站目标控制 |
| 8 | [RELIABILITY](./RELIABILITY.md) | 交付语义、lease、重试、恢复、退化和风险登记 |
| 9 | [SLO](./SLO.md) | SLI/SLO、错误预算、指标契约与告警阈值 |
| 10 | [ACCEPTANCE](./ACCEPTANCE.md) | 可执行命令与行为验收矩阵 |
| 11 | [RUNBOOK](./RUNBOOK.md) | 启动、诊断、处置、回滚和恢复 |
| 12 | [ADR index](./ADR/README.md) | 架构决策登记规则与当前索引 |

仓库级代码地图见 [`../INDEX.md`](../INDEX.md)，五分钟启动见 [`../README.md`](../README.md)。

## 规范入口

- 技术架构内容只进入 `TECHNICAL_DESIGN.md`；
- BFF/consumer 接入语义只进入 `API_CONTRACT.md`；
- 风险、失败恢复与退化只进入 `RELIABILITY.md`；
- 验收与运行手册分别只使用 `ACCEPTANCE.md`、`RUNBOOK.md`；
- 不保留小写文件、兼容链接或第二份可编辑 contract。

唯一生产入口是 `cmd/scheduler/main.go`；唯一 HTTP wire source 是
`contract/openapi/v1/openapi.yaml`。
