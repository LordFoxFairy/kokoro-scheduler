# internal 架构地图

`internal` 是 Scheduler 的生产实现边界：

```text
domain       Schedule/Occurrence/Receipt/Dispatch 状态与纯规则
application  command transaction、due planning、retry 与 recovery
ports        Store/TxStore、Clock、Recurrence、Wakeup、Lease、Target
adapters     PostgreSQL、recurrence、gocron、HTTP、Redis、system random
transport    internal HTTP control boundary
config       环境配置与启动约束
```

依赖方向：`transport -> application -> domain`、`application -> ports`、`adapters -> ports/domain`。`cmd/scheduler` 是 concrete wiring root。PostgreSQL 是事实源；gocron 只实现 Wakeup；Redis 只实现可选 Lease。
