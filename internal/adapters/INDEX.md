# adapters 架构地图

本目录只包含 ports 的技术实现：

- `cron/`：robfig/cron 定时器；
- `httpclient/`：通用 JSON command dispatch；
- `redis/`：带 token 校验的 occurrence lease acquire/renew/release。
- `system/`：context-aware retry timer 与操作系统加密随机源。

禁止在 adapter 中添加 Billing、Agent 或其他业务事实。
