## R46 Root namespace/fullcatalog 当前验收证据（2026-10-01）

候选基线 main 9a4effd150ab739acaed2579faa94618bb26f772，完整20path、唯一canonical4表不变。Root Go1.26.8/GOMAXPROCS=2：gofmt输出为空、go vet -p1 ./...、go build -p1 ./...均exit0；完整go test -p1 -count1 -timeout120s -v ./...实际167pass/25既有资源skip（含子例），/tmp/kokoro-scheduler-r46-root-pure.log。资源skip不计通过。

Root本机PG18.4/既有nako同role，临时owned数据库内真正namespace/canonical参考、16drift/5extra-object/取消及原安装并发/DDL回滚守卫：24顶层PASS/0FAIL/0SKIP，2.632s；/tmp/kokoro-scheduler-catalog-r46-root-resource.log。独立审原17全文/installer/14锁定路径保持、fixture P1闭合0P0/P1/P2，source b2706161/test6ad315dc。原R45首轮18pass6fixturefail完整保留（不是catalog行为RED），未抹去失败。

Root真实源码binary启动→写Schedule/receipt→停止→重启→同key精确重放：1PASS1.63s（包1.976s），/tmp/kokoro-scheduler-restart-r46-root-resource.log。两新资源记录created/closed=true/exit0（catalog owned kokoro_scheduler_test_r45_2293f25380ea，restart owned kokoro_scheduler_test_r46_c580f8fdb189）；无共享清理、无新role/长期service。Redis可选恢复、PG16 CI、镜像及BFF独立定时任务完整用户旅程本次未验，不称全体系/部署已闭环。ReadSchemaCatalog只读RR/UTC/有界rollback，无运行期reference DDL；轻量ready语义保持。

本20path切片由Root准备index复审/提交，当前段不是提前发布声明；下方为历史候选/失败与原body，按本段当前真实证据解释。

## R45-WIN09：fixture 修正已静态冻结，待 Root 重跑（2026-10-01）

- Root 首轮实际 **24 顶层：18 PASS / 6 FAIL，1.388s**；六个新资源用例在 occurrence 无 sentinel 的 beforeSnapshot 失败，未到 catalog 目标行为，不计行为 RED。日志 `/tmp/kokoro-scheduler-catalog-r45-root-resource.log`；Root 报告自有 `kokoro_scheduler_test_r45_cff1270f3918` 已回收。
- 本轮仅新增测试五个 fixture 入口补现 full sentinel seed，覆盖六个资源用例；原 17 全文与全部原断言不变。bootstrap SHA256 `b2706161ae535efe2e105f04fb65eaf7c5546332b8e19a4a868b2fea2ceff46f` 保持，四文档仅此失败证据前缀，原正文完整保留。
- Go 1.26.8/GOMAXPROCS=2/GOPROXY=off，数据库/Redis URL 清除；gofmt 输出为空，vet/build/无测试编译/收集均退出 0，仍收集 24 顶层、行为执行 0。日志 `/tmp/kokoro-scheduler-r45-fixture-{compile,vet,build,collect}.log`。
- owner 基线仍 `9a4effd150ab739acaed2579faa94618bb26f772`，未提交；无 PG/Redis/Git/服务操作。修后真 PG、完整 catalog 矩阵及独立源码审由 Root 续接，不宣称实现已通过。下方全文保持。

## R44-WIN09：catalog 源码/tests 候选冻结，待真实 PG（2026-10-01）

- owner 基线仍 `main / 9a4effd150ab739acaed2579faa94618bb26f772`，未提交；Root 报告 D0 独立审通过并授权现 bootstrap＋integration＋四文档小前缀。下方 R43 与全部历史正文保持，源码候选当前已具名实现，不再是 missing reader 阶段。
- 真实 `ReadSchemaCatalog` / typed `SchemaCatalog` / 纯 `CompareSchemaCatalog` 已可编译，单 session readonly RR、15 秒读取边界、有界 rollback、目标/UTC/事务状态实检。表达式/literal 原样，关联 OID 只转逻辑身份；未知对象失败。无修改 canonical SQL、installer 原流程或 Store/Ping/runtime/CLI。
- 现 integration 保留原 17 全文，新增 7 顶层用例，合计 24 可收集；drift 16 子例、extra 5 子例及 function/type、reference、cancel 边界尚未执行。
- Go 1.26.8，GOMAXPROCS=2/GOPROXY=off，数据库/Redis测试 URL 明确清除；gofmt 检查输出为空，`go vet -p 1 ./...`、`go build -p 1 ./...`、`go test -p 1 -run '^$' -count=1 ./...`、`go test -p 1 -list '^Test' ./test/integration` 均退出 0。这里只证明编译/静态/收集，执行行为测试数量为 0。
- 日志 `/tmp/kokoro-scheduler-r44-catalog-{compile,vet,build,collect}.log`；原 Root 17 PG PASS 证据保持，不代表新结构矩阵。本轮无 PG/Redis/Git/服务操作，没有资源 RED/GREEN 或发布宣称。
- 待验与后续 owner：Root 独占资源实际跑正例/漂移/分类/取消矩阵与原 17 回归、独立源码审及提交；原 Scheduler writer 按实际失败再接受精准修正授权。其余锁定路径及四文档旧正文保持。

## R43-WIN09-fullcatalog D0：四文档冻结，未实施（2026-10-01）

- owner 当前基线 `main / 9a4effd150ab739acaed2579faa94618bb26f772`，Root 任务卡基线 `14c70d794b69428ef012c07ca38804b62f77b5cc`；未提交。本轮仅 prepend TECHNICAL_DESIGN/API_CONTRACT/DATA_MODEL/CURRENT 的 R43 节，四文件既有 namespace/current/历史全文原样保留。20 路径基线中其余 16 文件不变，包括 RUNBOOK、源码和测试。
- 当前成熟 SQL loader 是 `database/schema.go` 的 embed；现 `ApplySchemaToEmptyDatabase` 事务执行 `database.Schema`。显式不可变目标、唯一 URL parser、目标锁/pg_depend 空白检查、事务 rollback 和 namespace/UTC/四表 ready 已存在。只读结构 catalog reader/comparator 仍未实现，四表计数不代表结构完整。
- 引用 Root 已实际执行的资源证据：`/tmp/kokoro-scheduler-schema-r42-root-resource-green.log`，**17 顶层 PG PASS / 0 skip / 0 fail，1.110s**，包括并发双 backend 精确锁等待的一胜一拒及受控第二表 NOTICE/安装回滚。Root 已报告自有临时库创建与回收。此证据未覆盖完整 catalog 或新增 function-only/type-only 分类测试；不是本轮新执行结果。
- D0 方案：现 bootstrap 内只读一致快照，现 integration 内 Root-owned actual/reference；参考由唯一 canonical SQL 的既有安装入口构建，不维护第二 editable expected list；仅结构身份/OID/集合次序归一，保留 literal/default/CHECK/index 全部语义。runtime 无参考 DDL、完整 drift ready 或自动修复。
- 文档门待 Root 审查；未决：冻结内部 callable 接入与可编译真实采集基线的精确授权，之后再写行为 RED/实现 GREEN。不存在符号的 compile failure 不算 RED；现 function/type occupancy 若已通过则如实记 GREEN。DATA_MODEL R43 已列精确矩阵与 Root-owned cleanup。
- 本轮没有运行 Go 测试、PG/Redis/source process，没有修改源码、测试、SQL、generated、依赖、流程或 Root 文件，没有 Git 操作；仅静态哈希、历史全文后缀与冻结范围核验。新完整结构 gate、真实 RED/GREEN、独立审查、集成 commit 均待验，不宣称 Scheduler 全部完成。后续 owner：Root 放行/资源/提交，Scheduler 原负责人按授权续接。

## R40-WIN09-schema：显式 owner namespace source cutover，待 Root 真实 GREEN（2026-10-01）

- 基线 `main / 9a4effd150ab739acaed2579faa94618bb26f772`，本切片未提交。Root 四文档及独立审已放行精确实现；原 R38 fixture 验收证据仍有效，不冒充本生产切换通过。
- Root 配置 R1 实际31非法 case失败，TLS/NUL/DEL控制通过：`/tmp/kokoro-scheduler-schema-r40-root-config-red.log`。首次 PG 因新增 snapshot 误表名遮挡，保留 helper-failed 证据不计业务 RED；修正为 canonical scheduler_dispatch_outbox 后 R2/R3 实际2failed/0skip（0.375s）：view-only竟安装4表、缺 namespace Ping成功/ready200。日志 `/tmp/kokoro-scheduler-schema-r40-root-resource-red.log`，Root-owned临时库已回收。
- 当前 config.go 唯一纯 `ParseDatabaseURL` 返回不可变 `DatabaseTarget`；raw Unicode controls先于trim，唯一安全schema/保留键校验，消费selector并生成owner-only search_path/UTC，保普通TLS。runtime/installer共用，无裸URL/默认namespace/第二parser；安装入口不加载Redis/runtime。两入口与所有授权fixture/smoke调用点已同步，CI/release仅追加授权生产URL selector，测试base URL保持。
- Bootstrap显式target、database＋schema锁、目标namespace对象空白/缺失创建/唯一canonical DDL/轻量后置检查同事务、失败rollback；Store.Ping在同一acquired session检查namespace/实际UTC/四事实表。原Runtime/Redis健康条件、canonical4表、API、业务repository/状态机、fixture独占/创建标志/精确清理及旧业务与R1/R2R3核心断言保持。
- Worker新增归一化/真实driver正例先2子例真实RED：`/tmp/kokoro-scheduler-r40-parser-normalization-red.log`。当前 Go1.26.8，GOMAXPROCS=2/GOPROXY=off，数据库/Redis测试URL明确清除；`go test -p 1 -count=1 -timeout=90s ./... -v`实际166pass/17skip/0fail（含子例；85顶层pass），日志 `/tmp/kokoro-scheduler-r40-namespace-pure-green.log`；gofmt输出为空、`go vet -p 1 ./...`、`go build -p 1 ./...`退出0，日志 `/tmp/kokoro-scheduler-r40-namespace-static.log`。跳过的PG/Redis/source-process不算资源通过。
- 未闭环：Root对冻结source重新真实PG/完整Go/源码进程与独立审；只读完整catalog未实施，必须先冻结并真实验证reference/同表数列/default/constraint/index漂移断言，再实现结构快照归一；并发安装、回滚和view/function/type类别扩展真实门仍待Root。无新文件/目录/依赖/role/process、未操作资源/Git，不宣称完整Scheduler或单库组合验收，不提交发布半cutover。后续owner为Root＋Scheduler原负责人。
## WIN09-R31：cron 大 step 整数溢出局部修复（2026-10-01）

基线 main 975dee59616a1e0eda609aa69283401344900d83。现 recurrence/cron 在递增前检查剩余范围，避免合法大正整数 step 溢出导致下一次分钟提前；原 Sunday 0/7 归一及标准范围行为保持。现 calculator_test 新最大 int/最大 int-1 两案例；旧实现一子例失败，worker 聚焦20主测试/11子例通过，独立审查本片0P0/P1/P2。契约、Schema、依赖及进程无变动。

Root 当前主树 GOMAXPROCS=2/GOPROXY=off：go vet ./...、go test -p 1 -count=1 -timeout=90s ./... -v、go build ./... 全退出0；**82 主测试 passed /12 既有资源 skip /0 failed**；两文件 gofmt 输出为空，diff-check 通过；日志 /tmp/kokoro-r31-scheduler-root-full.log。真实PG/Redis/源码重启集成本轮未运行，局部时间规则修复不等于独立任务整体闭环。只提交本两文件及本 CURRENT，其他 owner 修改保留。

# kokoro-scheduler 当前状态

更新日期：2026-09-04。本文只描述当前工作树对应的实现；最终证据以提交后的命令输出为准。

## 当前定位

Scheduler 是 Go 内部服务，唯一生产入口为 `cmd/scheduler/main.go`。PostgreSQL 是 Schedule、Occurrence、command receipt 和 dispatch outbox 的唯一事实源。gocron/v2 只按固定间隔唤醒扫描器；Redis DB 7 可选，只承担短期协调。

| Surface | 当前实现 |
|---|---|
| Schedule | tenant-scoped PostgreSQL row；保存本地 recurrence rule、IANA timezone、状态、版本和 `next_due_at` |
| Command receipt | `(tenant_id, command_scope, idempotency_key)` 唯一；同 digest 精确 replay，异 digest 冲突 |
| Due scan | PostgreSQL transaction + `FOR UPDATE SKIP LOCKED` + expiring claim |
| Occurrence/outbox | 同一事务原子写入；业务唯一键阻止重复 occurrence/outbox |
| Misfire | `skip`、默认 `fire_once`、`catch_up_bounded`；跳过和 bound exceeded 都持久化 outcome |
| Overlap | `allow` 或默认 `forbid`；禁止重叠时写 `SCHEDULER_OVERLAP_BLOCKED`，不静默丢弃 |
| Dispatch | POST/PUT JSON；稳定 occurrence identity；timeout/cancellation；持久化 attempt/status/error |
| Retry | 408、425、429、5xx、网络/timeout 可重试；其余 HTTP 与 caller cancellation 为永久结果；指数退避 + full jitter + attempt/window 上限 |
| Recovery | 启动立即扫描；expired schedule/outbox claim 可重领；final-attempt crash 转为 durable failed outcome |
| Contract | canonical OpenAPI 3.1 + runtime parity + compatibility signature + SHA-256 provenance |
| Schema install | `db:apply-schema` 只接受空 database namespace，并安装唯一 `database/schema.sql` |

## Recurrence 实现范围

本仓明确实现五字段 cron 和 `@every`，不把 timer 库当 recurrence 事实源。支持 `*`、list、range、step、英文月/星期名、Sunday 0/7 以及常用 descriptor。DOM/DOW 使用标准 OR 语义；`*/1` 视为未限制 selector。不存在的日期在保存前拒绝。

IANA timezone 在保存时验证。具体 occurrence 为 UTC 毫秒 instant：spring-forward 不存在的 wall time 不触发，fall-back 两个相同 wall time 对应两个 UTC occurrence。cron 搜索 horizon 为 10 年；`@every` 为 1 秒至 366 天且保持原 due anchor。

## 已知限制与风险

1. v1 没有 Schedule/Occurrence 查询 API；OpenAPI 描述 occurrence lifecycle 和 dispatch webhook，但不虚构读取接口。
2. inbound authorization 当前是共享 Bearer service token；tenant 来自受信 header，尚未接入 caller identity/细粒度 IAM permission。
3. dispatch 是 at-least-once。worker 在目标已接收后、状态提交前崩溃会以相同 `Idempotency-Key` 重投；目标 owner 必须持久化去重。
4. 表 retention/archival/GC 尚未实现；receipt、occurrence 和 outbox 会持续增长，需要部署 owner 在容量数据形成后确定策略。
5. 当前只有结构化 background/dispatch 日志和 probes，没有 metrics exporter、dashboard 或告警规则；`SLO.md` 是目标契约，不是实测证明。
6. Redis lease 不是真相且不做长任务续租；配置强制 `claim_ttl > dispatch_timeout`。超出该模型的长任务应由目标 command 异步化。
7. recurrence 是本仓受测试约束的窄实现，不支持秒字段、`?`、`L`、`W`、`#`、`@reboot` 或 embedded timezone prefix。

## 证据入口

- 单元/架构/schema：`go test ./...`、`go test ./test/architecture ./test/schema`；
- contract：`./scripts/contract-check`；
- PostgreSQL/Redis/restart：`docs/ACCEPTANCE.md` 中带独立测试 database 的命令；
- module：`go list -m github.com/go-co-op/gocron/v2`、`go mod graph`、定向 `rg`；
- fresh schema：在临时空 database 运行 `./scripts/db-apply-schema` 后检查四张表。
