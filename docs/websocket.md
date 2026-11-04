# AgentCore JSON WebSocket v1

这是独立服务协议，语义复用 HTTP/SSE 的会话、运行、权限和持久事件。ZCode Desktop `/ws` 的二进制 SocketProtocol/Channel RPC 与 stdio/V4 不兼容；`GET /api/server-info` 明确声明 `websocketJson: true`、`websocketRpc: false`。

## 连接与鉴权

`ws://127.0.0.1:3030/ws`；TLS 反代使用 `wss://`。原生客户端可发送 `Authorization: Bearer <token>`。浏览器首帧在 10 秒内认证：

```json
{"id":"auth","method":"auth","params":{"token":"secret"}}
```

回执 `{"id":"auth","result":{"authenticated":true}}`。认证前不接受命令，不使用 URL token。错误 Bearer 在 upgrade 前返回 401；首帧错误关闭 1008。Origin 必须同源或匹配 `server.cors_origins`；无 token 的本机模式还要求 Host 为 loopback/localhost。

## 帧格式

请求 `{"id":"请求关联字符串","method":"方法","params":{}}`。id 必填，最多 128 字符，用于当前连接关联；不要把请求 id 当作持久去重键。同连接允许 10 个并发请求，响应可乱序。

成功 `{"id":"...","result":...}`；失败 `{"id":"...","error":{"code":400,"message":"..."}}`。事件独立为 `{"subscription_id":"...","event":{...}}`，订阅响应一定先于该订阅事件。

## 发送与去重

```json
{"id":"send","method":"session.send","params":{"command_id":"task-uuid","conversation_id":"existing-session-or-omit","message":"检查源码","source":"web","metadata":{}}}
```

新会话省略 conversation_id；成功返回 command_id、conversation_id、run_id。`command_id` 必填，最大 128 字符，任务应使用 UUID。普通 Agent 消息的接收记录和 pending run 在 SQLite 同一事务落盘后才回复。相同 ID 和相同请求内容（包含 source/metadata/conversation_id）返回原 run；更改内容返回错误。去重跨连接与服务重启保留，不自动过期。Slash 控制命令返回 command.result，不创建 Agent run；控制命令不使用这套任务去重，操作前应读取当前状态。

断开、取消订阅和慢消费者不取消已接受任务。显式 run.stop 才请求取消，进程关闭时也取消；服务崩溃后不自动重放工具，新启动将未完成的已接受运行标为 failed，用户通过 HTTP resume 检查 checkpoint 并明确选择 retry/skip。独立的同步 HTTP chat 保留非交互语义；HTTP SSE 和 WebSocket 任务可交互。

## 事件与恢复

```json
{"id":"sub","method":"run.subscribe","params":{"subscription_id":"task","run_id":"run-uuid","after":12}}
{"id":"unsub","method":"run.unsubscribe","params":{"subscription_id":"task"}}
```

`after` 为最后处理的 sequence，首次 0；按序重放后继续订阅。event 是 [SSE envelope](api.md#sse-协议)，与 HTTP `/v1/runs/{id}/events` 使用完全相同的 event_id/sequence。每个连接最多 8 个订阅；同名活跃订阅报冲突。未知 run 返回 404；cursor 超过 watermark 发 `resync_required`。终态后结束订阅，连接可继续使用。

队列最多 128 帧，入队超时 2 秒、写入超时 5 秒，慢连接断开。重连后用保存的 cursor 继续，按 event_id 去重；`resync_required` 时查询 run 当前状态，再建立有效 cursor。每 30 秒 Ping，10 秒未响应关闭。单帧受 server.max_request_bytes 限制，不启用压缩。

## 共用 HTTP 处理器的方法

以下方法 params 统一为 `{"id":"资源 ID","body":{...},"after":0}`；只传需要的字段。body 是对应 HTTP JSON body。

| 方法 | HTTP 语义 |
|---|---|
| `server.info` | GET /api/server-info |
| `config.read` | GET /v1/config/effective（脱敏） |
| `session.list` | GET /v1/conversations |
| `session.read` | GET /v1/conversations/{id} |
| `session.messages` | GET /v1/conversations/{id}/logs |
| `run.read` | GET /v1/runs/{id} |
| `run.events` | GET /v1/runs/{id}/events?after=... |
| `run.stop` | POST /v1/runs/{id}/stop |
| `permission.list` | GET /v1/approvals |
| `permission.respond` | POST /v1/approvals/{id} |
| `input.respond` | POST /v1/chat/{id}/clarify |
| `skill.list/load` | GET /v1/skills / POST /v1/skills/load |
| `mcp.list` | GET /v1/mcp |

例：审批可从任意已认证 HTTP/SSE/WebSocket 客户端处理同一个 request_id：

```json
{"id":"approve","method":"permission.respond","params":{"id":"approval-request-uuid","body":{"decision":"allow","scope":"once"}}}
{"id":"stop","method":"run.stop","params":{"id":"run-uuid"}}
```

会话 `/mode`、`/model`、`/effort` 等控制命令通过 session.send.message 执行，与 HTTP chat 使用同一命令解析器。配置读取、审批、模式和 feature 开关不会因传输方式改变。
