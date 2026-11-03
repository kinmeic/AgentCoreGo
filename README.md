# AgentCoreGo

[![CI](https://github.com/kinmeic/AgentCoreGo/actions/workflows/ci.yml/badge.svg)](https://github.com/kinmeic/AgentCoreGo/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26.10-00ADD8?logo=go)](https://go.dev/dl/)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)

轻量级 Go AI Agent 服务端，支持 OpenAI 兼容接口与 Anthropic Messages API、MCP、权限审批、子代理与工作流编排，基于 SQLite 持久化。

面向自托管的编程助手和自动化任务，以 Go 单二进制提供 HTTP JSON/SSE API 与 CLI，可接入 IDE、自动化平台或自定义前端。服务采用单租户、单 workspace 模型，支持持久化工具执行、会话恢复和文件快照。

[快速开始](#快速开始) · [HTTP API](docs/api.md) · [配置说明](docs/configuration.md) · [安全模型](docs/security.md) · [部署运维](docs/operations.md)

## 已实现能力

- **模型接入**：OpenAI Chat Completions 与 Anthropic Messages 原生协议，同步/流式响应、工具调用，以及可分别配置协议的备用模型。
- **可靠 Agent 运行**：同步/流式共用执行路径，独立 `run_id`，统一 `done | error | cancelled` 终态、迭代/token/时长/并发预算。
- **会话与恢复**：完整 tool-call/tool-result 历史、版本化 SQLite 迁移、启动前备份、事件游标、原子工具 checkpoint、显式 resume。
- **工具与权限**：离线 JSON Schema 校验、能力声明、plan/build/edit/guarded/yolo、异步审批、会话级授权、静态 deny 优先。
- **Coding 工具**：原子写、revision 冲突检测、精确编辑、删除/移动、glob/grep、只读 Git、进程组取消。
- **真实 MCP**：官方 Go SDK，stdio、legacy SSE、streamable HTTP，工具发现/调用、版本协商、断连清理和 `mcp-serve`。
- **扩展与记忆**：Host Callback HMAC、长期记忆 CRUD/检索/consolidation/索引重建、可插拔向量开关。
- **上下文管理**：Unicode/tool schema 预算、分级压缩、可持久化摘要、跨 chunk `<think>` 过滤、failover。
- **工程能力**：薄 CLI、严格配置、CORS/限流/请求上限、结构化日志、readiness、指标、测试与容器部署。
- **扩展生态**：项目配置层级与 Workspace Trust、Skills、内置/自定义 Slash Commands、RunSpec 固定扩展版本。
- **任务编排**：Subagents、Explore 只读约束、声明式 YAML/JSON DAG Workflow、fan-out、有界重试、节点缓存与恢复。
- **快照与 IDE 集成**：完整文件树快照、冲突检查、内容完整性校验与补偿恢复，提供 Workspace/Git/运行/变更管理 API。
- **model-io 与评测**：原始 provider wire、工具/文件轨迹、redacted/training 采集、独立隔离 benchmark 与 grader。

> **安全边界**：文件工具有 workspace 路径与符号链接防护；终端命令拥有 AgentCore 进程的操作系统权限，**不是 OS 沙箱**。非 loopback 监听必须设置 `AUTH_TOKEN` 且禁用终端。当前会话、记忆和工具注册表是单租户共享状态，不是多租户隔离边界。

## 快速开始

### 前置条件

- Go **1.26.10 或更高版本**（最低版本与 `go.mod` 一致）
- CGO 与 C 编译器（SQLite 驱动需要）

Ubuntu 可安装：

```bash
sudo apt update
sudo apt install -y build-essential gcc ca-certificates
```

### 构建与配置

```bash
git clone https://github.com/kinmeic/AgentCoreGo.git
cd AgentCoreGo
go build -o agent-core ./cmd/agent-core

export LLM_API_KEY='your-api-key'
export AUTH_TOKEN='a-long-random-local-token'
# 如使用向量记忆：
export EMBEDDING_API_KEY="$LLM_API_KEY"

./agent-core config validate --config agent-core.yaml
./agent-core config show --config agent-core.yaml  # 密钥自动脱敏
```

编辑 [agent-core.yaml](agent-core.yaml)。`workspace.path` 是所有 workspace 工具的唯一目录来源；旧 `tools.work_dir` / `WORK_DIR` 仅作兼容映射，不应与 workspace 设置成不同路径。

按使用的模型服务设置 `llm.base_url`、`llm.model` 和 `LLM_API_KEY`。工作区默认位于 `./workspace`，SQLite 数据库默认位于 `./data/agent.db`。API key 通过环境变量传入。

### 使用 Anthropic Messages

将配置中的 `llm` 切换为原生 Messages 格式，并填写有权限访问的 Claude 模型 ID：

```yaml
llm:
  api_format: anthropic
  base_url: https://api.anthropic.com/v1
  api_key: "${LLM_API_KEY}"
  model: your-claude-model-id
  max_output_tokens: 4096
```

使用 `LLM_API_KEY` 注入 Anthropic key。也可用 `LLM_API_FORMAT`、`LLM_BASE_URL`、`LLM_MODEL` 覆盖对应配置；已有配置未指定协议时继续使用 OpenAI 格式。当前 Anthropic 适配支持文本和客户端工具调用，显式禁用 extended thinking；完整参数说明与混合协议 fallback 示例见 [配置文档](docs/configuration.md#anthropic-messages)。

### 启动

```bash
./agent-core -config agent-core.yaml
```

默认监听 `127.0.0.1:8900`。检查：

```bash
curl http://127.0.0.1:8900/health
curl -H "Authorization: Bearer $AUTH_TOKEN" http://127.0.0.1:8900/ready
./agent-core doctor --config agent-core.yaml
```

### Docker Compose

安装 Docker 与 Compose 后，可使用仓库中的容器配置启动：

```bash
export AUTH_TOKEN='a-long-random-local-token'
export LLM_API_KEY='your-api-key'
# 使用向量记忆时，同时设置 EMBEDDING_API_KEY
docker compose up -d --build

curl http://127.0.0.1:8900/health
```

容器使用 [deploy/container.yaml](deploy/container.yaml)，默认禁用终端工具；宿主机端口仅绑定 `127.0.0.1:8900`，数据与工作区保存在独立 Docker volumes 中。使用其他模型服务时，修改容器配置中的模型地址和名称。生产部署与备份见 [运维文档](docs/operations.md)。

## CLI

```bash
# SSE 对话；遇到审批或澄清时在终端应答
./agent-core chat --message '解释当前项目结构'
./agent-core chat --conversation "$CONVERSATION_ID" --message '继续'

# 同步 JSON
./agent-core chat --stream=false --message '你好'

# 停止与恢复；副作用工具中断后需要显式 retry 或 skip
./agent-core stop --run "$RUN_ID"
./agent-core resume --run "$RUN_ID"
./agent-core resume --run "$RUN_ID" --strategy retry

./agent-core conversations
./agent-core logs --conversation "$CONVERSATION_ID" --query '错误'
./agent-core tools
./agent-core memory list
./agent-core memory add --content '用户偏好简洁回复'
./agent-core memory consolidate
./agent-core mcp list
./agent-core mcp-serve --config agent-core.yaml --transport stdio
```

远程地址和 token 可用 `--url`、`--token`，或 `AGENT_CORE_URL`、`AUTH_TOKEN`。详见 [CLI 文档](docs/cli.md)。

## HTTP 示例

```bash
curl -N -X POST http://127.0.0.1:8900/v1/chat/stream \
  -H "Authorization: Bearer $AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message":"帮我检查这个项目"}'
```

SSE 每帧包含 `id`、`event` 和 JSON `data`。事件 envelope 含 `schema_version`、`event_id`、`sequence`、`conversation_id`、`run_id`、`timestamp`；连接每 15 秒发送心跳。运行事件也可通过 `/v1/runs/{id}/events?after=<sequence>` 获取。

```bash
# 查询运行与事件
curl -H "Authorization: Bearer $AUTH_TOKEN" \
  http://127.0.0.1:8900/v1/runs/$RUN_ID
curl -H "Authorization: Bearer $AUTH_TOKEN" \
  "http://127.0.0.1:8900/v1/runs/$RUN_ID/events?after=0"

# 恢复；同步请求不可处理交互审批，交互场景使用 stream=true
curl -N -X POST "http://127.0.0.1:8900/v1/runs/$RUN_ID/resume" \
  -H "Authorization: Bearer $AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"strategy":"retry","stream":true}'
```

完整接口见 [API/SSE 文档](docs/api.md) 和 [OpenAPI 草案](docs/openapi.yaml)。

## 文档

- [配置与环境变量](docs/configuration.md)
- [Skills 与 Slash Commands](docs/commands.md)
- [项目配置、Trust、Subagents 与 Workflow](docs/extensions.md)
- [Web IDE 与快照](docs/web-ide.md)
- [model-io 与自动化轨迹](docs/trajectory.md)
- [Benchmark 体系](docs/benchmark.md)
- [权限模式与安全策略](docs/permissions.md)
- [MCP 与 Host Callback](docs/mcp.md)
- [安全模型](docs/security.md)
- [CLI](docs/cli.md)
- [部署、备份与运维](docs/operations.md)
- [测试与验证边界](docs/testing.md)
- [2026-11-03 代码审计与修复](docs/audit-2026-11-03.md)

## 验证

```bash
gofmt -w ./cmd ./internal
go vet ./...
go test ./...
go test -race ./...
go build ./cmd/agent-core
```

测试包含 fake LLM、真实 SQLite、真实 stdio/SSE/streamable HTTP MCP 服务、HTTP 合同、文件/进程组以及 CLI 审批端到端场景。实际验证范围和未覆盖平台见 [testing.md](docs/testing.md)。

## 主要目录

```text
cmd/agent-core/          服务与 CLI 入口
internal/agent/         运行循环、取消、checkpoint
internal/llm/           OpenAI 兼容客户端、流式解析、failover
internal/tool/          注册、schema、权限、发现、进程管理
internal/tool/builtin/  文件、搜索、Git、终端、网页工具
internal/mcp/           MCP client/server
internal/store/         SQLite 迁移、run/event/log、向量存储
internal/memory/        记忆服务与工具
internal/compressor/    上下文预算与摘要
internal/server/        HTTP/SSE、中间件与管理 API
deploy/                 systemd、Caddy、容器配置
docs/                   使用与运维文档
```

## License

本项目采用 **BSD 3-Clause License**，详见 [LICENSE](LICENSE)。
