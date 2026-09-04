# kokoro-scheduler ADR 索引

本目录记录仍影响 Scheduler owner 边界、contract、持久化、协调和可靠性的架构决策。运行事实仍以当前源码、`database/schema.sql` 和 canonical OpenAPI 为准。

## Accepted

- [`0001-postgresql-facts-gocron-wakeup.md`](./0001-postgresql-facts-gocron-wakeup.md)：PostgreSQL 持久化事实，gocron/v2 仅进程唤醒，Redis 仅可选协调。

## 需要新 ADR 的变化

- 更换 Scheduler durable fact store 或 claim/outbox transaction模型；
- 改变 at-least-once、misfire、overlap、retry 或 recovery语义；
- 让timer/Redis成为事实源；
- 改变internal API visibility、认证或破坏性contract；
- 改变owner边界，特别是其他仓业务事实进入Scheduler。

文件名使用`NNNN-kebab-case.md`。状态使用`Proposed`、`Accepted`、`Superseded`、`Rejected`；被替代决策链接successor。
