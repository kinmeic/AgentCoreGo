# CLI

CLI 是 HTTP API 的薄客户端；服务必须已启动。默认连接 `http://127.0.0.1:3030`，可用 `AGENT_CORE_URL` 或 `--url` 修改，token 来自 `AUTH_TOKEN` 或 `--token`。

## 对话

```bash
./agent-core chat --message '分析这个项目'
./agent-core chat --conversation <id> --message '继续'
./agent-core chat --stream=false --message '同步回答'
```

默认渲染 SSE 文本；工具事件和最终 conversation/run ID 写到 stderr。审批时：

- `y`：允许一次。
- `a`：该会话同名工具持续允许。
- 其他输入：拒绝。

澄清问题直接读取下一行输入。非交互自动化应显式选择权限模式并准备审批客户端；不要把无人值守的交互超时当成任务成功。

## 运行控制

```bash
./agent-core stop --run <run-id>
./agent-core stop --conversation <conversation-id>
./agent-core resume --run <run-id>
./agent-core resume --run <run-id> --strategy retry
./agent-core resume --run <run-id> --strategy skip
./agent-core resume --run <run-id> --stream=false
```

副作用工具中断时必须选择 retry/skip；默认安全失败。恢复输出遵循同一 SSE 终态协议。

## 会话与工具

```bash
./agent-core conversations --limit 50
./agent-core logs --conversation <id> --limit 100
./agent-core logs --conversation <id> --query '错误信息'
./agent-core logs --conversation <id> --before 123
./agent-core tools
```

日志响应为 JSON；需要 NDJSON 时使用 HTTP `format=jsonl`。

## 记忆

```bash
./agent-core memory list
./agent-core memory add --content '用户偏好简洁回答' --category preference
./agent-core memory delete --id 42
./agent-core memory consolidate
./agent-core memory rebuild
```

## MCP

```bash
./agent-core mcp list
./agent-core mcp connect --name local --transport stdio \
  --config '{"command":"/absolute/path/to/server","args":["--stdio"]}'
./agent-core mcp connect --name remote --transport streamable_http \
  --config '{"url":"https://mcp.example.com/mcp"}'
./agent-core mcp disconnect --name local
./agent-core mcp-serve --config agent-core.yaml --transport stdio
```

## 诊断与配置

```bash
./agent-core doctor
./agent-core doctor --config agent-core.yaml
./agent-core config validate --config agent-core.yaml
./agent-core config show --config agent-core.yaml
./agent-core version
```

`doctor` 输出版本、Go、平台、配置校验、health 和 ready，任一检查失败返回非零。`config show` 只显示脱敏有效配置。

## 退出码与脚本

- `0`: 命令成功且收到正常终态。
- `1`: API、流、工具交互或诊断失败。
- `2`: 未知命令。

SSE 提前 EOF、error/cancelled 均不能视为成功。脚本除最终文本外，还应检查 stderr 的 `status=completed` 和实际文件产物。CI 中建议设置 `--url`、`--token`，不要依赖个人 shell 配置。
