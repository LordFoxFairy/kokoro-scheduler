# db-apply-schema 入口

`main.go`读取`SCHEDULER_DATABASE_URL`，连接Scheduler专用PostgreSQL，并调用`postgres.ApplySchemaToEmptyDatabase`安装embedded `database/schema.sql`。current namespace非空时退出；该命令不是migration或drift修复器。
