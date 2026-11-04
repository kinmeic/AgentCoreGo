# AgentCoreGo

[![CI](https://github.com/kinmeic/AgentCoreGo/actions/workflows/ci.yml/badge.svg)](https://github.com/kinmeic/AgentCoreGo/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26.10-00ADD8?logo=go)](https://go.dev/dl/)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)

轻量级 Go AI Agent 服务端，支持 OpenAI 兼容接口与 Anthropic Messages API、MCP、权限审批、子代理与工作流编排，基于 SQLite 持久化。

面向自托管的编程助手和自动化任务，以 Go 单二进制提供 HTTP JSON/SSE/WebSocket API 与 CLI，可接入 IDE、自动化平台或自定义前端。服务采用单租户、单 workspace 模型，支持持久化工具执行、会话恢复和文件快照。

[快速开始](#快速开始) · [HTTP API](docs/api.md) · [配置说明](docs/configuration.md) · [安全模型](docs/security.md) · [部署运维](docs/operations.md)

## 已实现能力

- **模型接入**：OpenAI Chat Completions 与 Anthropic Messages 原生协议，同步/流式响应、工具调用，以及可分别配置协议的备用模型。
- **可靠 Agent 运行**：同步/流式共用执行路径，独立 `run_id`，统一 `done | error | cancelled` 终态、取消、流空闲超时与并发控制，主 Agent 不设迭代/token/总时长硬停止。
- **会话与恢复**：完整 tool-call/tool-result 历史、版本化 SQLite 迁移、启动前备份、事件游标、原子工具 checkpoint、显式 resume。
- **工具与权限**：离线 JSON Schema 校验、能力声明、plan/build/edit/guarded/yolo、异步审批、会话级授权、静态 deny 优先。
- **Coding 工具**：原子写、revision 冲突检测、精确编辑、删除/移动、glob/grep、只读 Git、进程组取消。
- **真实 MCP**：官方 Go SDK，stdio、legacy SSE、streamable HTTP，工具发现/调用、版本协商、断连清理和 `mcp-serve`。
- **扩展与记忆**：Host Callback HMAC、长期记忆 CRUD、本地文本检索与 consolidation，无 embedding 依赖。
- **上下文管理**：Unicode/tool schema 预算、分级压缩、可持久化摘要、跨 chunk `<think>` 过滤、failover。
- **工程能力**：薄 CLI、严格配置、CORS/限流/请求上限、结构化日志、readiness、指标、测试与容器部署。
- **扩展生态**：项目配置层级与 Workspace Trust、Skills、内置/自定义 Slash Commands、RunSpec 固定扩展版本。
- **任务编排**：Subagents、Explore 只读约束、声明式 YAML/JSON DAG Workflow、fan-out、有界重试、节点缓存与恢复。
- **快照与 IDE 集成**：完整文件树快照、冲突检查、内容完整性校验与补偿恢复，提供 Workspace/Git/运行/变更管理 API。
- **model-io 与评测**：原始 provider wire、工具/文件轨迹、默认脱敏诊断采集、独立隔离 benchmark 与 grader。

> **安全边界**：文件工具有 workspace 路径与符号链接防护；终端命令拥有 AgentCore 进程的操作系统权限，**不是 OS 沙箱**。非 loopback 监听必须设置 `AUTH_TOKEN` 且禁用终端。当前会话、记忆和工具注册表是单租户共享状态，不是多租户隔离边界。

## 快速开始

可从 [GitHub Releases](https://github.com/kinmeic/AgentCoreGo/releases) 下载 Linux、macOS、Windows 和 OpenWrt 预编译包。平台、校验方式和 Actions 发布流程见 [发版文档](docs/release.md)。

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
export LLM_MODEL='your-model-id'

./agent-core config validate --config agent-core.yaml
./agent-core config show --config agent-core.yaml  # 密钥自动脱敏
```

编辑 [agent-core.yaml](agent-core.yaml)。`workspace.path` 是所有文件工具与 `AGENTS.md` 的统一根目录。运行配置对齐 ZCode v3.17.2 / `a9fe401e`；旧字段须按[迁移对照表](docs/zcode-alignment.md)更新。

按使用的模型服务设置 `llm.base_url`、`llm.model` 和 `LLM_API_KEY`。工作区默认为当前目录，SQLite 数据库默认位于 `./data/agent.db`。API key 通过环境变量传入。

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

使用 `LLM_API_KEY` 注入 Anthropic key。也可用 `LLM_API_FORMAT`、`LLM_BASE_URL`、`LLM_MODEL` 覆盖对应配置；已有配置未指定协议时继续使用 OpenAI 格式。Anthropic 适配支持文本、客户端工具调用及可配置的 manual/adaptive thinking；思考正文通过独立 `reasoning` 流事件公开，签名与 redacted 数据仅供协议连续性使用。完整参数说明与混合协议 fallback 示例见 [配置文档](docs/configuration.md#anthropic-messages)。

### 启动

```bash
./agent-core -config agent-core.yaml
```

默认监听 `127.0.0.1:3030`。检查：

```bash
curl http://127.0.0.1:3030/health
curl -H "Authorization: Bearer $AUTH_TOKEN" http://127.0.0.1:3030/ready
./agent-core doctor --config agent-core.yaml
```

### Docker Compose

安装 Docker 与 Compose 后，可使用仓库中的容器配置启动：

```bash
export AUTH_TOKEN='a-long-random-local-token'
export LLM_API_KEY='your-api-key'
export LLM_MODEL='your-model-id'
docker compose up -d --build

curl http://127.0.0.1:3030/health
```

容器使用 [deploy/container.yaml](deploy/container.yaml)，默认禁用终端工具；宿主机端口仅绑定 `127.0.0.1:3030`，数据与工作区保存在独立 Docker volumes 中。使用其他模型服务时，修改容器配置中的模型地址和名称。生产部署与备份见 [运维文档](docs/operations.md)。

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
curl -N -X POST http://127.0.0.1:3030/v1/chat/stream \
  -H "Authorization: Bearer $AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message":"帮我检查这个项目"}'
```

SSE 每帧包含 `id`、`event` 和 JSON `data`。事件 envelope 含 `schema_version`、`event_id`、`sequence`、`conversation_id`、`run_id`、`timestamp`；连接每 15 秒发送心跳。运行事件也可通过 `/v1/runs/{id}/events?after=<sequence>` 获取。

```bash
# 查询运行与事件
curl -H "Authorization: Bearer $AUTH_TOKEN" \
  http://127.0.0.1:3030/v1/runs/$RUN_ID
curl -H "Authorization: Bearer $AUTH_TOKEN" \
  "http://127.0.0.1:3030/v1/runs/$RUN_ID/events?after=0"

# 恢复；同步请求不可处理交互审批，交互场景使用 stream=true
curl -N -X POST "http://127.0.0.1:3030/v1/runs/$RUN_ID/resume" \
  -H "Authorization: Bearer $AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"strategy":"retry","stream":true}'
```

SSE 对话被接受后，断线不会取消任务；用 `X-Command-ID` 重试同一请求可去重。重新订阅 `GET /v1/runs/{run_id}/events/stream?after=<sequence>`，或发送 `Last-Event-ID` 恢复。停止使用 `/v1/runs/{run_id}/stop`。

完整接口见 [API/SSE 文档](docs/api.md) 和 [OpenAPI 草案](docs/openapi.yaml)。

## ZCode 配置对齐

主 Agent 已移除 `max_tool_iterations: 15`、累计 token 和总运行时长硬停止；重复调用只警告。默认权限 `build`，技能元数据预算 `20000`，模型流空闲超时 `600000` ms，模型网络超时 `180000` ms，工具并发 `10`，模型重试 `10` 次。embedding 客户端、向量查询、索引重建接口与对应环境变量已移除；数据库升级先备份，再删除向量表，保留会话与记忆。

[逐项对照表](docs/zcode-alignment.md)记录旧配置的全部 65 项、映射和删除原因。HTTP/SSE/WebSocket 共用执行与事件语义；JSON WebSocket 是本项目独立协议，不兼容 ZCode Desktop 的二进制 RPC/V4。独立部署路径、模型连接块、HTTP 鉴权/限流继续保留；SQLite 文本记忆与 ZCode MEMORY.md 不互换。

## SKILL 配置

本仓库在 `.agents/skills` 内置六个从 ZCode 导入的项目技能：`docx`、`pdf`、`pptx`、`xlsx`、`visualize` 和 `skill-creator`。完整脚本、references、assets、模板与许可证均已保留；来源版本和本地适配见 [UPSTREAM.md](.agents/skills/UPSTREAM.md)。可直接使用 `/skill <name> [任务]`，例如 `/skill docx 创建一份项目周报`。

这些技能不会自动安装 Python、Node、LibreOffice、Poppler、Playwright 等外部依赖，实际文档生成前需按各技能的环境检查说明准备依赖。四个 Office 技能不包含 ZCode 侧栏查看器或 MCP 服务；`visualize` 在 AgentCoreGo 中生成并链接 standalone HTML，不使用 ZCode Desktop 的 inline Gen UI。

自定义技能可在工作区创建 `.zcode/skills/code-review/SKILL.md`：

```markdown
---
name: code-review
description: 审查代码时使用，给出文件位置、风险和修复建议。
---

先阅读相关源码和测试，再检查正确性、权限与错误处理。
修改后运行相关测试，汇报实际验证范围。
```

在服务配置中设置：

```yaml
features:
  skill: true
skills:
  enabled: true
  includeInstructions: true
  metadataBudget: 20000
  roots: [/srv/shared-skills]
skill:
  /srv/project/.zcode/skills/obsolete/SKILL.md:
    enable: false
```

发现顺序：显式 `roots` → 当前项目及 Git 祖先（近到远）→ 用户家目录；每层依次搜索 `.zcode/skills`、`.agents/skills`、`.claude/skills`。每个技能目录包含 `SKILL.md`，同名技能以先发现的为准；API 同时返回 shadow diagnostics。单项覆盖使用 **SKILL.md 的绝对路径**，也可放进项目 `.zcode/config.json` 的 `skill` 对象；项目不能改全局 roots 或权限。

默认只向 prompt 注入名称、描述和路径等元数据，预算按字符计算并保留完整 XML 条目；正文按需由模型调用 `skill` 工具加载，最大 100 KiB，并返回技能基目录。`includeInstructions: false` 关闭自动元数据注入，仍允许显式加载；`skills.enabled: false` 或 `features.skill: false` 关闭技能工具与 API。`allowed-tools/model/effort/context/hooks` 等风险 frontmatter 需要对当前内容进行 Workspace Trust 审阅，不能提高工具权限。

```bash
./agent-core skills
./agent-core skills --name code-review
./agent-core chat --message '/skill code-review 检查当前变更'
./agent-core trust review
# 检查返回的声明和 digest 后授权
./agent-core trust grant --digest '<bundle_digest>'
```

同样可用 `GET /v1/skills`、`POST /v1/skills/load`（`{"name":"code-review"}`）。命令文件用 `command.<绝对文件路径>.enable` 单项启停；完整示例见 [Skills 与 Slash Commands](docs/commands.md)。

## MCP 配置

在 `agent-core.yaml` 中声明 `mcp.servers`；这些服务配置是显式信任的，启动即连接：

```yaml
features:
  mcp: true
mcp:
  servers:
    local-tools:
      type: stdio
      command: /usr/local/bin/my-mcp-server
      args: [--stdio]
      cwd: /srv/project
      env:
        SERVICE_TOKEN: "${SERVICE_TOKEN}"
      enabled: true
      alwaysLoad: false
      timeoutMs: 30000
    remote-tools:
      type: http
      url: https://mcp.example.com/mcp
      headers:
        Authorization: "Bearer ${MCP_TOKEN}"
      timeoutMs: 30000
    legacy-tools:
      type: sse
      url: https://legacy.example.com/sse
      enabled: false
```

`stdio` 使用独立 command/args，不经 shell；`env` 叠加到 PATH/HOME/临时目录等白名单。`http` 是 Streamable HTTP，`sse` 是 legacy SSE。`timeoutMs` 默认 30000，控制握手与工具调用；成功握手后取消建连上下文不会误关长连接。`alwaysLoad: true` 让该服务工具始终进入模型工具列表；默认按需发现。工具命名为 `<server>__<tool>`，受共享并发、权限和取消控制。

`headers/env` 支持环境变量展开，`config show` 全部脱敏。显式服务声明允许连接本机/私网 MCP；动态 API 连接默认阻止私网，需要逐服务 `allow_private: true`。跨源重定向仍被拒绝。修改 YAML 后重启；同名服务配置优先于数据库中动态注册的连接。`features.mcp: false` 关闭启动连接和 MCP 管理 API。

项目 `.zcode/config.json` / `zcode.json` 也可声明同结构的 `mcp.servers`，但必须先通过当前声明 bundle 的 Trust 审阅，**授权后重启才连接**，且不能覆盖用户/服务的同名声明。当前服务为单 workspace，共享 MCP 连接；可省略 `isolation` 或使用 `workspace`。OAuth、`isolation: session`、强制固定 `protocolVersion` 暂不实现，配置会报错；省略版本或 `auto` 由 SDK 协商。

```bash
./agent-core mcp list
# 动态 API/CLI 使用 transport/config 包装；http 对应 streamable_http
./agent-core mcp connect --name local-tools --transport stdio \
  --config '{"command":"/usr/local/bin/my-mcp-server","args":["--stdio"]}'
./agent-core mcp-serve --config agent-core.yaml --transport stdio
```

详细协议、进程清理与 Host Callback 示例见 [MCP 文档](docs/mcp.md)。

## WebSocket

连接 `ws://127.0.0.1:3030/ws`。原生客户端可传 Bearer header；浏览器首帧必须在 10 秒内发送 `auth`（token 不放 URL）：

```json
{"id":"auth-1","method":"auth","params":{"token":"your-token"}}
{"id":"send-1","method":"session.send","params":{"command_id":"unique-task-uuid","message":"检查项目"}}
```

发送回执的 `result` 含 `run_id`、`conversation_id`。然后订阅：

```json
{"id":"sub-1","method":"run.subscribe","params":{"subscription_id":"task-events","run_id":"<run_id>","after":0}}
```

事件帧包含 `subscription_id` 和 `event`，event 与 HTTP/SSE 使用相同 `event_id/sequence`。保存最后 sequence，断线后再次订阅即可恢复。相同 `command_id` 与内容返回同一 run，不重复执行；不同内容复用 ID 会报错。`run.stop`、`permission.respond`、`input.respond` 与 HTTP 共用处理逻辑，见 [WebSocket 协议](docs/websocket.md)。

## 文档

- [配置与环境变量](docs/configuration.md)
- [ZCode 配置逐项对照](docs/zcode-alignment.md)
- [WebSocket 协议](docs/websocket.md)
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
internal/store/         SQLite 迁移、run/event/log、命令去重
internal/memory/        记忆服务与工具
internal/compressor/    上下文预算与摘要
internal/server/        HTTP/SSE/WebSocket、中间件与管理 API
deploy/                 systemd、Caddy、容器配置
docs/                   使用与运维文档
```

## License

本项目采用 **BSD 3-Clause License**，详见 [LICENSE](LICENSE)。
