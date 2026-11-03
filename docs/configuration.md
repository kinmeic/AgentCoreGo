# 配置

配置只在进程启动时读取；重启是显式 reload。已开始的 run 不读取后续文件变化，`SOUL.md` 是唯一按回合实时读取的行为文件。不要把“修改 YAML”理解为会影响正在执行的 run。

## 校验与诊断

```bash
./agent-core config validate --config agent-core.yaml
./agent-core config show --config agent-core.yaml
```

YAML 使用严格字段检查：未知字段、多个 document、无效预算/URL/比例/端口/CORS 都会启动失败。`config show` 将 token 和 API key 脱敏。

## server

| 字段 | 默认值 | 说明 |
|---|---:|---|
| `host` | `127.0.0.1` | 非 loopback 必须配置 `auth_token` 并禁用终端 |
| `port` | `8900` | 1–65535 |
| `auth_token` | 空 | 静态单租户 Bearer token；生产必须设置 |
| `max_request_bytes` | `1048576` | 请求体上限，最小 1024 |
| `cors_origins` | `[]` | 精确 origin 列表，不允许 `*` 或路径 |
| `rate_limit_per_minute` | `300` | 每个直接连接 IP 的固定窗口限制；代理后需自行做外层限流 |
| `max_concurrent_runs` | `10` | 同时执行的 run 上限，超限返回 429 |
| `log_format` | `text` | `text` 或 `json` |

CORS 仅允许指定的 scheme/host/port。服务不处理 `X-Forwarded-For`，反向代理的客户端限流应在 Caddy/网关同步配置。

## llm

| 字段 | 默认值 | 说明 |
|---|---:|---|
| `base_url` | OpenAI `/v1` | OpenAI 兼容 Chat Completions 地址，不含 `/chat/completions` |
| `api_key` | 空 | 建议使用 `${LLM_API_KEY}` |
| `model` | `gpt-4o` | 模型名 |
| `context_window` | `128000` | 当前 provider 的上下文预算 |
| `max_tool_iterations` | `15` | 每 run 的 LLM/tool 循环上限 |
| `max_retries` | `3` | 建连前网络、429、5xx 的额外重试次数 |
| `temperature` | `0.7` | 0–2 |
| `max_output_tokens` | `4096` | 单次 completion 的 `max_tokens` |
| `max_run_tokens` | `100000` | 整 run 累计 usage 上限 |
| `run_timeout_seconds` | `1800` | run 总时长上限 |
| `reasoning_effort` | 空 | 可选透传 `reasoning_effort`，仅用于兼容 provider |
| `fallbacks` | `[]` | 备用 OpenAI 兼容 provider |

Fallback 项包含 `name/base_url/api_key/model/context_window/temperature`。建连成功后不会在流中途重放请求；fallback 保持为当前 provider，避免 context window 与正在使用的模型不一致。`Reset()` 可用于内部运维恢复，进程重启也会恢复主 provider。

## embedding / store

- `embedding.base_url/api_key/model/dimensions` 控制向量生成。未配置 API key 时，记忆使用文本检索。
- `store.db_path` 是 SQLite 文件。目录和备份应只允许服务账号读取。
- `store.vector_backend`: `sqlite`（BLOB + 有界余弦扫描）、旧别名 `sqlite-vec`、`none`。当前实现不是 SQLite `sqlite-vec` 扩展，名称保留仅为兼容。

## workspace

```yaml
workspace:
  path: ./workspace
  soul_file: SOUL.md
```

`workspace.path` 同时决定 SOUL、文件、搜索、Git 和终端初始目录。`tools.work_dir` 与 `WORK_DIR` 是旧别名；只配置旧别名时会映射到 workspace。两个字段显式冲突会启动失败。路径使用 `filepath.Abs`，支持平台原生绝对路径。

`SOUL.md` 每回合读取，最大 10,000 字节，路径不能通过符号链接逃逸 workspace。

## compression

- `threshold_percent`: 达到有效 context window 的比例后开始压缩。
- `protect_first_n/protect_last_n`: 首尾保护消息数。
- `target_ratio`: heavy 压缩目标比例。
- 工具 schema、tool arguments、role/tool-call ID 和协议预留均计入预算；摘要按明确的 log ID 边界持久化。

## tools

- `approval_mode`: `smart`（build 别名）、`plan`、`build`、`edit`、`guarded`、`yolo`。
- `terminal_enabled`: 生产与非 loopback 必须 `false`。
- `progressive_tools`: 超过阈值时隐藏未发现的非核心工具。
- `tool_search_threshold`: 默认 10；`tool_search/tool_describe` 发现的工具加入当前 run 并写入 checkpoint。
- `enabled_toolsets`: 空表示全部；可选 `core/web/memory/mcp/host_callback`。被禁用工具即使模型直接调用也拒绝执行。

## memory

- `capacity_limit`: enabled 记忆硬上限。
- `consolidation_threshold`: 触发合并的数量，必须不大于容量。
- `auto_nudge`: 输出提示日志。
- `auto_consolidate`: 达到阈值后自动合并低优先级记忆；默认 false。也可显式调用 `/v1/memories/consolidate`。

## security.policies

自定义策略在默认策略之后追加，deny 始终优先于审批模式。支持 `allow/deny/audit`，资源 `file/terminal/web/memory`，条件操作 `contains/prefix/suffix/equals/in/regex`。regex 使用 Go RE2；空值和无效表达式启动失败。

## 扩展与采集配置

- `features.skills/commands/subagents/workflow/model_io`：总开关。
- `ui.locale`：`auto/en-US/zh-CN`。
- `skills.roots/metadata_budget`：额外 Skill roots 与 prompt metadata 预算。
- `commands.roots/metadata_budget/max_command_bytes`：自定义命令 roots 与大小限制。
- `workflow.enabled/roots/max_nodes/max_concurrency/max_agent_calls/timeout_seconds`：声明式 DAG 执行预算。
- `telemetry.mode/directory/queue_size/max_artifact_bytes/retention_files`：redacted/training 采集、异步队列和 artifact 预算。

详见 [扩展配置](extensions.md)、[model-io](trajectory.md) 和 [Web IDE](web-ide.md)。

## 环境变量

| 变量 | 覆盖项 |
|---|---|
| `AUTH_TOKEN` | `server.auth_token`，不需要 YAML `${}` 也可生效 |
| `AGENT_CORE_HOST/PORT` | 监听地址/端口 |
| `LLM_API_KEY/BASE_URL/MODEL` | 主模型配置 |
| `EMBEDDING_API_KEY` | Embedding key |
| `DB_PATH` | SQLite 路径 |
| `VECTOR_BACKEND` | 向量后端 |
| `AGENT_CORE_WORKSPACE` | workspace 唯一来源 |
| `WORK_DIR` | 旧工具目录别名 |
| `AGENT_CORE_URL` | CLI 默认服务 URL |

YAML 中的 `${VAR}` 会展开。应用不自动读取 `.env`；systemd 的 `EnvironmentFile`、容器环境或 shell 负责注入。不要把 `.env`、数据库、MCP header 或 Host Callback secret 提交到 git。
