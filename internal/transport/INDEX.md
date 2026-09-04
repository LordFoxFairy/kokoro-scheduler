# transport 架构地图

`http/` 实现 canonical internal control surface：probes、service Bearer、trusted tenant/request/idempotency headers、strict JSON、Application command映射、durable receipt response replay和稳定error envelope。

Transport不执行SQL，不保存进程内事实，不操作Redis/gocron，也不决定misfire/retry状态机。字段与route必须和`contract/openapi/v1/openapi.yaml` parity。
