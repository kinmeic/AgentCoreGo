# ZCode 运行语义对齐

基准为本地 ZCode v3.17.2 / `a9fe401e`。本项目继续作为独立 Go 服务端：保留 HTTP JSON/SSE，新增 WebSocket，共用会话、运行、审批和持久事件；不声称兼容 ZCode Desktop 的二进制 RPC 或 V4 投影协议。

## 配置规则

- 对照 `apps/zcode-cli/packages/contracts/src/config/index.ts` 的 `DefaultRuntimeConfig` 和 `apps/zcode-cli/packages/adapters/src/config/schema.ts`，采用同名运行配置和默认值。
- 主 Agent 不设工具调用次数、累计 token 或总运行时长硬停止；取消、权限、模型错误、工具超时和上下文压缩负责终止/续行。重复工具调用仅发出有界警告。
- 模型连接仍由独立服务的 `llm` 管理；模型能力是每个模型的属性，不能把某个模型的限制当作全局运行预算。
- 移除 embedding、向量索引及相关环境变量；记忆不依赖远程 Embeddings API。
- 监听、鉴权、工作目录、独立 SQLite 路径是 Go 服务部署边界，不复用桌面 ZCode 数据库。
- 不再公开 ZCode 运行配置中不存在的压缩比例、工具循环次数、记忆容量、静态 DAG 调度上限、命令元数据预算和采集队列等旋钮。旧字段必须报告迁移错误，不静默接受后忽略。

旧 `agent-core.yaml` 共 65 个叶配置项，完整基线保存在 [配置清单](zcode-config-baseline.json)，下表逐项给出最终映射和原因。

## 协议验收

HTTP、SSE、WebSocket 适配同一个执行入口和持久事件，不能各起一套 Agent 状态。WebSocket 应提供请求关联、并发取消、审批/澄清响应、事件恢复和背压；订阅断开不取消已接受的后台运行，显式 stop 才停止它。

ZCode `/ws` 使用 13-byte SocketProtocol + Channel RPC；本项目的 JSON WebSocket 必须明确标注独立协议，不能只复用路径就宣称 wire 兼容。会话模式、运行终态和恢复语义按 ZCode 语义收敛。

## 回归场景

1. 默认任务执行超过 15 次工具调用仍可完成，并能取消；重复调用警告不变成硬停止。
2. 所有对齐的配置名、默认值、单位、开关和项目/用户优先级有对照测试；旧硬限制和 embedding 配置失败并提示迁移。
3. 无 embedding 配置、客户端、向量查询或启动网络调用；旧数据库升级保留业务记录。
4. Skills 开关、20,000 metadata budget、根目录/单项覆盖有效；MCP 声明可启动 stdio、HTTP、SSE 并正确清理。
5. 同一会话可从 HTTP、SSE 和 WebSocket 查询/订阅；断连、恢复、审批、取消和慢消费者不产生重复执行或越权。
6. README 配置示例与真实配置解析、CLI、接口一同验证。

## 审计统计与逐项映射

按旧示例 YAML 的叶配置项计数（列表/映射作为一个公开项）：**65 项 = 38 项移除公开入口 + 13 项映射对齐 + 14 项服务保留**。13 个映射项中 9 个有默认值变化，其他为字段/语义映射。新增 ZCode 同名设置单独列于配置文档，不重复算进旧 65 项。

“移除公开项”不等于删除所有对应内部代码：ZCode 没有的运行调节旋钮已从 YAML/JSON schema 移除；现有 Go 工作流、诊断采集、schema/文件输出上限保留实现常量。embedding 则连同网络客户端、向量查询、索引重建端点与环境变量完整删除。迁移 1–6 保留原 checksum；迁移 7 先经现有启动备份机制，再 DROP vector_store，不删除 agent_memories 或会话记录。

| 旧字段 | 处理 | 新字段 / 结果 |
|---|---|---|
| `server.host` | 服务保留 | `server.host`：127.0.0.1；部署监听边界 |
| `server.port` | 映射对齐 | `server.port`：3030；对齐 ZCode HTTP 监听默认值 |
| `server.auth_token` | 服务保留 | `server.auth_token`：空；独立服务 Bearer 鉴权 |
| `server.max_request_bytes` | 服务保留 | `server.max_request_bytes`：1048576；HTTP/WS 请求上限 |
| `server.cors_origins` | 服务保留 | `server.cors_origins`：[]；精确 Origin 白名单 |
| `server.rate_limit_per_minute` | 服务保留 | `server.rate_limit_per_minute`：300；独立 HTTP 防护 |
| `server.max_concurrent_runs` | 服务保留 | `server.max_concurrent_runs`：10；独立服务并发接收边界 |
| `server.log_format` | 映射对齐 | `logging.format`：text；同名运行配置 |
| `llm.api_format` | 服务保留 | `llm.api_format`：openai；独立 provider 连接块，另支持 anthropic |
| `llm.base_url` | 服务保留 | `llm.base_url`：OpenAI /v1；独立 provider 连接 |
| `llm.api_key` | 服务保留 | `llm.api_key`：空；环境注入 |
| `llm.model` | 映射对齐 | `llm.model`：空；不预选 gpt-4o，由用户指定 provider 模型 |
| `llm.context_window` | 映射对齐 | `llm.context_window`：200000；ZCode fallback context，按实际模型设置 |
| `llm.max_tool_iterations` | 移除公开项 | 主 Agent 无工具迭代硬停止 |
| `llm.max_retries` | 映射对齐 | `ZCODE_MODEL_RETRY_MAX_RETRIES`：10 次重试；2s/2倍/60s/jitter |
| `llm.temperature` | 移除公开项 | 不暴露全局采样温度，采用 provider 默认值 |
| `llm.max_output_tokens` | 映射对齐 | `llm.max_output_tokens`：32000；模型能力 fallback，压缩预留最多21000 |
| `llm.max_run_tokens` | 移除公开项 | 主 Agent 无累计 token 硬停止 |
| `llm.run_timeout_seconds` | 移除公开项 | 主 Agent 无总时长硬停止；改用流空闲超时与取消 |
| `llm.reasoning_effort` | 服务保留 | `llm.reasoning_effort`：空；模型能力属性 |
| `llm.fallbacks` | 服务保留 | `llm.fallbacks`：[]；独立多 provider 接入 |
| `embedding.base_url` | 移除公开项 | 无启用的 ZCode embedding 召回；删除客户端与配置 |
| `embedding.api_key` | 移除公开项 | 无启用的 ZCode embedding 召回；删除客户端与配置 |
| `embedding.model` | 移除公开项 | 无启用的 ZCode embedding 召回；删除客户端与配置 |
| `embedding.dimensions` | 移除公开项 | 无启用的 ZCode embedding 召回；删除客户端与配置 |
| `store.db_path` | 服务保留 | `storage.sessionDbPath`：./data/agent.db；刻意不打开桌面 DB |
| `store.vector_backend` | 移除公开项 | 删除向量存储；旧表经备份后迁移删除 |
| `workspace.path` | 服务保留 | `workspace.path`：当前目录；独立服务固定 workspace |
| `workspace.soul_file` | 移除公开项 | 固定使用工作区 AGENTS.md |
| `compression.threshold_percent` | 移除公开项 | 无对应 runtime 旋钮；按 context/output 预算自动压缩 |
| `compression.protect_first_n` | 移除公开项 | 无对应 runtime 旋钮；按 context/output 预算自动压缩 |
| `compression.protect_last_n` | 移除公开项 | 无对应 runtime 旋钮；按 context/output 预算自动压缩 |
| `compression.target_ratio` | 移除公开项 | 无对应 runtime 旋钮；按 context/output 预算自动压缩 |
| `tools.approval_mode` | 映射对齐 | `permission.mode`：build；不再使用 smart 别名作为公开默认 |
| `tools.terminal_enabled` | 服务保留 | `server.terminalEnabled`：true；非 loopback 必须关闭 |
| `tools.progressive_tools` | 移除公开项 | 无对应 runtime 旋钮；按需发现保持内部实现 |
| `tools.tool_search_threshold` | 移除公开项 | 无对应 runtime 旋钮；按需发现保持内部实现 |
| `tools.enabled_toolsets` | 移除公开项 | 无对应 runtime 旋钮；按需发现保持内部实现 |
| `memory.capacity_limit` | 移除公开项 | 无对应容量/合并策略旋钮；只公开 memory.use |
| `memory.consolidation_threshold` | 移除公开项 | 无对应容量/合并策略旋钮；只公开 memory.use |
| `memory.auto_nudge` | 移除公开项 | 无对应容量/合并策略旋钮；只公开 memory.use |
| `memory.auto_consolidate` | 移除公开项 | 无对应容量/合并策略旋钮；只公开 memory.use |
| `security.policies` | 移除公开项 | 用 permission.allowedTools/disallowedTools；内部 deny 优先仍保留 |
| `features.skills` | 映射对齐 | `features.skill`：true；另受 skills.enabled 控制 |
| `features.commands` | 移除公开项 | 使用 command.<绝对路径>.enable |
| `features.subagents` | 映射对齐 | `features.subagent`：true；子代理默认4回合 |
| `features.workflow` | 移除公开项 | ZCode 没有此 runtime feature；Go DAG 扩展保留内部实现 |
| `features.model_io` | 移除公开项 | ZCode 没有此 runtime feature；默认脱敏诊断采集 |
| `ui.locale` | 映射对齐 | `ui.locale`：en-US |
| `skills.roots` | 映射对齐 | `skills.roots`：[] |
| `skills.metadata_budget` | 映射对齐 | `skills.metadataBudget`：20000 字符 |
| `commands.roots` | 移除公开项 | 无对应 runtime 旋钮；标准命令目录与内部文件上限 |
| `commands.metadata_budget` | 移除公开项 | 无对应 runtime 旋钮；标准命令目录与内部文件上限 |
| `commands.max_command_bytes` | 移除公开项 | 无对应 runtime 旋钮；标准命令目录与内部文件上限 |
| `workflow.enabled` | 移除公开项 | 无对应 runtime 旋钮；Go DAG 实现常量，默认无总时长/调用次数硬停止 |
| `workflow.roots` | 移除公开项 | 无对应 runtime 旋钮；Go DAG 实现常量，默认无总时长/调用次数硬停止 |
| `workflow.max_nodes` | 移除公开项 | 无对应 runtime 旋钮；Go DAG 实现常量，默认无总时长/调用次数硬停止 |
| `workflow.max_concurrency` | 映射对齐 | `toolConcurrency.maxConcurrency`：10；共享工具/Workflow 并发 |
| `workflow.max_agent_calls` | 移除公开项 | 无对应 runtime 旋钮；Go DAG 实现常量，默认无总时长/调用次数硬停止 |
| `workflow.timeout_seconds` | 移除公开项 | 无对应 runtime 旋钮；Go DAG 实现常量，默认无总时长/调用次数硬停止 |
| `telemetry.mode` | 移除公开项 | 无对应 runtime 旋钮；固定脱敏诊断，storage.dir/model-io |
| `telemetry.directory` | 移除公开项 | 无对应 runtime 旋钮；固定脱敏诊断，storage.dir/model-io |
| `telemetry.queue_size` | 移除公开项 | 无对应 runtime 旋钮；固定脱敏诊断，storage.dir/model-io |
| `telemetry.max_artifact_bytes` | 移除公开项 | 无对应 runtime 旋钮；固定脱敏诊断，storage.dir/model-io |
| `telemetry.retention_files` | 移除公开项 | 无对应 runtime 旋钮；固定脱敏诊断，storage.dir/model-io |

## 参考证据与实现边界

参考目录为本地 `../ZCode`（只读对照，版本 a9fe401e）：

- `apps/zcode-cli/packages/contracts/src/config/index.ts`：DefaultRuntimeConfig，技能 20000、并发 10、build、idle/network、feature 默认值。
- `apps/zcode-cli/packages/adapters/src/config/schema.ts` / `env-config.adapter.ts`：camelCase 字段、受限项目层和 ZCODE 环境变量。
- `apps/zcode-cli/packages/adapters/src/model/retry-policy.ts`：10 次重试、2000ms/2/60000ms/jitter。
- `apps/zcode-cli/packages/core/src/compact/policy.ts`：200000 context fallback、32000 output fallback、21000 reserve cap、13000 buffer。
- `apps/zcode-cli/packages/core/src/runtime/methods/subagent.ts` 与 `runtime/helpers/model-anomaly.ts`：子代理4回合、重复工具警告3次且注入最多3次；主 Agent 没有对应硬停止。
- `apps/zcode-cli/packages/core/src/memory/project-memory-retrieval-branch.ts`：semantic recall 分支未启用；使用项目文件记忆，无 Embeddings API 接入。
- `apps/zcode-cli/packages/core/src/permission/service.ts`：build/edit、guarded/yolo、保留 auto、alwaysAsk。
- `packages/server/src/http.ts`：3030、/ws 二进制 RPC；独立服务按用户选择保留 HTTP/SSE 并增加 JSON WS。

本次对齐针对本服务已实现能力的配置名、默认值与运行语义，不是把 ZCode Desktop 完整重写成 Go。保留以下明确边界：

1. server/llm/workspace 为独立部署/模型接入；storage 使用独立默认目录与 SQLite，不读写桌面数据；provider 格式支持 Chat Completions/Messages，不冒充 Responses。
2. 文本记忆仍是 SQLite CRUD/本地检索，不与 ZCode MEMORY.md 格式互换；不再包含 embedding。压缩策略使用相同触发预算，摘要生成与 token 估算仍是本项目实现。
3. JSON WS 共用会话、审批、stop 和持久事件，不提供 ZCode 二进制 SocketProtocol、V4、host capability 或桌面 renderer。
4. 显式 disallowedTools 与 Explore 只读限制始终优先于 yolo/guarded。这是服务端硬化例外，不照搬参考中部分 yolo 先放行再检查 deny 的顺序。
5. MCP 支持 workspace 共享 stdio/http/sse、enabled、alwaysLoad、30s timeout 和 auto 协商；OAuth、session isolation、固定协议版本明确拒绝。项目 MCP 须在当前声明获得 Trust 后重启启用。
6. ZCode 的 hooks、plugins、桌面 UI 等完整宿主能力不在本服务中；未实现字段会拒绝，不接受后忽略。ui 只作为客户端偏好元数据。项目受保护字段按参考 schema 过滤。

详见 [完整配置](configuration.md)、[WebSocket 合同](websocket.md) 与 [README 配置示例](../README.md)。

Guarded 的终端规则保留服务端保守边界：无法证明只读的本地 shell 命令每次询问，不能用会话授权跳过。参考 ZCode 使用更细的危险 Bash 语法匹配器；Go 服务不会把未实现的匹配器当作已具备能力。普通非终端工具仍按 guarded 语义处理。
