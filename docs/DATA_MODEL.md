## R45-WIN09：补完整四表 fixture，不放宽证据（2026-10-01）

Root 实际 PG 首轮 **18 PASS / 6 FAIL，1.388s**，日志 `/tmp/kokoro-scheduler-catalog-r45-root-resource.log`。现 `prepareStoreBoundaryFixture` 只填 schedule/receipt，而新增用例立即调用要求四表都有事实的 `namespaceBoundaryFactsSnapshot`，因此在 occurrence sentinel 缺失处终止；未证明 catalog 正例或漂移行为。

新增测试五处入口复用现 `seedNamespaceBoundaryFacts(t, openNamespaceBoundaryPool(...))` 填 base/neighbor 四表；function/type 共用一个 helper，覆盖六个资源用例。保持旧 fixture helper、snapshot 非空/原值断言、原 17 测试、canonical SQL、collector 与清理行为；不删除断言、不改 expected。Root 报告自有临时库回收，修后真实矩阵待 Root。下方全文保持。

## R44-WIN09：真实 collector 与矩阵候选（2026-10-01）

现 bootstrap 已实现具名 readonly RR collector/typed snapshot/纯 comparator；唯一 canonical SQL 未改，期望仍来自 Root 自有 reference 的既有 installer。下方 D0/namespace/历史全文保留，当前源码与新增测试尚待 Root 真实 PG 验证。

- R43 profile 已落入真实 pg_catalog 查询，并保留列 storage/compression/COMMENT、constraint/type/index COMMENT、index replica-identity/clustered 标志。PG 18 的 NOT NULL constraint、enforced/period 元数据也采集；后两字段经实际 catalog JSON 投影读取，不改依赖或服务版本。[官方 pg_constraint 语义](https://www.postgresql.org/docs/18/catalog-pg-constraint.html)
- 自动 deferrable UNIQUE/PK recheck trigger 仅在 builtin function 与 owning constraint/index 关系已确认时支持；以 constraint 身份消除生成名中的物理 OID，保留 timing/enabled/deferred/列/argument/index/function。其他 trigger/routine/type/view/sequence、policy/rule/inheritance 等不支持结构 fail closed。
- 现 integration 原 17 顶层测试全文未改，新增 7 个顶层用例：installer function-only/type-only、同源不同 namespace/OID 正例、same-count drift、额外对象、missing/cancel、纯 comparator。drift 16 子例覆盖缺列/type/NULL/精度/storage/COMMENT/default/literal/CHECK/NOT VALID/UNIQUE deferred/predicate/方向/NULLS/INCLUDE/expression；extra 5 子例含独立 table trigger。
- trigger 负例使用 builtin `suppress_redundant_updates_trigger`，不同时创建 owner function，以独立验证 table-bound inventory。[官方 builtin trigger](https://www.postgresql.org/docs/16/functions-trigger.html)
- 正例记录实际 server_version_num，并在同一个单连接 backend 观察目标局部 GUC 恢复；每个 drift 先同源相等、DDL 成功、四表保持、轻量 ready 正控制，再要求指定结构字段产生差异。reference 与邻居事实保持，fixture 生命周期沿用既有创建标志与精确有界清理。
- 本轮仅收集 24 顶层测试；上述行为未运行。function/type 非空边界可能原已通过，实际结果由 Root 记录，未冒称 RED 或 fullcatalog GREEN。

## R43-WIN09-fullcatalog D0：同源结构 profile 与漂移矩阵（2026-10-01）

状态：文档设计，未实现 catalog reader/comparator，未新增测试或改 canonical SQL。下方 namespace 规则与历史正文完整保留。唯一结构事实源仍为 `database/schema.sql`，经现 `database/schema.go` embed 和现 installer 加载；Root-owned reference 安装同一来源，不维护第二 expected 清单。

### 结构范围与语义字段

比较当前四表的所有实际字段/对象，而不是手写四表的字段期望。PK/UNIQUE 产生的索引以及表自动产生的 composite/array type 也进入逻辑对象 inventory；不得用“系统生成”标签忽略语义对象。表类型依赖关联到对应逻辑表；type 的种类、element/base/关联关系须保持。当前 canonical 没有自定义 function/type/view/sequence/trigger，出现这些或未知 namespace-bound 用户对象即 fail closed，不能静默过滤。

| 结构 | 必须保留的语义 |
| --- | --- |
| 表 | 逻辑 namespace/name、relkind/persistence、access method、reloptions、RLS/force RLS、replica identity、表 COMMENT；非 canonical relation kind 或对象也必须被 inventory 发现。 |
| 列 | attnum 的列序、名称、symbolic type identity＋typmod/ndims、collation、NOT NULL、identity/generated/dropped 状态、default 缺失与表达式区别。dropped 属性不得因 type OID=0 的 inner join 被丢掉。 |
| default | 通过 pg_get_expr 反解，不直接比较含 OID 的 pg_node_tree；默认常量、cast、精度与表达式均保留。 |
| 约束 | 名称/种类、列名及顺序、validated/deferrable/deferred/noinherit、pg_get_constraintdef 的定义、关联逻辑索引。CHECK 表达式或 NOT VALID 不可消失。当前无 FK；额外 FK 按不支持新增 owner 结构拒绝，不以 JOIN 跨 owner。 |
| 索引 | 名称/所属表/access method、unique/primary/exclusion/immediate/nulls-not-distinct/valid/ready/live、key 与 INCLUDE 数量及有序成员、表达式、每个 key 的方向/NULLS/opclass/collation、partial predicate。只比索引名、个数或整串盲替换的 CREATE INDEX 均不合格。 |
| 对象 inventory | namespace 依赖的对象类别/逻辑身份/关联及 type 语义；包含正常自动对象。未知类、额外 routine/type/relation/trigger、解析不了的身份都失败，不用白名单筛选把它们隐藏。 |

字段含义依据 PostgreSQL 官方 [pg_attribute](https://www.postgresql.org/docs/16/catalog-pg-attribute.html)、[pg_constraint](https://www.postgresql.org/docs/16/catalog-pg-constraint.html)、[pg_index](https://www.postgresql.org/docs/16/catalog-pg-index.html)、[pg_class](https://www.postgresql.org/docs/16/catalog-pg-class.html)、[pg_type](https://www.postgresql.org/docs/16/catalog-pg-type.html)；依赖与表达式入口见 [pg_depend](https://www.postgresql.org/docs/16/catalog-pg-depend.html)、[pg_attrdef](https://www.postgresql.org/docs/16/catalog-pg-attrdef.html)、[反解函数](https://www.postgresql.org/docs/16/functions-info.html)。这是语义来源，不宣称 Root 当前 server 是 16；实际 server_version_num 待 Root 资源证据记录。

### 唯一允许的归一

1. 仅在明确的身份字段把本目标 namespace 标为 SELF；OID 解析为 namespace＋对象名/签名/关联逻辑身份。外部/builtin namespace 保持真实名，解析失败即错误，不以 OID=0 或空值吞掉。
2. 集合按对象种类/逻辑名稳定排序；列序、索引 key/INCLUDE、约束列和 type 有序成员保留其语义顺序，不能统一字母排序。
3. 两份快照在同 server/version、相同受控 search_path/UTC 下使用非 pretty deparse。禁止对 SQL 表达式进行全局 namespace 替换、正则删字面量、压空白、改大小写或删除 CHECK/predicate；即使字面量等于 actual/reference schema 名也原样保留。当前 builtin 表达式保守比较；未来遇 namespace-qualified 表达式若无法在身份结构层处理即 fail closed，先设计再扩展，不悄悄宣称等价。
4. 原始 OID、物理 filenode、统计/行数、页数、事务水位不进入逻辑 DDL profile；这是非结构观测项，不是表达式归一。部署 role/ACL/GRANT/mTLS 仍属部署阶段，本门不新增其配置或伪造验收。
5. 只读 Repeatable Read 的单 session 采集，不读取业务数据、不写物化 expected snapshot。Root reference 与 actual 都来自同 canonical loader；禁止根据 actual 修改 reference 来通过。

### 现 integration 文件的待验矩阵

全部仅计划在现 `test/integration/postgres_test.go` 中扩展，不创建文件。完整比较需要 API_CONTRACT R43 的可编译接入门；下列目标断言未执行，不冒称 RED/GREEN。

| 拟测试 / 子例 | 准备与必须断言 |
| --- | --- |
| `TestSchemaCatalogMatchesCanonicalReferenceAcrossNamespaces` | 不同随机 actual/reference schema 均现 installer 安装；namespace/OID 不同但结构相等；重复采集稳定、无写入；reference 不改。 |
| `TestSchemaCatalogRejectsSameTableCountDrift` / column | 每例独立 actual：删 outbox.last_error_message；schedule.version BIGINT→INTEGER；target_url DROP NOT NULL；next_due_at TIMESTAMPTZ(3)→(6)。保持四 ordinary tables，基线相等，成功变更后完整比较失败。 |
| 同测试 / default 与 literal | status 默认 active→paused；另例把文本 default 改为包含 actual schema 名的字面量，采集须保留原 literal 且比较失败，验证不是盲替换 namespace。 |
| 同测试 / CHECK | outbox 的 attempt_count 上界检查改为更宽表达式（同约束名）；另例原表达式重建为 NOT VALID。采集表达式/validated 差异，均失败。 |
| 同测试 / UNIQUE | tenant/name UNIQUE 在同逻辑名下变成 DEFERRABLE INITIALLY DEFERRED，保持列集合与索引数量；比较仍失败。 |
| 同测试 / 索引 | due partial predicate active→paused；occurrence scheduled_at DESC→ASC/NULLS 顺序变化；receipt(created_at,id)→(created_at) INCLUDE(id)。各保持逻辑索引名与表数，分别验证 predicate/方向/key-vs-INCLUDE 语义。 |
| `TestSchemaCatalogRejectsExtraOwnerObjects` | 已安装四表后添加 fixture 自有 function 或 enum type；不得被采集筛掉，完整比较失败。 |
| `TestApplySchemaRejectsFunctionOnlyNamespace` / `TestApplySchemaRejectsTypeOnlyNamespace` | 分别只含自有 SQL function / enum type 的目标拒安装，原对象/邻居不变且未生成四表；用现 installer。pg_depend 已覆盖，可能已 GREEN，按实际补证。 |
| 缺失目标 / 取消 / 只读 | Reader 拒不存在 namespace 和已取消 context，调用前后对象/邻居事实不变，无参考 DDL、DDL 修复、连接泄漏或降级成功。 |

每个 drift 子例先证明 baseline equality 与 mutation 成功，再确认表数仍四、完整 gate 拒绝；轻量 ready 可继续成功，作为“不是 ready 门”的正控制，不能冒称完整结构 healthy。现邻居业务事实 snapshot 与新增结构证据共同证明隔离。语法/fixture 建立失败不算业务 RED；实际一次缺陷失败只证明对应字段，不能外推矩阵通过。

### Root-owned fixture 与清理

- 仅 Root 执行；沿用现独占测试 database 名 guard、随机 namespace、实际创建成功标志与有界 cleanup。目标/reference/邻居同一 Root-owned 临时库；不新增应用 database、role、Redis 实例或全局资源。
- reference 使用现 parser＋installer＋embedded Schema；不扫描目录或复制 SQL、不覆写 shared/public 或别人的 schema。每例独占 actual；neighbor sentinel 和原定义/事实前后保持。
- 先停止/等待本例 goroutine 与有界 reader rollback，关闭本例 pools，再按创建标志精确回收本例 schema；最后 Root 回收其独占 database/admin 资源。失败也走该顺序；不清共享 Redis、不 DROP 非自建 database/schema，不改变 role 权限。
- 当前已验收的并发双 backend 等锁与受控第二表 NOTICE/rollback 证据保持；新增 matrix 不能用它们代替。源码、测试、canonical SQL 和 cleanup 实现本阶段均锁定。

## R40-WIN09-schema：单库数据选址 cutover，待真实复验（2026-10-01）

状态：Root 已批准 D0 并取得三项真实 RED；当前精确实现显式 owner namespace 安装/连接与轻量 ready，未改 canonical SQL/API，基线 `9a4effd150ab739acaed2579faa94618bb26f772`，未提交。纯 Go 门已通过，真实 PG、完整 catalog 与并发/回滚扩展门待 Root。本节是唯一 namespace 方案；旧正文逐字保留，专用库/隐式选址叙述不作为当前实现依据，其余业务不变量有效。

### Owner 与连接

- 一 database、一套现有应用 role/credential、每 owner 独立 schema；Scheduler 仍只写四张自有事实表，不跨 owner SQL/JOIN，不新增 role/GRANT/部署进程。
- 唯一 canonical SQL 仍为 `database/schema.sql`，四表、字段、索引、CHECK、tenant 谓词、事务状态机及 retention 事实均不变；表名前缀不能代替 owner schema。
- 现 config 中的唯一纯 parser 必须先拒 raw 控制字符，再解析唯一 `schema`（`^[a-z][a-z0-9_]{0,62}$`，非 public/pg\_ 前缀）；拒空、重复、列表及 options/search_path/timezone 等覆盖入口与大小写变体。消费 selector 后统一显式 target search_path 与 UTC、保留普通 TLS，不回显秘密；runtime/installer 共用，无隐式 public。

### Fresh install 与失败恢复

- Bootstrap 显式接收目标 namespace，锁 key 是 database＋该 namespace，不能用可回落的 `current_schema()` 推导。锁、目标创建（仅缺失时）、对象空白判断、canonical DDL 和安装后置检查均在同一事务；任何失败整体回滚，只影响本次目标。
- 空白针对目标用户对象，而非整库或只针对 table：既有 view/materialized view/sequence/type/function 等也拒绝安装。邻居 schema 非空允许；邻居含相同四表名、sentinel 或其他事实时，前后原始快照必须一致。重复安装拒绝；同目标并发不产生半安装，不同目标不共用全库锁。
- 无 migration、自动 drift 修复、TRUNCATE、DROP database、别名或双写。已验收测试 fixture 的随机 schema、创建成功标志和 pool→自有 schema→admin cleanup 顺序保持；临时测试库仍只是 Root 资源隔离，不是新增应用数据库。

### Catalog 与验收

- 本 owner 只读快照包含 schema 内表、列、类型、默认值、约束和索引。期望由 Root 自有 reference namespace 执行同一 canonical SQL 得到；只消除 namespace/OID 身份、排序噪声，保留语义内容和 default/约束/index 定义，不盲替换字面量，不维护第二可编辑 schema 或 catalog。
- 正常 runtime 不执行参考 DDL。Startup/ready 只验证显式目标 namespace/UTC/四表，缺失即失败且不 fallback；完整 catalog 正确性仍由上述真实门验证，不用轻量 ping 或四表计数代替。
- 三个先 RED：现 config 测试拒非法/缺失 selector；现 integration 测试拒只有 view 的非空目标；同 integration 测试在目标缺失且邻居含同名表时拒 ready。其余 positive/并发/回滚/drift 断言及完整 PG 门由 Root 在自有资源上验证，尚未执行即保持待验。
- 当前已按授权将 bootstrap、production/smoke/fixture 一次切换为唯一 parser 产生的显式只读目标。安装用 Read Committed，在 database＋schema 事务锁获得后读取新对象快照，以目标 namespace 的 pg_depend 引用检查占用，再同事务选择目标/UTC、执行唯一 DDL及轻量后置检查；不从 current_schema 推 owner。完整只读 catalog 尚未实施/验收，类别、并发/回滚与同表数漂移扩展先由 Root 真实 RED，不把当前轻量后置检查当完整 drift 门；canonical SQL/API/业务状态机及原 fixture 核心断言保持。
# kokoro-scheduler 数据模型

唯一 current schema 是 [`../database/schema.sql`](../database/schema.sql)。本仓不保存 migration，不使用 `FOREIGN KEY`/`REFERENCES`。所有关系由 Application transaction、tenant predicate、业务唯一键和 reconciliation 维护。

## 1. `scheduler_schedule`

Durable tenant-scoped Schedule definition。

关键字段：`tenant_id`、`name`、`schedule_rule`、`timezone`、target snapshot、misfire/catch-up/overlap/retry policy、`status`、`next_due_at`、claim、`version`、timestamps。

不变量：

- `UNIQUE (tenant_id, name)`；
- active/paused、三种 misfire、allow/forbid 与 retry 范围均有 CHECK；
- `next_due_at` 是 UTC instant；rule/timezone 保留本地调度语义；
- claim owner/expiry 同时为空或同时存在；
- due partial index 为 `(next_due_at, id) WHERE status='active'`。

## 2. `scheduler_occurrence`

每个计划 instant 的 durable lifecycle 和可观测 outcome。

```text
pending -> dispatching -> succeeded
                      -> retrying -> dispatching
                      -> failed
planned -> skipped (misfire / bound / overlap)
```

`UNIQUE (tenant_id, schedule_id, scheduled_at)` 防止相同 Schedule instant 重复物化。`scheduled_at`、`observed_at`、`completed_at` 使用 `TIMESTAMPTZ(3)`；终态必须有 `completed_at`。`schedule_name` 是创建时快照，Schedule 删除后历史仍可解释。

## 3. `scheduler_command_receipt`

Append-only internal command outcome。

`UNIQUE (tenant_id, command_scope, idempotency_key)` 是 mutation replay identity。保存 request SHA-256 digest、原 request ID、稳定 result code 与 result JSON。没有 update path；调用方重试读取首次结果。

## 4. `scheduler_dispatch_outbox`

Occurrence 的 durable target snapshot、attempt 与 retry state。

```text
pending -> dispatching -> succeeded
                      -> retrying -> dispatching
                      -> failed
```

`UNIQUE (tenant_id, occurrence_id)` 保证一个 dispatchable occurrence 只有一个 outbox。ready partial index 覆盖 pending/retrying 的 `(next_attempt_at, id)`；claim-expiry index支持 crash recovery。`attempt_count <= max_attempts`、terminal completion、claim/status consistency 均由 CHECK 保护。

## 5. 原子关系维护

- Schedule 与 receipt：同一 command transaction；
- Occurrence 与 outbox：同一 planner transaction；
- outbox 与 occurrence 状态：同一 dispatch result transaction；
- schedule/outbox claim：SQL lock + owner + expiry；
- 所有读写显式 tenant predicate；内部 UUID 引用不由数据库外键耦合。

## 6. 时间、NULL 与 JSON

- 所有 instant：`TIMESTAMPTZ(3)`，写入前 `.UTC().Truncate(time.Millisecond)`；
- `NULL completed_at` 表示未终结；`NULL last_http_status/error` 表示尚无对应结果；
- `payload` 只保存 opaque JSON object snapshot，不替代核心查询列；
- 同毫秒稳定顺序使用 UUID `id` 作为第二排序键。

## 7. Retention

当前 schema 未实现自动 TTL、partition 或 GC。删除 Schedule 不级联删除 occurrence/outbox/receipt；已建立的 dispatch snapshot继续收敛。部署 owner 在真实容量、审计和 replay 窗口形成后，需要给出按 tenant/time 的 archival 与删除事务，并先补 integration/operational gate。禁止直接清空共享数据库作为日常处置。
