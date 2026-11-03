# 测试与验证边界

## 本次实际验收（2026-11-03）

环境：macOS x86_64，隔离的 Go 1.26.4 工具链（官方包 SHA-256 校验），CGO/SQLite。

以下命令已实际通过：

```bash
go mod verify
go vet ./...
go test ./...
go test -race ./...
go build -trimpath ./cmd/agent-core
./agent-core config validate --config agent-core.yaml
./agent-core config validate --config deploy/container.yaml
```

额外验收：

- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build` 与 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build` 均通过；这只验证平台分支可构建，不是这些系统的运行测试，生产 SQLite 仍需 CGO。
- `docs/openapi.yaml`、`docker-compose.yml`、`.github/workflows/ci.yml`、两份 AgentCore YAML 均通过 YAML 解析。
- 使用真实编译后的 `agent-core` 进程和本地 mock OpenAI Chat Completions 服务完成启动、health、SQLite 初始化、SSE 对话、CLI `status=completed`、doctor 和优雅关闭。
- 使用真实本地 MCP 服务分别验证 stdio、legacy SSE、streamable HTTP 的发现、调用和断连；验证 header、私网默认拒绝和 Connect 后会话生命周期。
- CLI 通过真实 HTTP/SSE 触发工具审批，stdin 允许后执行工具并收到 `done`。
- race detector 覆盖进程组取消、run 状态、SQLite、审批和 HTTP 测试；最终全量通过。

## 自动化测试覆盖

- **Agent**：不存在会话、流异常不 `done`、取消终态、副作用工具恢复策略、think 分片过滤。
- **存储**：最新日志顺序、cursor/FTS、run/event round-trip、旧库 migration backup、Host Callback 配置。
- **工具**：schema 拒绝、名称冲突、内建删除保护、输出限制、toolset、渐进发现、审批重复响应。
- **文件/沙箱**：revision/stale write、权限保留、glob/grep、路径逃逸、已有/祖先符号链接。
- **进程**：按 conversation 关联并终止 Unix 进程组，最终 race 通过。
- **记忆**：query 文本检索、容量、consolidation、disabled 向量过滤。
- **压缩**：Unicode/tool 预算、fallback 标记、关键目标/错误保留、极短输入。
- **HTTP/CLI**：鉴权、严格 JSON、CORS、readiness、SSE frame、限流、审批端到端。
- **MCP/Callback**：三传输、HMAC、响应大小、私网策略和会话生命周期。

## 0.3.0 扩展能力验收（2026-11-03）

在原有验收基础上，本轮再次实际通过：

- 全部包的标准测试、race detector 和 `go vet`。
- run 级 checkpoint、RunSpec、项目配置/Trust、Skills、Commands、完整文件树快照、Subagents、声明式 Workflow、model-io、benchmark 和 Web IDE 相关测试。
- 真实临时 Git 仓库、快照冲突/补偿恢复、Workflow 节点缓存恢复、SQLite 旧库迁移备份。
- 真实 HTTP 的 `/help`、Workspace 写入与恢复、Workflow 完成状态，以及同步/流式 provider wire 记录。
- 真实 0.3.0 二进制 + 本地 mock OpenAI 的端到端：Skills discovery、`/help`、Workspace 快照恢复、Workflow、trace export、benchmark 和 CLI，全部通过。
- `agent-core.yaml`、`deploy/container.yaml`、OpenAPI、Compose、CI YAML 再次解析通过；Windows/Linux 交叉编译通过。

端到端输出包含 `BINARY_E2E_0_3_PASS` 和 `status=completed`。与真实模型效果、Docker 镜像运行和多租户隔离相关的限制仍适用，未因新增功能而消除。

## 未在本次环境验收

- Windows 和 Linux 的实际运行、路径与进程树行为；Windows `taskkill /T` 仅实现并参与源码编译路径，未在 Windows 执行。
- Docker 镜像构建和 Compose 启动：当前环境没有 Docker，配置和 YAML 已检查，但不能声称镜像已运行。
- systemd、Caddy、logrotate 的真实服务器部署。
- 真实 OpenAI/Embedding provider、真实第三方 MCP 服务、OAuth、公网 DNS/SSRF 场景。
- 模型质量、长任务效果、性能/容量 benchmark、SQLite 超大数据量和高并发生产压测。
- 多租户隔离：本项目明确仍是单租户，不应据此推断已提供多用户授权。

Mock 验证证明协议接线和失败路径，不代表真实模型效果。部署到目标平台前，应运行同一命令集，并用目标 provider/MCP 做最小工具任务；生产还需独立安全审查、资源限制和恢复演练。
