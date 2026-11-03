# MCP 与 Host Callback

AgentCore 使用官方 `github.com/modelcontextprotocol/go-sdk` v1.8.0。客户端完成 initialize/initialized、协议版本协商、tools/list、tools/call、取消、超时和进程清理；`mcp-serve` 使用同一 SDK 暴露内建工具。

## stdio

```json
{
  "name":"local-tools",
  "transport":"stdio",
  "config":{
    "command":"/absolute/path/to/mcp-server",
    "args":["--stdio"],
    "dir":"/absolute/work/dir",
    "env":{"SERVER_MODE":"production"}
  }
}
```

通过 CLI 注册：

```bash
./agent-core mcp connect --name local-tools --transport stdio \
  --config '{"command":"/absolute/path/to/mcp-server","args":["--stdio"]}'
```

命令使用参数数组而非 shell。环境仅继承 PATH/HOME/临时目录等白名单，再叠加显式 `env`。关闭时先关会话，再清理进程组；服务异常退出会摘除该来源工具。

stdio 启动失败会保留最近 4 KiB stderr，经过 ANSI/secret 脱敏后附带最多 512 字符摘要；`/v1/mcp` 返回 `status: failed` 和可选 `error`。HTTP/SSE 不附带 stdio 信息，错误不改变协议分类或重试行为。

## streamable HTTP

```json
{
  "name":"remote-tools",
  "transport":"streamable_http",
  "config":{
    "url":"https://mcp.example.com/mcp",
    "headers":{"Authorization":"Bearer ${MCP_TOKEN}"}
  }
}
```

## legacy SSE（2024-11-05）

```json
{
  "name":"legacy-sse",
  "transport":"sse",
  "config":{"url":"https://mcp.example.com/sse"}
}
```

建连调用的超时取消不会关闭已建立会话。远程连接限制重定向次数并阻止跨源重定向，认证 header 不转发到其他 origin。日志不输出 header secret。

## 私网例外

默认拒绝 loopback、私网、link-local 和 metadata 地址。仅当 MCP 服务确实位于本机/私网时，在该服务的 config 中设置：

```json
{"url":"http://127.0.0.1:3001/mcp","allow_private":true}
```

这是逐服务、逐 endpoint 的显式例外，不会全局关闭 SSRF 防护。dial 时仍执行 DNS 解析，redirect 仍限制同源。不要用 URL userinfo 传密钥，使用 `headers`。

## 工具命名与冲突

远程工具以 `<server>__<tool>` 暴露，非字母数字/`_`/`-` 字符替换为下划线。冲突、重复服务名、半注册失败都会回滚，不留下假连接。外部工具默认高风险、需要审批。

配置保存于 SQLite，启动时恢复；旧版错误保存的嵌套 ServerConfig envelope 也可读取。恢复失败只记录警告，不伪造已连接状态。

## 作为 MCP 服务运行

```bash
./agent-core mcp-serve --config agent-core.yaml --transport stdio
```

仅支持 stdio server。协议协商、通知、并发请求、取消和错误映射由 SDK 处理。工具审批没有交互客户端时会失败关闭；生产调用方应使用只读工具或明确隔离后的权限模式。

## Host Callback

注册要求独立 `secret`。每次请求包含 JSON `{"tool":name,"args":args}`，以及：

- `X-AgentCore-Timestamp`: Unix 秒。
- `X-AgentCore-Nonce`: 每次请求唯一 UUID。
- `X-AgentCore-Signature`: 小写 hex HMAC-SHA256。

签名输入：

```text
timestamp + "\n" + nonce + "\n" + raw_request_body
```

Host 必须使用相同 secret 计算并常量时间比较，检查 timestamp 允许窗口并缓存 nonce 防重放。响应限 1 MiB；非 200 不回显响应正文。

成功响应可为 `{"result":"text"}`、错误 `{"error":"text"}`，或原始文本。Callback 注册持久化并在启动时恢复；secret 位于数据库中，数据库文件必须仅服务账号可读。`allow_private` 同样逐工具显式配置。
