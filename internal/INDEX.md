# internal 架构地图

`internal` 是 Scheduler 的生产实现边界，禁止被其他仓库直接导入。

```text
domain       纯领域模型和不变量
application  调度用例、状态控制、lease、重试和 dispatch 编排
ports        技术无关的 Clock、Sleeper、RandomSource、ScheduleEngine、LeaseStore、TargetClient
adapters     cron、HTTP client、Redis lease、系统 timer/random 的具体实现
transport    internal HTTP command surface
config       环境变量解析和启动配置
```

依赖方向：`transport -> application -> domain`；`application -> ports`；`adapters -> ports/domain`。
`cmd/scheduler` 负责把具体 adapter 装配到 application。
