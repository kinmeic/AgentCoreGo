# model-io 与自动化轨迹

## 两种采集模式

`telemetry.mode` 只有两种：

- `redacted`：默认诊断模式。HTTP headers、常见 secret 字段和 inline media 脱敏；有界异步队列，记录 dropped/truncated，Close 时 flush。
- `training`：显式训练模式。同步持久化完整 provider wire，目录 0700、文件 0600。任何写入失败或截断都会令样本失败或 `capture_complete=false`，禁止进入训练集。

```yaml
telemetry:
  mode: redacted
  directory: ./data/model-io
  queue_size: 256
  max_artifact_bytes: 4194304
  retention_files: 100
```

原始 prompt、源码和 tool 内容不会进入普通 FTS 或 HTTP 访问日志。训练采集属于敏感数据操作，必须单独授权、加密备份并设置删除策略。

## 采集内容

每次物理 provider attempt 都记录：

- request ID、trace/root/parent/run、purpose、attempt、provider/model。
- 原始 request JSON、response JSON 或原始 SSE artifact。
- headers（脱敏）、HTTP status、开始/结束时间、latency、finish reason、usage 和错误。

Purpose 包括 `main/compact/title/subagent/workflow/embedding`，避免把标题、压缩或子任务请求误当成主回答。`<think>` 过滤前的原始内容仅在采集策略允许时保留，conversation log 不作为 raw model-io。

工具记录包含 input/output、能力、审批、耗时和状态。文件变更包含 operation、revision、增删行和 diff artifact。

## 导出

```http
POST /v1/runs/{id}/trace/export
```

导出目录包含：

- `trace.jsonl`：模型、工具和文件事实。
- `manifest.json`：schema version、capture policy、fidelity、capture_complete 和数量。

缺少 request body、artifact 截断、写失败或 dropped 都会显式标记，绝不补造 messages 来伪装完整轨迹。训练模式遇到不完整采集返回 `ErrIncompleteCapture`。

Replay 仅能在 model provider 边界重放响应，不重放终端、文件、MCP、Host Callback 或其他副作用。

## 与 ZCode model-io 的关系

AgentCore 借鉴其 attempt/purpose、脱敏、artifact 和保留语义，但不把 ZCode 的有损诊断日志当成可靠训练集。AgentCore 的训练导出必须自己满足 capture_complete 和连续性校验；生产 redacted 日志只用于诊断。
