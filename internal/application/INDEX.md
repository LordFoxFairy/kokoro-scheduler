# application 架构地图

- `commands.go`：受信 tenant command、durable idempotency receipt与 Schedule事务；
- `planner.go`：claim due Schedule，计算 misfire/catch-up/overlap，原子写 Occurrence+outbox；
- `dispatcher.go`：claim/recover outbox，optional lease，timeout、retry和终态事务；
- `processor.go`：每个 cycle按 planner后dispatcher执行；
- `runtime.go`：Wakeup生命周期、startup immediate cycle、cancellation和drain。

Application只依赖 Domain和窄 ports，不引用PostgreSQL、Redis、HTTP或gocron concrete类型。
