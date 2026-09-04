# scheduler 组合根

`main.go` 是唯一生产服务入口，负责：

1. 读取并校验配置；
2. 连接/探测Scheduler PostgreSQL与可选Redis DB 7；
3. 装配PostgreSQL、recurrence、gocron Wakeup、HTTP target、Redis lease和Application；
4. 启动startup immediate scan及internal HTTP server；
5. readiness检查、SIGINT/SIGTERM cancellation和10秒graceful drain。

业务状态机和SQL不放在组合根。空库schema安装入口是`cmd/db-apply-schema`。
