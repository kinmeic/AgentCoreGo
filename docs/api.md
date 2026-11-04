# HTTP API、SSE 与 WebSocket

除 `GET /health` 外，配置 `server.auth_token` 后所有接口都需要：

```http
Authorization: Bearer <token>
Content-Type: application/json
```

错误响应包含兼容的 `error` 字符串和机器可读 `code`。未知 JSON 字段、错误类型和多个 JSON value 返回 400；超过 `max_request_bytes` 会被拒绝。响应包含 `X-Request-ID`。

## 对话与运行

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/v1/chat` | 同步对话；需要审批/澄清时失败关闭 |
| POST | `/v1/chat/stream` | SSE 对话，支持审批与澄清 |
| POST | `/v1/chat/{conversation}/stop` | 停止该会话所有活跃 run |
| POST | `/v1/chat/{conversation}/clarify` | 提交 `{"response":"..."}` |
| GET | `/v1/runs/{run}` | run 状态、usage、错误与时间 |
| POST | `/v1/runs/{run}/stop` | 请求取消精确 run |
| POST | `/v1/runs/{run}/resume` | 从 checkpoint 恢复 |
| GET | `/v1/runs/{run}/events?after=0&limit=1000` | 按 sequence 获取持久化事件 |
| GET | `/v1/approvals` | 当前待审批列表 |
| POST | `/v1/approvals/{request}` | `{"decision":"allow|deny","scope":"once|conversation"}` |

Run 状态：`pending/running/waiting_clarification/waiting_approval/completed/failed/cancelled`。停止接口的 `cancellation_requested` 只是请求回执，最终状态以 run 查询或 SSE 终态为准。

恢复请求：

```json
{"strategy":"", "stream":true}
```

`strategy` 默认为空。若崩溃时副作用工具处于 `in_flight`，默认返回 409，避免盲目重复写文件/命令：

- `retry`: 明确允许重试该工具。
- `skip`: 写入“中断并跳过”的 tool result，再续跑后续工具。
- 已完成并提交 tool result 的工具不会重放；只读工具可安全重试。

## SSE 协议

```text
id: <event_uuid>
event: chunk
data: {"schema_version":1,"event_id":"...","sequence":4,"conversation_id":"...","run_id":"...","type":"chunk","content":"文本","timestamp":"..."}
```

事件类型：

- `conv_id`: 早期返回 conversation/run 标识。
- `thinking`: 即将调用模型，`iteration` 从 1 开始。
- `chunk`: 已过滤 `<think>` 的可见文本。
- `tool_call`, `tool.started`, `progress`: 工具生命周期；参数预览脱敏限长。
- `approval.required`: 使用 `request_id` 调用审批接口。
- `clarify`: 使用 conversation 调用 clarify 接口。
- `file.changed`: 文件修改路径与操作。
- `resumed`: checkpoint 恢复开始。
- `done | error | cancelled`: 每个 run 的唯一成功/失败/取消终态。

每 15 秒发送 `: keep-alive` 注释。事件先持久化再发送，可用 `after=<sequence>` 诊断或补拉；使用 `GET /v1/runs/{id}/events/stream?after=<sequence>` 或 `Last-Event-ID` 重新订阅。SSE chat 被接受后断线不会取消任务；`X-Run-ID` 返回 run ID，`X-Command-ID` 可作为任务重试去重键。未指定时自动生成。显式 stop 才取消。同步 chat 和交互式 resume 保留原有请求生命周期。

## 会话与日志

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/v1/conversations?source=&limit=&offset=` | 活跃会话 |
| GET | `/v1/conversations/{id}` | 详情 |
| PUT | `/v1/conversations/{id}` | `{"title":"..."}` |
| DELETE | `/v1/conversations/{id}` | 软归档；归档后拒绝新 run |
| GET | `/v1/conversations/{id}/logs` | 最新日志，时间正序 |
| GET | `/v1/conversations/{id}/logs?before={logID}&limit=100` | 向历史翻页 |
| GET | `/v1/conversations/{id}/logs?q=needle` | FTS5 或 LIKE 降级搜索 |
| GET | `/v1/conversations/{id}/logs?format=jsonl` | NDJSON 导出 |

满页响应的 `X-Next-Cursor` 是最早一条日志 ID，将它作为下一次 `before`。旧会话缺少完整 tool 状态时按 legacy 内容处理，不伪造工具历史。

## 记忆

- `GET/POST /v1/memories`
- `PUT/DELETE /v1/memories/{id}`
- `POST /v1/memories/consolidate`

记忆内容、类别、来源和 importance（0–1）可配置。禁用记忆不会进入文本召回；已移除 embedding/向量索引。`features.memory: false` 或 `memory.use: false` 时记忆 API 返回 503。

## 工具与 MCP

- `GET /v1/tools`: 名称、来源、能力、风险和超时。
- `POST /v1/tools/register`: 注册 Host Callback，必须提供 `secret`。
- `DELETE /v1/tools/{name}`: 删除 Host Callback；内建工具不可删除。
- `GET/POST /v1/mcp`, `DELETE /v1/mcp/{name}`: 连接管理与持久化。

Host Callback 示例：

```json
{
  "name":"host_search",
  "description":"Search host data",
  "schema":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]},
  "callback_url":"https://host.example/tools",
  "secret":"shared-hmac-secret",
  "allow_private":false
}
```

MCP 配置见 [mcp.md](mcp.md)。

## 健康与指标

- `GET /health`: 公开 liveness，仅表示 HTTP 进程可响应。
- `GET /ready`: 数据库、workspace、LLM 配置 readiness，失败返回 503。
- `GET /v1/metrics`: 请求数、5xx 数、平均请求时长、工具/MCP 数量。

## WebSocket

`GET /ws` 提供 AgentCore JSON v1，共用会话、审批与持久事件；鉴权、请求、去重、恢复和方法表见 [WebSocket 协议](websocket.md)。`GET /api/server-info` 返回明确的独立协议能力。
