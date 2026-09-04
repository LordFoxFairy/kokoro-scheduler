# ADR-0001: PostgreSQL facts with a replaceable gocron wakeup

- Status: Accepted
- Date: 2026-09-04
- Owners: kokoro-scheduler

## Context

Schedule定义、trigger历史、command replay和dispatch retry需要跨进程重启及多实例竞争保持一致。进程内timer registry与短期coordination store都不能证明这些事实。调度库也不应泄漏进Domain或决定misfire/overlap状态机。

## Decision

1. `database/schema.sql`中的Schedule、Occurrence、command receipt和dispatch outbox是Scheduler唯一durable facts；
2. Application通过窄`Store`/`TxStore` port执行tenant-scoped transaction、idempotency、state transition和recovery；
3. due/outbox claim使用PostgreSQL row lock、`SKIP LOCKED`、worker/expiry与业务UNIQUE；
4. `github.com/go-co-op/gocron/v2 v2.22.0`只实现可替换`Wakeup`，按固定间隔调用Application scanner；
5. recurrence rule由独立、受contract/tests约束的adapter解释，保存local rule+IANA timezone，Occurrence保存UTC instant；
6. Redis DB 7仅可选coordination，删除Redis不删除Scheduler facts；
7. dispatch为durable at-least-once，target使用稳定idempotency identity收敛重复。

## Consequences

- restart后不需要外部重放Schedule注册；receipt、misfire、overlap、attempt和失败结果可恢复/审计；
- PostgreSQL成为必需启动依赖，部署必须先在空Scheduler database安装canonical schema；
- timer更换不改变Domain或持久化模型；
- worker crash可能在target已接收但success未提交时重投，目标owner必须durable deduplicate；
- current clean-slate schema流程不提供历史migration或schema downgrade。

## Verification

- `test/architecture`检查依赖方向、gocron固定版本和adapter隔离；
- `test/schema`检查四表、约束、唯一schema和`SKIP LOCKED`；
- `test/integration`检查restart、tenant、duplicate、atomic outbox、retry和expired claim；
- `test/smoke`从源码启动、重启并replay durable receipt；
- `test/contract`检查canonical OpenAPI、runtime parity、breaking signature和provenance。
