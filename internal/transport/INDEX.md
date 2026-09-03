# transport 架构地图

`http/` 只负责 internal HTTP 的认证、请求解析、幂等 response replay、错误映射和健康探针。
它通过 application interface 操作 Scheduler，不执行 SQL、不直接操作 Redis、不编排业务规则。
