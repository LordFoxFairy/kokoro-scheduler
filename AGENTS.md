# kokoro-scheduler 子仓 Agent 规范

@../AGENTS.md

本仓是独立 Go Scheduler owner，负责 schedule、occurrence、lease、retry 和 dispatch；只发送业务 command，不拥有 Billing、Credit 或 Agent 的业务事实。规则已经明确时直接执行，不重复向用户确认。

目标目录：

```text
cmd/scheduler/
internal/domain/
internal/application/
internal/ports/
internal/adapters/
internal/transport/
```

- `domain` 不依赖 HTTP、Redis、数据库和第三方 SDK；`application` 编排用例；`ports` 定义 Clock、LeaseStore、TargetClient；具体实现放 `adapters`。
- 时间使用 `time.Time`，进入领域层统一 `.UTC()`；周期规则保留 IANA timezone，具体 occurrence 使用 UTC instant。
- Scheduler 不连接业务数据库；Redis lease 必须可恢复，业务成功以目标 owner 的 durable receipt 为准。
- 生产代码禁止 Fixture/Fake；测试替身放 `test/fixtures/` 或 `test/doubles/`。

完成前执行：

```bash
gofmt -w .
go vet ./...
go test ./...
go build ./...
```
