# kokoro-scheduler ADR 索引

本目录记录仍影响 Scheduler owner 边界、contract、持久化、协调和可靠性的架构决策。当前没有独立编号的
accepted ADR；现行实现事实以 [`../TECHNICAL_DESIGN.md`](../TECHNICAL_DESIGN.md) 和机器 contract 为准，
不把历史说明文件重新包装成追溯性决策证据。

## 需要 ADR 的变化

- 引入或更换 occurrence coordination 机制；
- 引入任何 durable Schedule/Occurrence 存储；
- 改变 at-least-once、misfire、lease retention 或 retry 语义；
- 改变 internal API visibility、认证机制或破坏性 contract；
- 改变仓库 owner 边界，特别是任何业务事实进入 Scheduler。

普通文案修订、测试补充和不改变行为的重构不单独创建 ADR。

## 命名与状态

文件名使用 `NNNN-kebab-case.md`，编号单调递增。状态只使用 `Proposed`、`Accepted`、`Superseded`、
`Rejected`；被替代文档必须链接 successor，不能删除历史 decision。

## 模板

```markdown
# ADR-NNNN: 决策标题

- Status: Proposed
- Date: YYYY-MM-DD
- Owners: kokoro-scheduler

## Context

当前事实、约束和触发决策的问题。

## Decision

被选择的方案、明确边界和不变量。

## Consequences

正向结果、代价、失败模式和迁移/回滚要求。

## Verification

能够证明该决策落地的测试、contract 和运行证据。
```

ADR 不能定义别的仓库业务字段；跨仓 wire 字段仍由事实 owner 的 contract 定义。
