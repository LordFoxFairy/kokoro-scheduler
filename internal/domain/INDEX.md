# domain 架构地图

- `schedule.go`：Schedule、retry/misfire/overlap/status值、tenant/URL/payload不变量；
- `occurrence.go`：Occurrence/outbox状态与outcome code、稳定dispatch identity、retry分类；
- `command.go`：control command/result与durable receipt模型。

Domain仅依赖Go标准库。recurrence parser、SQL、HTTP DTO、Redis key和timer实现不进入本目录。
