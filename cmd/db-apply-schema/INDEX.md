# db-apply-schema 入口

`main.go` 读取 `SCHEDULER_DATABASE_URL`，只调用现 `config.ParseDatabaseURL` 获取不可变目标 namespace 与受控 UTC/owner search_path 的 driver URL，不要求 Redis/runtime 配置。调用 `postgres.ApplySchemaToEmptyDatabase(ctx, pool, target)`：以 database＋显式 schema 取事务锁，只接受该 namespace 无用户对象，缺失时仅创建目标，在同事务安装唯一 embedded `database/schema.sql` 并检查轻量后置条件；失败整体回滚，同库邻居不读写。该命令不是 migration 或 drift 修复器；完整 catalog 及并发/回滚真实门仍待 Root 验收。
