# Benchmark 体系

## Manifest

```yaml
schema_version: 1
name: smoke-test
base_url: http://127.0.0.1:9000/v1
api_key: ""
model: mock-model
fixture: ./fixtures/project
concurrency: 2
repetitions: 1
timeout_seconds: 30
permission_mode: edit
cases:
  - id: quicksort
    prompt: 实现快速排序
    expect:
      type: contains
      value: quicksort
  - id: artifact
    prompt: 创建 result.txt
    expect:
      type: file_exists
      path: result.txt
```

完整 plan 在第一次模型请求前持久化，包含 manifest digest、case、并发、重复次数、provider/model 和状态。

## 隔离与评分

每个 case/repetition 都使用独立：

- workspace 副本
- HOME 与 SQLite
- Agent runtime、工具注册表和 checkpoint
- 超时、取消和进程清理上下文

内建 grader：`exact`、`contains`、`regex`、`json_schema`、`file_exists`。`argv` 仅在 manifest `trusted: true` 时允许，使用参数数组而非 shell，并支持 `{reply}`、`{workspace}` 占位。

Agent 执行失败与 grader 失败是不同事实。plan `completed` 只表示调度收尾，不表示所有 case 通过；单项状态为 `passed/failed/skipped/cancelled`。

## 运行与报告

```bash
./agent-core benchmarks start --file benchmark.yaml
./agent-core benchmarks show --id <plan-id>
./agent-core benchmarks cancel --id <plan-id>
```

报告包含 `plan.json` 和 `results.jsonl`：duration、TTFB、usage、run/conversation、错误、stdout/grader 证据和完成时间。写盘失败停止新的模型请求，已有部分结果仍可离线读取。

Mock provider 通过只能证明协议、隔离、工具和 grader 接线，不能证明真实模型质量。真实评测必须固定 binary/config/list hash、provider/model、扩展版本和 Workspace Trust，并对真实产物独立评分。
