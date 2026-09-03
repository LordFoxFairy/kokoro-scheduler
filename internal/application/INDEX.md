# application 架构地图

`Scheduler` 是调度用例的唯一编排入口，负责注册/更新/删除、暂停/恢复、occurrence 触发、lease
claim/renew、超时、重试和取消。所有技术能力通过 `internal/ports` 注入。

application 不返回 HTTP response，也不直接引用 Redis、HTTP client 或 cron 实现。
