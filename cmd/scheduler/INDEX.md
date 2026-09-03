# scheduler 组合根

`main.go` 是唯一生产入口，负责读取 `config`、构造 cron/HTTP/Redis adapter、装配
`application.Scheduler`、启动 HTTP/cron，并在 SIGTERM/SIGINT 时执行有超时的 graceful shutdown。
