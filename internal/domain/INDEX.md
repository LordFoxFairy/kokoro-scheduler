# domain 架构地图

保存 `Job`、`RetryPolicy`、`Occurrence` 和 `RunResult`，以及调度名称、URL、cron、重试和 misfire
不变量。这里不处理 HTTP 请求、Redis lease、日志、定时器和业务仓库。

新增领域规则放在本目录；不要把 target client、Redis key 或 transport response 放入 domain。
