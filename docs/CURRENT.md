# kokoro-scheduler 当前状态

更新日期：2026-09-04。本文只描述当前工作树对应的实现；最终证据以提交后的命令输出为准。

## 当前定位

Scheduler 是 Go 内部服务，唯一生产入口为 `cmd/scheduler/main.go`。PostgreSQL 是 Schedule、Occurrence、command receipt 和 dispatch outbox 的唯一事实源。gocron/v2 只按固定间隔唤醒扫描器；Redis DB 7 可选，只承担短期协调。

| Surface | 当前实现 |
|---|---|
| Schedule | tenant-scoped PostgreSQL row；保存本地 recurrence rule、IANA timezone、状态、版本和 `next_due_at` |
| Command receipt | `(tenant_id, command_scope, idempotency_key)` 唯一；同 digest 精确 replay，异 digest 冲突 |
| Due scan | PostgreSQL transaction + `FOR UPDATE SKIP LOCKED` + expiring claim |
| Occurrence/outbox | 同一事务原子写入；业务唯一键阻止重复 occurrence/outbox |
| Misfire | `skip`、默认 `fire_once`、`catch_up_bounded`；跳过和 bound exceeded 都持久化 outcome |
| Overlap | `allow` 或默认 `forbid`；禁止重叠时写 `SCHEDULER_OVERLAP_BLOCKED`，不静默丢弃 |
| Dispatch | POST/PUT JSON；稳定 occurrence identity；timeout/cancellation；持久化 attempt/status/error |
| Retry | 408、425、429、5xx、网络/timeout 可重试；其余 HTTP 与 caller cancellation 为永久结果；指数退避 + full jitter + attempt/window 上限 |
| Recovery | 启动立即扫描；expired schedule/outbox claim 可重领；final-attempt crash 转为 durable failed outcome |
| Contract | canonical OpenAPI 3.1 + runtime parity + compatibility signature + SHA-256 provenance |
| Schema install | `db:apply-schema` 只接受空 database namespace，并安装唯一 `database/schema.sql` |

## Recurrence 实现范围

本仓明确实现五字段 cron 和 `@every`，不把 timer 库当 recurrence 事实源。支持 `*`、list、range、step、英文月/星期名、Sunday 0/7 以及常用 descriptor。DOM/DOW 使用标准 OR 语义；`*/1` 视为未限制 selector。不存在的日期在保存前拒绝。

IANA timezone 在保存时验证。具体 occurrence 为 UTC 毫秒 instant：spring-forward 不存在的 wall time 不触发，fall-back 两个相同 wall time 对应两个 UTC occurrence。cron 搜索 horizon 为 10 年；`@every` 为 1 秒至 366 天且保持原 due anchor。

## 已知限制与风险

1. v1 没有 Schedule/Occurrence 查询 API；OpenAPI 描述 occurrence lifecycle 和 dispatch webhook，但不虚构读取接口。
2. inbound authorization 当前是共享 Bearer service token；tenant 来自受信 header，尚未接入 caller identity/细粒度 IAM permission。
3. dispatch 是 at-least-once。worker 在目标已接收后、状态提交前崩溃会以相同 `Idempotency-Key` 重投；目标 owner 必须持久化去重。
4. 表 retention/archival/GC 尚未实现；receipt、occurrence 和 outbox 会持续增长，需要部署 owner 在容量数据形成后确定策略。
5. 当前只有结构化 background/dispatch 日志和 probes，没有 metrics exporter、dashboard 或告警规则；`SLO.md` 是目标契约，不是实测证明。
6. Redis lease 不是真相且不做长任务续租；配置强制 `claim_ttl > dispatch_timeout`。超出该模型的长任务应由目标 command 异步化。
7. recurrence 是本仓受测试约束的窄实现，不支持秒字段、`?`、`L`、`W`、`#`、`@reboot` 或 embedded timezone prefix。

## 证据入口

- 单元/架构/schema：`go test ./...`、`go test ./test/architecture ./test/schema`；
- contract：`./scripts/contract-check`；
- PostgreSQL/Redis/restart：`docs/ACCEPTANCE.md` 中带独立测试 database 的命令；
- module：`go list -m github.com/go-co-op/gocron/v2`、`go mod graph`、定向 `rg`；
- fresh schema：在临时空 database 运行 `./scripts/db-apply-schema` 后检查四张表。
