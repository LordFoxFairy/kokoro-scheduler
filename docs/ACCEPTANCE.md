# kokoro-scheduler 验收清单

## 1. 自动化门禁

从仓库根目录执行：

```bash
gofmt -w .
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
go build ./...
```

CI 还执行 `go mod verify`、`go test -race ./...`、生产 binary/image build 和安全扫描。Redis integration 需要
复用真实 Redis 并显式设置 `SCHEDULER_REDIS_TEST_URL`；没有该变量时测试输出中的 skip 必须如实报告。

## 2. Contract 检查

本仓行为与路由 contract：

```bash
go test ./internal/architecture ./internal/transport/http
```

在 Kokoro Root 工作区内，使用 Root canonical governance checker 定向检查本仓 OpenAPI 与 provenance：

```bash
PYTHONPATH=.. python3 - <<'PY'
import hashlib
import re
from pathlib import Path

from scripts.governance.contract_checks import check_openapi_contract
from scripts.governance.ten_repository_standard import (
    missing_contract_readme_fields,
)

repository = Path.cwd()
failures = []
check_openapi_contract(
    "kokoro-scheduler",
    repository / "contract/openapi/v1/openapi.yaml",
    failures,
)
missing = missing_contract_readme_fields(
    (repository / "contract/README.md").read_text(encoding="utf-8")
)
specification = (repository / "contract/openapi/v1/openapi.yaml").read_bytes()
readme = (repository / "contract/README.md").read_text(encoding="utf-8")
recorded_digest = re.search(r"^sha256:([0-9a-f]{64})$", readme, re.MULTILINE)
actual_digest = hashlib.sha256(specification).hexdigest()
provenance_ok = (
    recorded_digest is not None and recorded_digest.group(1) == actual_digest
)
if failures or missing or not provenance_ok:
    raise SystemExit(
        "contract check failed: "
        + repr(
            {
                "openapi": [item.detail for item in failures],
                "readme": missing,
                "provenance_digest_matches": provenance_ok,
            }
        )
    )
print("kokoro-scheduler contract governance: PASS")
PY
```

该检查只读 Root 工具，不修改或用其他仓的结果替代 Scheduler 门禁。

## 3. 场景矩阵

| 场景 | Fixture/检查 | 结果标准 |
|---|---|---|
| 合法配置 | `internal/application.LoadJobs` | 完成注册并填充默认值 |
| 非法配置 | 未知/重复/null 字段、重复名称、非法 cron、retry 零值/越界 | 启动失败 |
| register | `POST /internal/scheduler/v1/jobs/{name}` | token、request ID、idempotency key 通过后注册通用 job |
| update/delete | `PUT` / `DELETE` | 仅更新/删除已存在 job；缺失返回明确 404 |
| mutation replay | 同 key 同 payload、同 key 改 payload、不同 key 重复 POST | replay 原响应、409 conflict、409 already exists |
| strict boundary | 错误 Content-Type、类型、trailing JSON、body > 1 MiB | 请求被拒绝且返回 request_id |
| pause/resume | `/pause`、`/resume` | 控制后续 occurrence，不取消 in-flight dispatch |
| dispatch success | target mock 返回 2xx | succeeded，携带 occurrence/request/idempotency/trace metadata |
| target auth | 配置/清空 `SCHEDULER_TARGET_SERVICE_TOKEN` | 非空发送独立 Bearer；空值省略 Authorization |
| target failure | mock 返回 4xx/429/5xx 或网络错误 | 只重试网络错误、429、5xx |
| retry backoff | 注入 Clock、Sleeper、RandomSource | exponential ceiling 受 max backoff/window 限制并使用 full jitter |
| retry deadline | 推进 Clock 到 deadline | 不启动越过总窗口的下一 attempt |
| misfire | 显式 `scheduledAt` / `observedAt` | `skip` 丢弃，`fire_once` 最多继续一次显式 occurrence |
| single-instance overlap | cron wrapper | 同一 entry still-running 时跳过下一触发 |
| multi-instance | 真实 Redis lease test | 同一 occurrence 只获得一个 claim；token-safe renew/release |
| readiness | 无 Redis / 配置 Redis | lifecycle 与 deadline-bound PING 决定 200/503 |
| shutdown | 空配置进程 + SIGTERM | readiness 关闭，cron stop，in-flight 在 deadline 内收敛 |
| ownership | 目录/contract 检查 | 无 PostgreSQL/业务 Schema/业务 `ScheduledTask` ownership |

## 4. 文档与发布核对

- `INDEX.md` 与 `docs/INDEX.md` 只指向存在的规范大写入口；
- `contract/README.md` 包含 owner、visibility、version、generation、breaking、provenance；
- 每个 OpenAPI operation 含五个 `x-kokoro-*` 扩展；
- 当前缺口仍在 `CURRENT.md`，SLO 目标没有冒充实测结果；
- 最终报告列出 branch、commit、变更文件、命令退出码和 Redis skip/执行状态。
