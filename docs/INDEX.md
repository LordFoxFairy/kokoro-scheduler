# kokoro-scheduler 文档索引

## 阅读顺序

| 顺序 | 文档 | 内容 |
|---:|---|---|
| 1 | [CURRENT](./CURRENT.md) | 当前实现、证据边界、缺口和风险 |
| 2 | [TECHNICAL_DESIGN](./TECHNICAL_DESIGN.md) | owner、依赖、事务、状态机、恢复与 recurrence |
| 3 | [机器 OpenAPI](../contract/openapi/v1/openapi.yaml) | inbound control 与 outbound dispatch 的唯一 wire source |
| 4 | [Contract README](../contract/README.md) | lint、parity、breaking signature 与 provenance |
| 5 | [API_CONTRACT](./API_CONTRACT.md) | 人类可读的调用、幂等、时间与错误策略 |
| 6 | [DATA_MODEL](./DATA_MODEL.md) | 四张表、约束、索引、关系和 retention |
| 7 | [RELIABILITY](./RELIABILITY.md) | claim、outbox、retry、restart、degradation |
| 8 | [SECURITY](./SECURITY.md) | trust boundary、tenant、secret、SQL 和 egress |
| 9 | [SLO](./SLO.md) | 目标、指标契约与尚未实现的观测项 |
| 10 | [ACCEPTANCE](./ACCEPTANCE.md) | 可执行验收矩阵 |
| 11 | [RUNBOOK](./RUNBOOK.md) | schema 安装、启动、诊断、恢复和回滚 |
| 12 | [ADR index](./ADR/README.md) | 架构决策登记入口 |

仓库代码地图见 [`../INDEX.md`](../INDEX.md)，本地启动见 [`../README.md`](../README.md)。

## 唯一事实源

- HTTP/dispatch wire contract：`contract/openapi/v1/openapi.yaml`；
- PostgreSQL current schema：`database/schema.sql`；
- runtime behavior：当前 commit 的 Go 源码与测试；
- 当前状态与风险：`docs/CURRENT.md`。

不保留第二份可编辑 wire schema、历史 migration、旧 timer 实现或进程内事实实现。
