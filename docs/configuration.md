# 配置

运行配置对齐本地 ZCode v3.17.2 / `a9fe401e`，字段采用其 camelCase。独立服务继续使用 YAML（也可读取 JSON），`server`、`llm`、`workspace` 是部署/模型连接扩展。逐项迁移与差异统计见 [ZCode 对照表](zcode-alignment.md)。

```bash
./agent-core config validate --config agent-core.yaml
./agent-core config show --config agent-core.yaml
```

配置启动时读取；修改后重启。未知字段、多 YAML 文档、非法数值/URL/CORS 会报错。旧字段如 `llm.max_tool_iterations`、`embedding`、`tools` 会提示迁移，不再静默兼容。`config show` 脱敏 token、API key、MCP headers/env，不修改实际运行配置。

## ZCode 同名运行配置

| 配置 | 默认值 | 生效范围 |
|---|---|---|
| `modelStream.idleTimeoutMs` | `600000` ms | 模型流连续无数据时取消；持续有数据会重置计时 |
| `network.timeout` | `180000` ms | 模型请求响应头/非流式响应读取超时 |
| `network.httpProxy/noProxy/caCertFile` | 未设置 | 模型 HTTP 代理、代理绕过列表、额外 CA PEM；也保留系统代理 |
| `permission.mode` | `build` | `build/edit/guarded/yolo/plan/auto`，见权限文档 |
| `permission.allowedTools/disallowedTools` | `[]` | 精确匹配 `/v1/tools` 返回的工具名；显式 deny 优先 |
| `permission.autoApproveHighRisk` | `false` | build 的高风险判定；不免除有副作用工具的审批 |
| `permission.allowMediumRiskInAuto` | `false` | 与 ZCode 一致保留；`auto` 是保留模式，当前拒绝工具 |
| `storage.dir` | `./data` | 独立服务数据根目录；与桌面 ZCode 数据隔离 |
| `storage.sessionDbPath` | `./data/agent.db` | 独立 SQLite；不会自动跟随 dir，须分别设置 |
| `logging.level/format` | `info/text` | `debug/info/warn/error`、`text/json` |
| `toolConcurrency.maxConcurrency` | `10` | 共享工具执行队列与 Workflow 并发 |
| `modelAnomalyGuard.repeatedToolCallWarningThreshold` | `3` | 相同工具和规范化参数的重复调用警告阈值 |
| `modelAnomalyGuard.maxBudgetWarningsPerTurn` | `3` | 每轮最多注入的警告数 |
| `modelAnomalyGuard.toolCallWarningThreshold` | 未设置 | 可选累计工具次数警告；始终不是硬停止 |
| `features.compact/rewind/subagent/memory/skill/mcp` | 全部 `true` | 分别控制自动压缩、快照、子代理、记忆、技能、MCP |
| `memory.use` | `true` | 关闭后不注入记忆、不注册记忆工具，记忆 HTTP API 返回 503 |
| `skills.enabled/includeInstructions` | `true/true` | 技能发现/加载开关与 prompt 元数据注入 |
| `skills.metadataBudget/roots` | `20000/[]` | 元数据字符预算，完整条目装入；额外发现根目录 |
| `skill/command` | `{}` | 绝对文件路径到 `{enable: false}` 的单项覆盖 |
| `mcp.servers` | `{}` | stdio/http/sse 声明，见 README 与 MCP 文档 |
| `ui.locale/theme` | `en-US/auto` | UI 偏好元数据；Go 服务不渲染桌面 UI |

主 Agent 无默认工具迭代、累计 token、总时长硬停止。子 Agent 默认 `4` 个模型回合，与 ZCode 子代理默认值一致。模型重试默认最多 `10` 次（首次请求之外），初始延迟 `2000` ms、指数因子 `2`、上限 `60000` ms，带 jitter；取消立即打断等待。

压缩触发按模型 context 减去工具 schema 估算、`min(max_output_tokens,21000)` 和 `13000` 缓冲估算；200k/32k 模型在约 166k 输入预算触发。摘要实现仍为 Go 分级压缩器，不能视为 ZCode 的逐 token 相同实现。`features.compact: false` 同时关闭预处理、循环内自动压缩与错误恢复压缩。

## 独立服务配置

`server` 默认 `host: 127.0.0.1`、`port: 3030`、空 token、请求上限 1048576 字节、空 CORS 列表、每 IP 每分钟 300 HTTP 请求、最多 10 个并发 run；`terminalEnabled: true`。非 loopback 必须设置 token 并关闭终端。WebSocket 另外限制每连接 10 个在途请求、8 个订阅、128 帧发送队列和 5 秒写入超时；超限断开后从持久 cursor 恢复。CORS 仅精确 origin。这些是服务部署边界，不是 ZCode RuntimeConfig 中的模型运行限制。

`workspace.path` 默认为当前目录；文件工具、项目发现和 `AGENTS.md` 共用此根目录。服务配置显式选择 workspace 后不会让项目文件修改它。ZCode 的桌面目录默认值与本项目隔离，避免直接打开其 session DB。

## 模型连接

```yaml
llm:
  api_format: openai
  base_url: https://api.openai.com/v1
  api_key: "${LLM_API_KEY}"
  model: your-model-id
  context_window: 200000
  max_output_tokens: 32000
  reasoning_effort: ""
  expose_reasoning: true
  thinking_type: disabled
  thinking_budget_tokens: 16000
  fallbacks: []
```

`model` 默认空；未配置时 `/ready` 返回 503，不猜测可用模型。context/output 是模型能力，默认采用 ZCode 的 fallback 200000/32000；请按实际模型调整。没有全局 temperature 配置，遵循 provider 默认采样参数。

模型注册在独立 `llm` 连接块中，并非直接读取 ZCode 的 provider_config.json。当前支持 OpenAI Chat Completions 与 Anthropic Messages；不支持 OpenAI Responses。

### Anthropic Messages

```yaml
llm:
  api_format: anthropic
  base_url: https://api.anthropic.com/v1
  api_key: "${LLM_API_KEY}"
  model: your-claude-model-id
  context_window: 200000
  max_output_tokens: 32000
  thinking_type: adaptive
  reasoning_effort: high
  expose_reasoning: true
  fallbacks:
    - name: openai-backup
      api_format: openai
      base_url: https://api.openai.com/v1
      api_key: "${BACKUP_API_KEY}"
      model: your-backup-model
      context_window: 200000
```

客户端调用 `/v1/messages`，使用 `x-api-key`、`anthropic-version: 2023-06-01`。支持文本、工具调用/结果和 SSE；省略 temperature；不支持图像/PDF、server tools、prompt cache 写入策略。跨源重定向拒绝转发密钥。fallback 可各用不同协议，继承超时、重试、输出和思考公开配置，Anthropic 专属 provider state 不会写入 OpenAI 请求。

`thinking_type` 默认 `disabled`。`enabled` 使用 manual thinking，并要求 `thinking_budget_tokens` 至少 1024 且小于 `max_output_tokens`；配合客户端工具时会携带 interleaved-thinking beta header。支持 adaptive thinking 的模型使用 `adaptive`，不发送 budget，可用 `reasoning_effort` 控制深度。具体模式取决于实际模型或网关能力。

`expose_reasoning` 默认 `true`。OpenAI `reasoning_content`、Anthropic thinking delta 和文本内嵌 `<think>` 正文通过独立 `reasoning` SSE/WS 事件输出，并进入 run events 以供 replay；不会混入 `chunk`、`done.reply` 或同步 `/v1/chat` 的回答。设为 `false` 可恢复不公开正文的行为。Anthropic signature 与 redacted data 仅保存在内部 provider state 用于后续请求和 checkpoint，不在公开事件或会话日志 JSON 中返回。已持久化的 reasoning 事件不会因之后关闭开关而自动删除。

## 配置层与环境变量

顺序：默认值 → `~/.agent-core/config.yaml` → `--config` 服务文件 → 受限项目配置 → 环境变量。独立路径避免意外读取 ZCode 桌面密钥/数据库。

项目支持 `.agent-core/config.yaml`、`.zcode/config.json`、`zcode.json`：只应用 `mcp` 和绝对路径 `skill/command` 覆盖。模型、权限、网络、存储、features、skills roots 等受保护字段被过滤，与 ZCode 的项目层语义一致。项目 MCP 在当前声明 bundle 获得 Workspace Trust 后，下次启动才连接；用户/服务配置同名声明优先。OAuth、MCP session isolation、固定协议版本当前不支持，会明确拒绝，不忽略。

| 环境变量 | 对应字段 |
|---|---|
| `AUTH_TOKEN` | server.auth_token |
| `AGENT_CORE_HOST/AGENT_CORE_PORT` | server.host/port |
| `AGENT_CORE_WORKSPACE` | workspace.path |
| `LLM_API_FORMAT/LLM_BASE_URL/LLM_API_KEY/LLM_MODEL` | llm 对应字段 |
| `ZCODE_STORAGE_DIR` | storage.dir |
| `ZCODE_SESSION_DB_PATH` / `ZCODE_SESSION_DB` | storage.sessionDbPath（PATH 优先）；`DB_PATH` 仍是部署别名 |
| `ZCODE_HTTP_PROXY/ZCODE_NO_PROXY/ZCODE_AGENT_CA_CERT` | network 代理/绕过/CA |
| `ZCODE_HTTP_TIMEOUT` / `ZCODE_TIMEOUT` | network.timeout（HTTP_TIMEOUT 优先） |
| `ZCODE_LOG_FORMAT` | logging.format |
| `ZCODE_MAX_TOOL_CONCURRENCY` | toolConcurrency.maxConcurrency |
| `ZCODE_MODEL_RETRY_MAX_RETRIES` | 模型重试次数，可为 0 |
| `ZCODE_MODEL_RETRY_BASE_DELAY_MS/BACKOFF_FACTOR/MAX_DELAY_MS` | 重试延迟/指数因子/上限 |

`EMBEDDING_API_KEY`、`VECTOR_BACKEND`、`WORK_DIR` 已删除。记忆使用 SQLite 本地文本检索，保留已有 CRUD 数据；与 ZCode 的 MEMORY.md 文件记忆共享“无 embedding”语义，存储格式并不互换。
