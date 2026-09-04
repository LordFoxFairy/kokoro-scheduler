# adapters 架构地图

- `postgres/`：`Store`/`TxStore`、tenant-scoped SQL、`FOR UPDATE SKIP LOCKED` claim、outbox/recovery、空库 schema bootstrap；
- `recurrence/`：五字段 cron、descriptor、`@every`、IANA timezone、DST/DOM-DOW语义；
- `gocron/`：固定周期进程内 wakeup，不加载或保存 Schedule；
- `httpclient/`：POST/PUT dispatch、稳定 identity、timeout/cancellation、DNS pin、错误分类；
- `redis/`：可选 DB 7 token-fenced coordination lease；
- `system/`：操作系统加密随机源，用于 full jitter。

Adapter 实现技术 port，不拥有 misfire/overlap状态机、tenant授权或其他 Kokoro业务事实。
