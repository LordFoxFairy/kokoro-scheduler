## R45-WIN09：真实首轮在 fixture 阶段失败（2026-10-01）

Root PG 首轮 **18 PASS / 6 FAIL（24 顶层，1.388s）**；六个新资源用例尚未到 reader/比较/分类/取消断言，失败是四表 sentinel 未完整准备，不是 missing callable 或 catalog 行为 RED。日志 `/tmp/kokoro-scheduler-catalog-r45-root-resource.log`。

现仅补新测试 beforeSnapshot 前的既有 full seed；接口、typed snapshot、比较语义与诊断契约不改。bootstrap SHA256 仍 `b2706161ae535efe2e105f04fb65eaf7c5546332b8e19a4a868b2fea2ceff46f`，等待 Root 真实复验。下方全文保持。

## R44-WIN09：冻结内部 callable 边界（2026-10-01）

当前源码候选已具名实现，替代下方 R43 的“未有 reader”阶段状态；原正文保持：

```go
ReadSchemaCatalog(ctx context.Context, pool *pgxpool.Pool, target config.DatabaseTarget) (SchemaCatalog, error)
CompareSchemaCatalog(actual, reference SchemaCatalog) []SchemaCatalogDifference
```

- 仅本 owner 内部 adapter 导出；无新增 endpoint/Proto/OpenAPI/generated 或公开 ABI。pool 属 caller，目标来自既有唯一 parser；reader 不关闭 caller pool，不安装/修复/创建 reference。
- `SchemaCatalog` 为 typed Tables/Types 结构及其 columns/defaults/constraints/indexes/自动类型/内部约束 trigger；每个对象身份由 Namespace/Name 表示，owner namespace 为 SELF。表达式、定义、字面量保留原值，非第二份 editable expected 清单。
- `SchemaCatalogDifference` 仅 ObjectKind/ObjectName/Field 三个差异路径字段，不输出 actual/reference 值；对象集合排序稳定，列序与索引成员序保留，比较不修改 caller 快照。
- 缺 target/pool、不存在 namespace、取消/超时、读取或身份解析失败、未知 owner 结构均返回错误；取消支持 `errors.Is(err, context.Canceled)`。无 public fallback、弱化计数通过或资源副作用。
- 两入口已可编译，后继测试不引用 missing symbol；实际行为证据尚未执行。普通 runtime ready 与现 installer 对外签名/事务行为保持。

## R43-WIN09-fullcatalog D0：内部结构验证边界（2026-10-01）

状态：仅文档候选，待 Root 文档门；原 namespace、公开 API 和历史正文完整保留。owner 基线 `9a4effd150ab739acaed2579faa94618bb26f772`，Root 任务卡基线 `14c70d794b69428ef012c07ca38804b62f77b5cc`。

### 唯一入口与职责

- 当前可调用边界仍是 `ParseDatabaseURL` → 不可变 `DatabaseTarget` → `ApplySchemaToEmptyDatabase`，canonical 内容来自 `database/schema.go` 的 embedded `database/schema.sql`。现 `NewStore(pool, target)` 与 `Store.Ping` 执行轻量 owner ready，未有完整结构快照 API。
- 提议在现 bootstrap adapter 内新增只读验证能力：输入 caller context、现 pool、显式 `DatabaseTarget`，输出 DATA_MODEL R43 定义的结构快照或失败；比较 actual/reference 为纯结构比较，返回稳定对象/字段差异路径。具体 Go 名称、类型与可见性须由 Root 在实现任务卡冻结，本节不是已发布符号承诺。
- 该边界只供本 owner installer 验证及 Root-owned fixture/fresh gate，不属于 public/browser-private/internal-owner 网络 API/event-protocol；无新增 endpoint、HTTP error code、Proto/OpenAPI/generated client、schema version 或权限模型。对外业务幂等、分页、tenant/actor 信任来源和状态机不变。
- snapshot 差异诊断不得输出原连接 URL、密码、token、业务行、完整 SQL 或 default/CHECK 字面量；可输出对象种类、逻辑对象名与差异字段。表达式实际值留在受控比较内，不靠删值实现脱敏或一致。
- Reader 缺 pool/目标、目标不存在、取消、catalog 读取失败、身份解析失败或遇不支持 owner 对象均失败；不 fallback 到 public/邻居，不降级为四表计数，不做修复。引用外部对象的 namespace 身份保留，不将所有 namespace 归为 SELF。
- 只读事务与资源生命周期在 TECHNICAL_DESIGN R43；仅显式 fixture setup 调用现 installer。runtime 不创建 reference，不启动新增 worker 或共享服务。

### 可执行 RED 的接入门

当前代码没有上述 reader/comparator，D0 未修改测试；仅导入未来函数而不编译不是 RED。后继先由 Root 批准可编译真实采集边界，再对同源参考与语义漂移采集真实行为证据；缺字段/错误归一/漏对象等具体断言失败才算对应 RED。不得要求轻量 `Store.Ping` 因索引或 CHECK 漂移失败，它只验证原已批准的 ready 契约。

现 `ApplySchemaToEmptyDatabase` 已用 namespace 的 pg_depend 占用检测；function-only/type-only 的拒安装案例无需未来入口，属于独立补证，实际若通过则记录 GREEN 而非虚构 RED。当前 Root 17 PG PASS 不包含上述完整比较与新分类案例。文档门、接入方案、源码/测试写入授权和真实资源门均由 Root 放行。

## R40-WIN09-schema：连接边界 cutover，待真实复验（2026-10-01）

状态：Root 文档门与配置/目标 only-view/缺 namespace ready 三项真实 RED 已通过前置；当前工作树正在已批准精确 source/URL 切换，基线 `9a4effd150ab739acaed2579faa94618bb26f772`，未提交，资源 GREEN/完整 catalog 尚未验收。本节覆盖旧正文的连接/readiness 决定；旧正文逐字保留，字段级事实仍只有既有 OpenAPI。

- Schedule、Occurrence、tenant、mutation receipt、dispatch identity、状态机及所有 HTTP path/body/error envelope 保持；canonical OpenAPI、版本、manifest digest、breaking policy 均不变。数据库 namespace 是部署输入，不是 tenant header、业务 schema、API body 或新服务。
- `SCHEDULER_DATABASE_URL` 由现 `internal/config/config.go` 唯一纯 parser 解释，runtime 与 installer 共用。原始控制字符在 trimming 前拒；显式唯一 `schema` 为小写安全 ASCII、最多 63 字节，拒空/重复/列表/public/pg\_ 前缀及 options/search_path/timezone 等驱动覆盖入口和大小写变体。selector 消费后产生唯一 owner search_path 与 UTC，普通 TLS 参数保持；无 secret 回显或默认/fallback。
- 安装入口只加载数据库规则，不要求 Redis/runtime 配置；bootstrap 显式使用 parser 给出的目标 namespace。安装锁与对象空白判断限制在该目标；同库其他 owner 已有事实不妨碍目标 fresh install，也不被读取或清理。
- `/healthz` 继续表示进程存活。`/readyz` 保留既有 Runtime/Redis 条件，数据库条件从连接 ping 收敛为显式目标 namespace、UTC session 与四张事实表存在。目标缺失返回既有 `scheduler_not_ready`/503，已安装且其他条件通过才为200；不自动创建目标、不借 public/邻居表。轻量 ready 不证明所有 catalog 约束正确。
- Catalog 完整比较是 Root 的真实 owner 验收门：只读目标快照对照自有参考 namespace 中同 canonical SQL 的结果，正常 runtime 不建 reference，不发布新查询 API或第二清单。
- 当前两生产入口、source smoke 与 installer/fixture 显式目标调用点均消费唯一 `DatabaseTarget`，CI/release 仅获批生产 URL 加 owner selector，测试 base URL 保持。没有旧 search_path 输入 alias、第二 parser 或兼容路由；原 Runtime/Redis 条件和 API 全部保持。本片仅取得纯 Go 证据，资源 GREEN、完整 catalog、source-process/镜像仍待 Root；未新增状态码。
# kokoro-scheduler API contract

字段级唯一事实源是 [`../contract/openapi/v1/openapi.yaml`](../contract/openapi/v1/openapi.yaml)。本文解释调用语义，不复制成第二套 machine schema。

## 1. 可见性与认证

- owner：`kokoro-scheduler`；
- inbound visibility：`internal-owner`；
- outbound dispatch visibility：`event-protocol`；
- base prefix：`/internal/scheduler/v1`。

所有 mutation 要求：

```text
Authorization: Bearer <service token>
X-Kokoro-Tenant-Id: <trusted tenant>
X-Request-Id: <stable caller request id>
Idempotency-Key: <stable mutation identity>
```

Transport 不从 JSON body 推导 tenant。service token 为空时 command routes fail closed。每个 OpenAPI operation 声明 owner、visibility、stability、idempotency 和 permission metadata。

## 2. Control surface

| Method | Path | 语义 |
|---|---|---|
| GET | `/healthz` | 进程存活 |
| GET | `/readyz` | Runtime + PostgreSQL；配置 Redis 时再检查 Redis |
| POST | `/internal/scheduler/v1/schedules/{name}` | 创建 Schedule |
| PUT | `/internal/scheduler/v1/schedules/{name}` | 完整替换 Schedule |
| DELETE | `/internal/scheduler/v1/schedules/{name}` | 删除 Schedule 定义；既有 outbox snapshot 继续收敛 |
| POST | `/internal/scheduler/v1/schedules/{name}/pause` | 暂停新 due materialization |
| POST | `/internal/scheduler/v1/schedules/{name}/resume` | 从 resume 当前 instant 重新计算下一次 due |

DELETE/pause/resume 接受空 body 或严格 `{}`。Schedule body 为严格 JSON object：未知字段、重复 key、null、trailing JSON、错误 content type 和超过 1 MiB 均拒绝。`name` 可省略；若提供必须与 path 一致。

POST/PUT 的主要字段：本地 `schedule` rule、`timezone`、target `url`/`method`/opaque object `body`、retry、misfire、catch-up、overlap 和 `paused`。默认值及范围由 canonical OpenAPI 和 Domain 同时约束。

## 3. Durable idempotency

identity 是 `(tenant_id, HTTP method:path, Idempotency-Key)`；request digest 包含 command scope 与规范化 payload。

- 首次请求在同一事务中修改 Schedule 并写 receipt；
- 相同 identity + digest 返回首次保存的 status/body/request ID，即使进程已重启；
- 相同 identity + 不同 digest 返回 `409 idempotency_conflict`；
- 不同 tenant 的相同 path/key 相互隔离。

调用方在 response 丢失时必须重发原 method、path、tenant、key 和语义相同的 body。

## 4. 时间与 recurrence

API instant 为 RFC3339 UTC；Schedule rule 与 IANA timezone 分列。`next_due_at` 和 dispatch 的 `X-Kokoro-Scheduler-Occurrence` 都是 UTC。DST、DOM/DOW 和 unsupported grammar 的精确语义见 [`TECHNICAL_DESIGN.md`](./TECHNICAL_DESIGN.md)。

## 5. Outbound dispatch

OpenAPI `webhooks.scheduleOccurrenceDispatch` 定义 Scheduler 调目标 owner 的 POST/PUT。opaque JSON body 不被 Scheduler 解释。headers：

- `X-Kokoro-Tenant-Id`；
- `X-Kokoro-Scheduler-Schedule`；
- `X-Kokoro-Scheduler-Occurrence`（RFC3339 UTC）；
- `X-Request-Id`；
- `Idempotency-Key`；
- W3C `traceparent`；
- 可选独立 outbound Bearer。

2xx 为成功。408/425/429/5xx 与网络/timeout 可重试；其他 HTTP 为永久失败。3xx 不跟随。每次 attempt 使用同一 occurrence identity，目标 owner 必须 durable deduplicate。

## 6. Error envelope

成功：`{"data": ..., "meta":{"request_id":"..."}}`。错误：`{"error":{"code":"...","message":"..."},"meta":{"request_id":"..."}}`。不返回 SQL、stack、token、下游 body 或 provider 原文。

稳定错误包括 auth/request/tenant/idempotency/route/media/body validation，以及 `schedule_already_exists`、`schedule_not_found`、`scheduler_command_failed`。具体 status 与 schema 以 OpenAPI 为准。

## 7. Contract gate

```bash
./scripts/contract-check
```

该 gate 解析 canonical document、检查 `$ref`/operation metadata/关键 schema，执行每个 control route 的 runtime parity，并把 dispatch header 与 concrete HTTP adapter 对齐。`breaking-policy.json` 固定 v1 protected surface；`manifest.json` 固定 artifact SHA-256 provenance。
