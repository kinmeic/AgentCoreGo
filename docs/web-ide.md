# Web IDE 与快照

## Workspace API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/v1/workspace` | workspace identity 与项目声明 |
| GET | `/v1/workspace/entries?path=&offset=&limit=` | 有界、稳定排序目录浏览 |
| GET | `/v1/workspace/files/content?path=&offset=&limit=` | UTF-8 分页读取与 SHA-256 revision |
| PUT | `/v1/workspace/files/content` | 原子写，已有文件必须带 revision |
| GET | `/v1/workspace/files/diff?path=` | 当前文件相对 HEAD 的 diff |
| GET | `/v1/workspace/git/status` | 结构化 staged/unstaged/untracked/conflicts |
| GET | `/v1/workspace/git/diff?mode=&base=&path=` | worktree/staged/head/base diff |
| GET | `/v1/workspace/git/log?before=&limit=&path=` | 有界提交历史 |

写入示例：

```json
{
  "path": "src/main.go",
  "content": "package main\n",
  "expected_revision": "<当前文件revision>",
  "create_snapshot": true
}
```

也可用 `If-Match` header。已有文件缺少 revision 或 revision 已变化会返回 409。路径、符号链接、文件大小和 UTF-8 均复用 workspace 安全边界。

Git 使用固定 argv、禁用 hooks/ext diff/textconv/lazy fetch，不接受任意 Git 参数。porcelain v2/NUL 支持 rename、conflict、非 ASCII 和特殊文件名。

## 完整文件树快照

```http
POST /v1/snapshots
GET /v1/snapshots
GET /v1/snapshots/{id}
POST /v1/snapshots/{id}/restore
```

Manifest 包含文件类型、路径、权限、大小、SHA-256、symlink target 和整体 digest；blob 在 workspace 外内容寻址存储。默认包含未跟踪文件，排除 `.git`、状态目录和配置的 ignore。文件数、单文件和总字节预算超限会明确失败。

恢复语义：

1. 检查 snapshot 后的 revision 冲突；存在未批准变更时返回 409。
2. 必须明确传 `allow_changes: true` 才能覆盖或删除这些文件。
3. 恢复前先为当前状态创建补偿快照。
4. 按 manifest 恢复创建/删除/移动/权限/空目录/symlink，并新增补偿事件，不修改历史 run/event。

`/fork` 只复制会话历史并记录来源，不隐式覆盖共享 workspace；需要回滚时显式执行 `/rewind` 或 restore。

快照不回滚 SQLite、MCP/Host 外部状态、网络响应或系统副作用。

## 执行与轨迹面板

- `GET /v1/conversations/{id}/runs`
- `GET /v1/runs/{id}/model-calls`
- `GET /v1/runs/{id}/tool-calls`
- `GET /v1/runs/{id}/changes`
- `POST /v1/runs/{id}/trace/export`
- `GET /v1/config/effective`

文件变更包含 operation、before/after revision、增删行和 diff artifact。run 净变化应按 first-before → final-after 理解，不能把每次写入的增删简单累加为权威 Git 状态。

## SSE 断线续传

```http
GET /v1/runs/{id}/events/stream?after=<sequence>
Last-Event-ID: <event_uuid>
```

服务端先重放数据库事件，再进入 live；每个订阅者独立维护 cursor，按 `(run_id, sequence)` 去重。超前 cursor 返回 `resync_required`，慢消费者不会被允许无限阻塞 Agent。浏览器使用 fetch streaming + Bearer，不把 token 放 URL。
